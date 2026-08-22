package idempotency

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
)

// SQLStore implements the production PostgreSQL Inbox claim transaction.
// Its advisory transaction lock allocates a per-session inbox_seq, while the
// primary key enforces delivery idempotency across Gateway nodes.
type SQLStore struct {
	DB  *sql.DB
	Now func() time.Time
}

var _ Store = (*SQLStore)(nil)

// Cancel marks an exact active SQL claim terminal.
func (store *SQLStore) Cancel(ctx context.Context, claim Claim) error {
	if err := validateSQLClaim(store, claim); err != nil {
		return err
	}
	result, err := store.DB.ExecContext(ctx, `UPDATE inbox_messages SET status='canceled',completed_at=$6 WHERE tenant_id=$1 AND binding_id=$2 AND external_message_id=$3 AND claim_owner=$4 AND claim_token=$5 AND status='processing'`, claim.Message.TenantID, claim.Message.BindingID, claim.Message.ExternalMessageID, claim.Owner, claim.ClaimToken, store.now().UTC())
	return exactClaimResult(result, err)
}

// Renew extends an unexpired SQL claim with an owner/token CAS.
func (store *SQLStore) Renew(ctx context.Context, claim Claim, ttl time.Duration) (Claim, error) {
	if err := validateSQLClaim(store, claim); err != nil {
		return Claim{}, err
	}
	if ttl <= 0 {
		return Claim{}, errors.New("idempotency: positive ttl is required")
	}
	now := store.now().UTC()
	until := now.Add(ttl)
	result, err := store.DB.ExecContext(ctx, `UPDATE inbox_messages SET lease_until=$7 WHERE tenant_id=$1 AND binding_id=$2 AND external_message_id=$3 AND claim_owner=$4 AND claim_token=$5 AND status='processing' AND lease_until>$6`, claim.Message.TenantID, claim.Message.BindingID, claim.Message.ExternalMessageID, claim.Owner, claim.ClaimToken, now, until)
	if err := exactClaimResult(result, err); err != nil {
		return Claim{}, err
	}
	claim.LeaseUntil = until
	return claim, nil
}

// Claim atomically inserts or reclaims a delivery under serializable isolation.
func (store *SQLStore) Claim(ctx context.Context, message gateway.InboundMessage, owner string, ttl time.Duration) (Claim, bool, error) {
	if store == nil || store.DB == nil {
		return Claim{}, false, errors.New("idempotency: nil SQL database")
	}
	if message.TenantID == "" || message.AppID == "" || message.BindingID == "" || message.ExternalMessageID == "" || message.UserID == "" || message.SessionID == "" || message.ConfigVersion == 0 || owner == "" || ttl <= 0 {
		return Claim{}, false, errors.New("idempotency: complete tenant message, owner, and positive ttl are required")
	}
	tx, err := store.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Claim{}, false, err
	}
	defer tx.Rollback()
	now := store.now().UTC()
	scope := message.TenantID + "\x00" + message.AppID + "\x00" + message.UserID + "\x00" + message.SessionID
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, scope); err != nil {
		return Claim{}, false, err
	}
	var status Status
	var attempt int
	var inboxSeq uint64
	var leaseUntil, nextAttempt sql.NullTime
	var payload []byte
	var existingOwner, existingToken sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT status, attempts, inbox_seq, lease_until, next_attempt_at, payload_json, claim_owner, claim_token FROM inbox_messages WHERE tenant_id=$1 AND binding_id=$2 AND external_message_id=$3 FOR UPDATE`, message.TenantID, message.BindingID, message.ExternalMessageID).Scan(&status, &attempt, &inboxSeq, &leaseUntil, &nextAttempt, &payload, &existingOwner, &existingToken)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Claim{}, false, err
	}
	if err == nil {
		var stored gateway.InboundMessage
		if err := json.Unmarshal(payload, &stored); err != nil {
			return Claim{}, false, err
		}
		existing := Claim{InboxID: InboxID(stored), Owner: existingOwner.String, ClaimToken: existingToken.String, Attempt: attempt, InboxSeq: inboxSeq, Status: status, LeaseUntil: leaseUntil.Time, Message: stored}
		if status == StatusCompleted || status == StatusCanceled || (status == StatusProcessing && leaseUntil.Valid && now.Before(leaseUntil.Time)) || (status == StatusRetry && nextAttempt.Valid && now.Before(nextAttempt.Time)) {
			if err := tx.Commit(); err != nil {
				return Claim{}, false, err
			}
			return existing, false, nil
		}
		attempt++
		token := claimToken(existing.InboxID, attempt, now)
		until := now.Add(ttl)
		if _, err := tx.ExecContext(ctx, `UPDATE inbox_messages SET status='processing', attempts=$4, claim_owner=$5, claim_token=$6, lease_until=$7, claimed_at=$8, next_attempt_at=NULL, last_error=NULL WHERE tenant_id=$1 AND binding_id=$2 AND external_message_id=$3`, message.TenantID, message.BindingID, message.ExternalMessageID, attempt, owner, token, until, now); err != nil {
			return Claim{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Claim{}, false, err
		}
		return Claim{InboxID: existing.InboxID, Owner: owner, ClaimToken: token, Attempt: attempt, InboxSeq: inboxSeq, Status: StatusProcessing, LeaseUntil: until, Message: stored}, true, nil
	}
	var seq uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(inbox_seq),0)+1 FROM inbox_messages WHERE tenant_id=$1 AND app_id=$2 AND user_id=$3 AND session_id=$4`, message.TenantID, message.AppID, message.UserID, message.SessionID).Scan(&seq); err != nil {
		return Claim{}, false, err
	}
	payload, err = json.Marshal(message)
	if err != nil {
		return Claim{}, false, err
	}
	id := InboxID(message)
	token := claimToken(id, 1, now)
	until := now.Add(ttl)
	_, err = tx.ExecContext(ctx, `INSERT INTO inbox_messages (tenant_id,binding_id,external_message_id,inbox_id,app_id,user_id,session_id,config_version,inbox_seq,status,attempts,claim_owner,claim_token,lease_until,claimed_at,trace_id,payload_json) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'processing',1,$10,$11,$12,$13,$14,$15)`, message.TenantID, message.BindingID, message.ExternalMessageID, id, message.AppID, message.UserID, message.SessionID, message.ConfigVersion, seq, owner, token, until, now, message.TraceID, payload)
	if err != nil {
		return Claim{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, false, err
	}
	return Claim{InboxID: id, Owner: owner, ClaimToken: token, Attempt: 1, InboxSeq: seq, Status: StatusProcessing, LeaseUntil: until, Message: message}, true, nil
}

// Complete uses tenant scope, claim token, status, and lease in one CAS update.
func (store *SQLStore) Complete(ctx context.Context, claim Claim) error {
	if err := validateSQLClaim(store, claim); err != nil {
		return err
	}
	now := store.now().UTC()
	result, err := store.DB.ExecContext(ctx, `UPDATE inbox_messages SET status='completed', completed_at=$7 WHERE tenant_id=$1 AND binding_id=$2 AND external_message_id=$3 AND claim_owner=$4 AND claim_token=$5 AND status='processing' AND lease_until>$6`, claim.Message.TenantID, claim.Message.BindingID, claim.Message.ExternalMessageID, claim.Owner, claim.ClaimToken, now, now)
	return exactClaimResult(result, err)
}

// Fail schedules the exact current claim for retry.
func (store *SQLStore) Fail(ctx context.Context, claim Claim, cause error, retryAt time.Time) error {
	if err := validateSQLClaim(store, claim); err != nil {
		return err
	}
	lastError := ""
	if cause != nil {
		lastError = sanitizeError(cause.Error())
	}
	result, err := store.DB.ExecContext(ctx, `UPDATE inbox_messages SET status='retry', next_attempt_at=$6, last_error=$7, lease_until=NULL WHERE tenant_id=$1 AND binding_id=$2 AND external_message_id=$3 AND claim_owner=$4 AND claim_token=$5 AND status='processing'`, claim.Message.TenantID, claim.Message.BindingID, claim.Message.ExternalMessageID, claim.Owner, claim.ClaimToken, retryAt.UTC(), lastError)
	return exactClaimResult(result, err)
}

func validateSQLClaim(store *SQLStore, claim Claim) error {
	if store == nil || store.DB == nil {
		return errors.New("idempotency: nil SQL database")
	}
	if claim.Message.TenantID == "" || claim.Message.BindingID == "" || claim.Message.ExternalMessageID == "" || claim.Owner == "" || claim.ClaimToken == "" {
		return errors.New("idempotency: tenant-scoped claim identity is required")
	}
	return nil
}
func exactClaimResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrClaimOwner
	}
	return nil
}
func claimToken(id string, attempt int, now time.Time) string {
	return fmt.Sprintf("%s#%d#%d", id, attempt, now.UnixNano())
}
func (store *SQLStore) now() time.Time {
	if store.Now != nil {
		return store.Now()
	}
	return time.Now()
}
