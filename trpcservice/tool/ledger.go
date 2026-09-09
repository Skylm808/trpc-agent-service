package tool

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type ExecutionStatus string

const (
	ExecutionRunning        ExecutionStatus = "running"
	ExecutionCompleted      ExecutionStatus = "completed"
	ExecutionFailed         ExecutionStatus = "failed"
	ExecutionOutcomeUnknown ExecutionStatus = "outcome_unknown"
)

type ExecutionRecord struct {
	TenantID, RequestID, ToolCallID, ToolName string
	ArgumentsHash, IdempotencyKey, TraceID    string
	Status                                    ExecutionStatus
	ResultHash, ErrorType                     string
}

type ExecutionStore interface {
	Begin(context.Context, ExecutionRecord) error
	Complete(context.Context, string, string, string, string) error
	Fail(context.Context, string, string, string, string, ExecutionStatus) error
}

// SQLExecutionStore is the durable tool side-effect ledger. A unique
// tenant/request/call key makes retries observe the previous invocation.
type SQLExecutionStore struct {
	DB  *sql.DB
	Now func() time.Time
}

func (store *SQLExecutionStore) Begin(ctx context.Context, record ExecutionRecord) error {
	if store == nil || store.DB == nil || ctx == nil || record.TenantID == "" || record.RequestID == "" || record.ToolCallID == "" || record.ToolName == "" {
		return errors.New("tool: execution ledger request is invalid")
	}
	now := time.Now
	if store.Now != nil {
		now = store.Now
	}
	_, err := store.DB.ExecContext(ctx, `INSERT INTO tool_executions (tenant_id,request_id,tool_call_id,tool_name,arguments_hash,idempotency_key,status,trace_id,started_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT (tenant_id,request_id,tool_call_id) DO NOTHING`, record.TenantID, record.RequestID, record.ToolCallID, record.ToolName, record.ArgumentsHash, record.IdempotencyKey, ExecutionRunning, record.TraceID, now().UTC())
	return err
}

func (store *SQLExecutionStore) Complete(ctx context.Context, tenantID, requestID, callID, resultHash string) error {
	return store.update(ctx, tenantID, requestID, callID, ExecutionCompleted, resultHash, "")
}

func (store *SQLExecutionStore) Fail(ctx context.Context, tenantID, requestID, callID, errorType string, status ExecutionStatus) error {
	if status != ExecutionOutcomeUnknown {
		status = ExecutionFailed
	}
	return store.update(ctx, tenantID, requestID, callID, status, "", errorType)
}

func (store *SQLExecutionStore) update(ctx context.Context, tenantID, requestID, callID string, status ExecutionStatus, resultHash, errorType string) error {
	if store == nil || store.DB == nil || ctx == nil || tenantID == "" || requestID == "" || callID == "" {
		return errors.New("tool: execution ledger update is invalid")
	}
	_, err := store.DB.ExecContext(ctx, `UPDATE tool_executions SET status=$4,result_hash=NULLIF($5,''),error_type=NULLIF($6,''),completed_at=CASE WHEN $4 IN ('completed','failed','outcome_unknown') THEN NOW() ELSE completed_at END,updated_at=NOW() WHERE tenant_id=$1 AND request_id=$2 AND tool_call_id=$3`, tenantID, requestID, callID, status, resultHash, errorType)
	return err
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func executionCallID(toolName string, ordinal uint64) string {
	return fmt.Sprintf("%s:%d", toolName, ordinal)
}
