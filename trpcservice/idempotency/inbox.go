// Package idempotency owns durable inbound-message claims.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
)

// ErrClaimOwner indicates that a different worker owns the active claim.
var ErrClaimOwner = errors.New("idempotency: claim owned by another worker")

// Status is the durable Inbox processing state.
type Status string

const (
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusRetry      Status = "retry"
	StatusCanceled   Status = "canceled"
)

// Claim is one Inbox ownership attempt.
type Claim struct {
	InboxID, Owner, ClaimToken string
	Attempt                    int
	InboxSeq                   uint64
	Status                     Status
	LeaseUntil                 time.Time
	Message                    gateway.InboundMessage
}

// Store claims duplicate deliveries and records terminal/retry state.
type Store interface {
	Claim(context.Context, gateway.InboundMessage, string, time.Duration) (Claim, bool, error)
	Renew(context.Context, Claim, time.Duration) (Claim, error)
	Cancel(context.Context, Claim) error
	Complete(context.Context, Claim) error
	Fail(context.Context, Claim, error, time.Time) error
}

// Cancel marks an explicitly canceled active claim terminal.
func (store *MemoryStore) Cancel(_ context.Context, claim Claim) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	item := store.records[claim.InboxID]
	if item == nil || item.claim.Owner != claim.Owner || item.claim.ClaimToken != claim.ClaimToken || item.claim.Status != StatusProcessing {
		return ErrClaimOwner
	}
	item.claim.Status = StatusCanceled
	return nil
}

// Renew extends an unexpired claim only for its exact owner and token.
func (store *MemoryStore) Renew(_ context.Context, claim Claim, ttl time.Duration) (Claim, error) {
	if ttl <= 0 {
		return Claim{}, errors.New("idempotency: positive ttl is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	item := store.records[claim.InboxID]
	now := store.now().UTC()
	if item == nil || item.claim.Owner != claim.Owner || item.claim.ClaimToken != claim.ClaimToken || item.claim.Status != StatusProcessing || !now.Before(item.claim.LeaseUntil) {
		return Claim{}, ErrClaimOwner
	}
	item.claim.LeaseUntil = now.Add(ttl)
	return item.claim, nil
}

type record struct {
	claim       Claim
	nextAttempt time.Time
	lastError   string
}

// MemoryStore is a concurrency-safe reference Inbox implementation.
type MemoryStore struct {
	mu        sync.Mutex
	records   map[string]*record
	sequences map[gateway.SessionKey]uint64
	now       func() time.Time
}

// NewMemoryStore creates an empty Inbox store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]*record), sequences: make(map[gateway.SessionKey]uint64), now: time.Now}
}

// InboxID returns a deterministic tenant-scoped delivery identity.
func InboxID(message gateway.InboundMessage) string {
	return message.TenantID + "/" + message.BindingID + "/" + message.ExternalMessageID
}

// Claim atomically wins a new or expired delivery; duplicates do not run.
func (store *MemoryStore) Claim(_ context.Context, message gateway.InboundMessage, owner string, ttl time.Duration) (Claim, bool, error) {
	if message.TenantID == "" || message.BindingID == "" || message.ExternalMessageID == "" || owner == "" || ttl <= 0 {
		return Claim{}, false, errors.New("idempotency: tenant, binding, external message, owner, and positive ttl are required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	id := InboxID(message)
	existing := store.records[id]
	if existing != nil {
		if existing.claim.Status == StatusCompleted || existing.claim.Status == StatusCanceled {
			return existing.claim, false, nil
		}
		if existing.claim.Status == StatusProcessing && now.Before(existing.claim.LeaseUntil) {
			return existing.claim, false, nil
		}
		if existing.claim.Status == StatusRetry && now.Before(existing.nextAttempt) {
			return existing.claim, false, nil
		}
		existing.claim.Owner = owner
		existing.claim.Attempt++
		existing.claim.ClaimToken = fmt.Sprintf("%s#%d#%d", id, existing.claim.Attempt, now.UnixNano())
		existing.claim.Status = StatusProcessing
		existing.claim.LeaseUntil = now.Add(ttl)
		existing.claim.Message = message
		return existing.claim, true, nil
	}
	key := gateway.SessionKey{TenantID: message.TenantID, AppID: message.AppID, UserID: message.UserID, SessionID: message.SessionID}
	store.sequences[key]++
	claim := Claim{InboxID: id, Owner: owner, ClaimToken: fmt.Sprintf("%s#1#%d", id, now.UnixNano()), Attempt: 1, InboxSeq: store.sequences[key], Status: StatusProcessing, LeaseUntil: now.Add(ttl), Message: message}
	store.records[id] = &record{claim: claim}
	return claim, true, nil
}

// Complete marks a claim terminal only for its current owner.
func (store *MemoryStore) Complete(_ context.Context, claim Claim) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	item := store.records[claim.InboxID]
	if item == nil {
		return fmt.Errorf("idempotency: inbox %q not found", claim.InboxID)
	}
	if item.claim.Owner != claim.Owner || item.claim.ClaimToken != claim.ClaimToken || item.claim.Status != StatusProcessing || !store.now().UTC().Before(item.claim.LeaseUntil) {
		return ErrClaimOwner
	}
	item.claim.Status = StatusCompleted
	return nil
}

// Fail schedules a retry only for the current owner.
func (store *MemoryStore) Fail(_ context.Context, claim Claim, cause error, retryAt time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	item := store.records[claim.InboxID]
	if item == nil {
		return fmt.Errorf("idempotency: inbox %q not found", claim.InboxID)
	}
	if item.claim.Owner != claim.Owner || item.claim.ClaimToken != claim.ClaimToken || item.claim.Status != StatusProcessing {
		return ErrClaimOwner
	}
	item.claim.Status = StatusRetry
	item.nextAttempt = retryAt.UTC()
	if cause != nil {
		item.lastError = sanitizeError(cause.Error())
	}
	return nil
}
