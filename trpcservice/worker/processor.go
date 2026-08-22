// Package worker connects durable gateway work to fenced tRPC-Agent runtimes.
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/audit"
	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
	"github.com/liuzengh/trpc-agent-service/trpcservice/idempotency"
	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	servicemetrics "github.com/liuzengh/trpc-agent-service/trpcservice/metrics"
	"github.com/liuzengh/trpc-agent-service/trpcservice/policy"
	serviceruntime "github.com/liuzengh/trpc-agent-service/trpcservice/runtime"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessioncoord"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storage"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

// Processor executes one already-claimed Inbox message.
type Processor struct {
	WorkerID    string
	Inbox       idempotency.Store
	Coordinator sessioncoord.LeaseCoordinator
	Writes      sessioncoord.WriteStore
	Runtimes    *serviceruntime.Manager
	Snapshots   gateway.SnapshotResolver
	Publisher   gateway.EventPublisher
	LeaseTTL    time.Duration
	RetryDelay  time.Duration
	Policy      *policy.Engine
	Audit       audit.Store
	Redactor    *servicelog.Redactor
	Telemetry   *servicemetrics.Telemetry
	activeMu    sync.Mutex
	active      map[string]context.CancelFunc
	canceled    map[string]bool
}

// RuntimeFactory creates tenant services and gates every session mutation with the current fence.
type PlatformStore interface {
	sessioncoord.WriteStore
	sessioncoord.FenceValidator
}

func RuntimeFactory(writes PlatformStore) serviceruntime.Factory {
	return func(snapshot config.RuntimeSnapshot) (serviceruntime.Runtime, error) {
		services, err := storage.NewInMemory(snapshot.App().Storage)
		if err != nil {
			return nil, err
		}
		fenced, err := sessioncoord.NewFencedSessionService(services.Session, writes)
		if err != nil {
			_ = services.Close()
			return nil, err
		}
		services.Session = fenced
		bundle, err := serviceruntime.NewBundleWithServices(snapshot, services)
		if err != nil {
			_ = services.Close()
			return nil, err
		}
		return bundle, nil
	}
}

// Process follows inbox -> event/state -> summary/memory -> outbox -> inbox complete.
func (processor *Processor) Process(ctx context.Context, request gateway.RunRequest) (runErr error) {
	if err := processor.validate(request); err != nil {
		return err
	}
	ctx = processor.Telemetry.Extract(ctx, request.TraceContext)
	ctx, runSpan := processor.Telemetry.Start(ctx, "worker.run", processor.spanFields(request))
	defer runSpan.End()
	started := time.Now()
	var projection *eventProjection
	governanceDecision := "allow"
	redact := processor.redact
	auditEnabled, auditStoreContent := false, false
	tenantRedactor := servicelog.NewRedactor(nil, nil)
	defer func() {
		decision, errorType := governanceDecision, ""
		details := map[string]any{}
		if runErr != nil {
			if decision == "allow" {
				decision = "error"
			}
			errorType = fmt.Sprintf("%T", runErr)
			if auditStoreContent {
				details["error"] = redact(runErr.Error())
			}
		}
		tokens := int64(0)
		toolName := ""
		if projection != nil {
			tokens = int64(projection.totalTokens)
			toolName = projection.lastTool
		}
		processor.Telemetry.Request(ctx, servicemetrics.Labels{TenantID: request.TenantID, AppID: request.AppID, Channel: request.BindingID, Operation: "runner", Status: decision}, time.Since(started), tokens, 0)
		if auditEnabled && processor.Audit != nil {
			record := audit.Record{TenantID: request.TenantID, Channel: request.BindingID, UserID: request.UserID, SessionID: request.SessionID, AgentName: request.AppID, ToolName: toolName, Decision: decision, Latency: time.Since(started), ErrorType: errorType, TraceID: request.TraceID, RequestID: request.InboxID, Details: details}
			record.Channel = tenantRedactor.RedactField("channel", record.Channel)
			record.UserID = tenantRedactor.RedactField("user_id", record.UserID)
			record.SessionID = tenantRedactor.RedactField("session_id", record.SessionID)
			record.ToolName = tenantRedactor.RedactField("tool_name", record.ToolName)
			auditCtx, cancelAudit := context.WithTimeout(context.Background(), 2*time.Second)
			auditErr := processor.Audit.Append(auditCtx, record)
			cancelAudit()
			if auditErr != nil {
				processor.Telemetry.Request(ctx, servicemetrics.Labels{TenantID: request.TenantID, AppID: request.AppID, Channel: request.BindingID, Operation: "audit", Status: "failed"}, 0, 0, 0)
			}
		}
	}()
	defer func() {
		if runErr != nil {
			processor.publish(gateway.RunEvent{Type: "run.error", RequestID: request.InboxID, SessionID: request.SessionID, TraceID: request.TraceID, Error: redact(runErr.Error()), Terminal: true})
		}
	}()
	claim := idempotency.Claim{InboxID: request.InboxID, Owner: request.ClaimOwner, ClaimToken: request.ClaimToken, Attempt: request.ClaimAttempt, InboxSeq: request.InboxSeq, LeaseUntil: request.ClaimLeaseUntil, Message: gateway.InboundMessage{TenantID: request.TenantID, BindingID: request.BindingID, ExternalMessageID: request.ExternalMessageID}}
	completed := false
	defer func() {
		if !completed && runErr != nil {
			_ = processor.Inbox.Fail(context.Background(), claim, errors.New(redact(runErr.Error())), time.Now().UTC().Add(processor.retryDelay()))
		}
	}()
	leaseCtx, leaseSpan := processor.Telemetry.Start(ctx, "session.lease", processor.spanFields(request))
	leaseStarted := time.Now()
	lease, err := processor.Coordinator.Acquire(leaseCtx, request.Key(), processor.WorkerID, processor.leaseTTL())
	leaseSpan.End()
	processor.observeOperation(leaseCtx, request, "session_lease", leaseStarted, err)
	if err != nil {
		return err
	}
	defer processor.Coordinator.Release(lease)
	snapshot, err := processor.Snapshots.Resolve(ctx, request.TenantID, request.AppID, request.ConfigVersion)
	if err != nil {
		return err
	}
	auditPolicy := snapshot.Audit()
	auditEnabled, auditStoreContent = auditPolicy.Enabled, auditPolicy.StoreContent
	tenantRedactor = servicelog.NewRedactor(auditPolicy.RedactFields, nil)
	redact = func(value string) string { return tenantRedactor.RedactString(processor.redact(value)) }
	estimatedTokens := int64(len(request.Text)/4 + 1)
	policyRequest := policy.Request{TenantID: request.TenantID, AppID: request.AppID, UserID: request.UserID, RequestID: request.InboxID, Policy: snapshot.App().Tools, EstimatedTokens: estimatedTokens, EstimatedCostMicros: processor.Policy.EstimateCost(estimatedTokens)}
	controls, err := processor.Policy.Evaluate(ctx, policyRequest)
	if err != nil {
		if isGovernanceDenial(err) {
			governanceDecision = "deny"
			if rejectErr := processor.Inbox.Reject(context.Background(), claim); rejectErr != nil {
				return errors.Join(err, rejectErr)
			}
			completed = true
		}
		return err
	}
	runtimeLease, err := processor.Runtimes.Acquire(snapshot)
	if err != nil {
		return err
	}
	defer runtimeLease.Release()
	processor.publish(gateway.RunEvent{Type: "run.started", RequestID: request.InboxID, SessionID: request.SessionID, TraceID: request.TraceID})
	runCtx, cancelRun := context.WithCancel(ctx)
	runCtx = policy.WithRequest(runCtx, processor.Policy, policyRequest)
	runCtx = servicelog.WithRedactor(runCtx, tenantRedactor)
	runCtx = servicemetrics.WithTelemetry(runCtx, processor.Telemetry, processor.spanFields(request))
	projection = &eventProjection{publisher: processor.Publisher, request: request, redact: redact}
	projection.onUsage = func(tokens int64) error {
		return processor.Policy.Reconcile(runCtx, policyRequest, tokens, processor.Policy.EstimateCost(tokens))
	}
	projection.cancel = cancelRun
	processor.register(request.InboxID, cancelRun)
	defer processor.unregister(request.InboxID)
	renewDone := make(chan struct{})
	renewFailure := make(chan error, 1)
	go processor.renew(runCtx, cancelRun, lease, claim, renewDone, renewFailure)
	runnerCtx, runnerSpan := processor.Telemetry.Start(sessioncoord.WithLease(runCtx, lease), "runner.execute", processor.spanFields(request))
	runInput := serviceruntime.RunInput{RequestID: request.InboxID, UserID: request.UserID, SessionID: request.SessionID, Text: request.Text, Observer: projection.Observe, ToolFilter: controls.Visibility, ToolExecutionFilter: controls.Execution, ToolPermissionPolicy: controls.Permission}
	_, err = runtimeLease.Runtime.Run(runnerCtx, runInput)
	if err == nil && projection.pendingTool != "" {
		processor.publish(gateway.RunEvent{Type: "run.approval_required", RequestID: request.InboxID, SessionID: request.SessionID, TraceID: request.TraceID, Stage: "approval_required", ToolName: projection.pendingTool, ToolCallID: projection.pendingCall})
		if approvalErr := processor.Policy.WaitApproval(runnerCtx, policyRequest, projection.pendingTool); approvalErr != nil {
			err = approvalErr
		} else if resumer, ok := runtimeLease.Runtime.(serviceruntime.ToolResumer); !ok {
			err = errors.New("worker: runtime does not support approved tool resume")
		} else {
			_, err = resumer.ResumeTool(runnerCtx, serviceruntime.ToolResume{Input: runInput, ToolName: projection.pendingTool, ToolCallID: projection.pendingCall, Arguments: projection.pendingArgs})
		}
	}
	if projection.policyErr != nil {
		err = errors.Join(err, projection.policyErr)
	}
	runnerSpan.End()
	cancelRun()
	<-renewDone
	select {
	case renewErr := <-renewFailure:
		if renewErr != nil {
			err = errors.Join(err, renewErr)
		}
	default:
	}
	if err != nil {
		if isGovernanceDenial(err) {
			governanceDecision = "deny"
			if rejectErr := processor.Inbox.Reject(context.Background(), claim); rejectErr != nil {
				return errors.Join(err, rejectErr)
			}
			completed = true
			return err
		}
		if errors.Is(err, context.Canceled) && processor.takeCanceled(request.InboxID) {
			if cancelErr := processor.Inbox.Cancel(context.Background(), claim); cancelErr != nil {
				return cancelErr
			}
			completed = true
			processor.publish(gateway.RunEvent{Type: "run.canceled", RequestID: request.InboxID, SessionID: request.SessionID, TraceID: request.TraceID, Terminal: true})
			return nil
		}
		return err
	}
	reply := projection.reply
	if reply == "" {
		return errors.New("worker: runner produced no final message")
	}
	reply = processor.redact(reply)
	eventID := "agent:" + request.InboxID
	writeCtx, writeSpan := processor.Telemetry.Start(ctx, "session.write", processor.spanFields(request))
	writeStarted := time.Now()
	seq, err := processor.Writes.CommitTurn(writeCtx, sessioncoord.TurnWrite{Key: request.Key(), Fence: lease.Token, InboxSeq: request.InboxSeq, InboxID: request.InboxID, EventID: eventID, EventType: "assistant", Payload: reply, TraceID: request.TraceID, StateDelta: map[string]string{"last_inbox_id": request.InboxID}})
	writeSpan.End()
	processor.observeOperation(writeCtx, request, "session_write", writeStarted, err)
	if err != nil {
		return err
	}
	derivedCtx, derivedSpan := processor.Telemetry.Start(ctx, "memory.summary.write", processor.spanFields(request))
	derivedStarted := time.Now()
	if err := processor.Writes.PublishSummary(derivedCtx, request.Key(), lease.Token, sessioncoord.Summary{Version: seq, CutoffEventSeq: seq, Content: reply}); err != nil {
		derivedSpan.End()
		processor.observeOperation(derivedCtx, request, "memory_summary", derivedStarted, err)
		return err
	}
	if err := processor.Writes.UpsertMemory(derivedCtx, request.Key(), lease.Token, sessioncoord.Memory{MemoryID: "memory:" + eventID, SourceEventID: eventID, SourceEventSeq: seq, Version: 1, Status: "active", Content: reply}); err != nil {
		derivedSpan.End()
		processor.observeOperation(derivedCtx, request, "memory_summary", derivedStarted, err)
		return err
	}
	derivedSpan.End()
	processor.observeOperation(derivedCtx, request, "memory_summary", derivedStarted, nil)
	outbound := gateway.OutboundMessage{TenantID: request.TenantID, AppID: request.AppID, BindingID: request.BindingID, OutboxID: "outbox:" + request.InboxID, DedupeKey: "reply:" + request.InboxID, UserID: request.UserID, SessionID: request.SessionID, Text: reply, TraceID: request.TraceID, SourceInboxID: request.InboxID, SourceEventID: eventID}
	outboxCtx, outboxSpan := processor.Telemetry.Start(ctx, "outbox.write", processor.spanFields(request))
	outboxStarted := time.Now()
	if err := processor.Writes.PublishOutbox(outboxCtx, request.Key(), lease.Token, outbound); err != nil {
		outboxSpan.End()
		processor.observeOperation(outboxCtx, request, "outbox_write", outboxStarted, err)
		return err
	}
	outboxSpan.End()
	processor.observeOperation(outboxCtx, request, "outbox_write", outboxStarted, nil)
	if err := processor.Inbox.Complete(ctx, claim); err != nil {
		return err
	}
	completed = true
	processor.publish(gateway.RunEvent{Type: "run.completed", RequestID: request.InboxID, SessionID: request.SessionID, TraceID: request.TraceID, Message: reply, Terminal: true})
	return nil
}

func isGovernanceDenial(err error) bool {
	return errors.Is(err, policy.ErrIdentityDenied) || errors.Is(err, policy.ErrBudgetExceeded) || errors.Is(err, policy.ErrToolDenied)
}

// Cancel cancels an active Runner request. Queued cancellation belongs to the durable queue.
func (processor *Processor) Cancel(requestID string) bool {
	processor.activeMu.Lock()
	cancel := processor.active[requestID]
	if cancel != nil {
		if processor.canceled == nil {
			processor.canceled = make(map[string]bool)
		}
		processor.canceled[requestID] = true
	}
	processor.activeMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
func (processor *Processor) register(requestID string, cancel context.CancelFunc) {
	processor.activeMu.Lock()
	if processor.active == nil {
		processor.active = make(map[string]context.CancelFunc)
	}
	processor.active[requestID] = cancel
	processor.activeMu.Unlock()
}
func (processor *Processor) unregister(requestID string) {
	processor.activeMu.Lock()
	delete(processor.active, requestID)
	processor.activeMu.Unlock()
}
func (processor *Processor) takeCanceled(requestID string) bool {
	processor.activeMu.Lock()
	defer processor.activeMu.Unlock()
	value := processor.canceled[requestID]
	delete(processor.canceled, requestID)
	return value
}

func (processor *Processor) validate(request gateway.RunRequest) error {
	if processor == nil || processor.WorkerID == "" || processor.Inbox == nil || processor.Coordinator == nil || processor.Writes == nil || processor.Runtimes == nil || processor.Snapshots == nil || processor.Policy == nil {
		return errors.New("worker: processor dependencies are incomplete")
	}
	if request.InboxID == "" || request.InboxSeq == 0 || request.TenantID == "" || request.AppID == "" || request.BindingID == "" || request.ExternalMessageID == "" || request.UserID == "" || request.SessionID == "" || request.Text == "" || request.ClaimOwner == "" || request.ClaimToken == "" || request.ConfigVersion == 0 {
		return errors.New("worker: request routing, claim, config version, and text are required")
	}
	return nil
}

func (processor *Processor) leaseTTL() time.Duration {
	if processor.LeaseTTL > 0 {
		return processor.LeaseTTL
	}
	return 30 * time.Second
}
func (processor *Processor) retryDelay() time.Duration {
	if processor.RetryDelay > 0 {
		return processor.RetryDelay
	}
	return time.Second
}
func (processor *Processor) publish(event gateway.RunEvent) {
	if processor.Publisher != nil {
		processor.Publisher.Publish(event)
	}
}

func (processor *Processor) renew(ctx context.Context, cancel context.CancelFunc, lease sessioncoord.Lease, claim idempotency.Claim, done chan<- struct{}, failure chan<- error) {
	defer close(done)
	interval := processor.leaseTTL() / 3
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var err error
			lease, err = processor.Coordinator.Renew(ctx, lease, processor.leaseTTL())
			if err != nil {
				failure <- fmt.Errorf("worker: renew session lease: %w", err)
				cancel()
				return
			}
			claim, err = processor.Inbox.Renew(ctx, claim, processor.leaseTTL())
			if err != nil {
				failure <- fmt.Errorf("worker: renew inbox claim: %w", err)
				cancel()
				return
			}
		}
	}
}

type eventProjection struct {
	publisher   gateway.EventPublisher
	request     gateway.RunRequest
	reply       string
	lastTool    string
	totalTokens int
	redact      func(string) string
	pendingTool string
	pendingCall string
	pendingArgs []byte
	onUsage     func(int64) error
	cancel      context.CancelFunc
	policyErr   error
}

func (projection *eventProjection) Observe(item *event.Event) {
	if item == nil || item.Response == nil {
		return
	}
	base := gateway.RunEvent{RequestID: projection.request.InboxID, SessionID: projection.request.SessionID, TraceID: projection.request.TraceID}
	if item.Usage != nil {
		base.PromptTokens = item.Usage.PromptTokens
		base.CompletionTokens = item.Usage.CompletionTokens
		base.TotalTokens = item.Usage.TotalTokens
		projection.totalTokens = item.Usage.TotalTokens
		if projection.onUsage != nil && projection.policyErr == nil {
			if err := projection.onUsage(int64(item.Usage.TotalTokens)); err != nil {
				projection.policyErr = err
				if projection.cancel != nil {
					projection.cancel()
				}
			}
		}
	}
	if item.Response.IsToolCallResponse() {
		base.Type, base.Stage, base.ToolStatus = "run.progress", "running_tool", "running"
		for _, choice := range item.Choices {
			calls := choice.Message.ToolCalls
			if len(calls) == 0 {
				calls = choice.Delta.ToolCalls
			}
			if len(calls) > 0 {
				base.ToolName = calls[0].Function.Name
				base.ToolCallID = calls[0].ID
				projection.pendingTool = base.ToolName
				projection.pendingCall = base.ToolCallID
				projection.pendingArgs = append([]byte(nil), calls[0].Function.Arguments...)
				break
			}
		}
		projection.publish(base)
		projection.lastTool = base.ToolName
		return
	}
	if item.Response.IsToolResultResponse() {
		projection.pendingTool, projection.pendingCall, projection.pendingArgs = "", "", nil
		base.Type, base.Stage, base.ToolStatus = "run.progress", "tool_completed", "completed"
		for _, choice := range item.Choices {
			if choice.Message.ToolID != "" {
				base.ToolCallID = choice.Message.ToolID
				break
			}
			if choice.Delta.ToolID != "" {
				base.ToolCallID = choice.Delta.ToolID
				break
			}
		}
		projection.publish(base)
		return
	}
	if item.IsError() {
		base.Type = "run.progress"
		base.Stage = "runner_error"
		if item.Error != nil {
			base.Error = item.Error.Error()
		}
		projection.publish(base)
		return
	}
	for _, choice := range item.Choices {
		delta := choice.Delta.Content
		if item.Object != model.ObjectTypeChatCompletionChunk || strings.TrimSpace(delta) == "" {
			delta = choice.Message.Content
		}
		delta = strings.TrimSpace(delta)
		if delta == "" {
			continue
		}
		if item.Response.IsFinalResponse() {
			projection.reply = delta
			base.Type = "message.completed"
			base.Message = delta
		} else {
			base.Type = "message.delta"
			base.Delta = delta
		}
		projection.publish(base)
	}
}
func (projection *eventProjection) publish(event gateway.RunEvent) {
	if projection.redact != nil {
		event.Delta = projection.redact(event.Delta)
		event.Message = projection.redact(event.Message)
		event.Error = projection.redact(event.Error)
	}
	if projection.publisher != nil {
		projection.publisher.Publish(event)
	}
}

func (processor *Processor) redact(value string) string {
	if processor == nil || processor.Redactor == nil {
		return servicelog.NewRedactor(nil, nil).RedactString(value)
	}
	return processor.Redactor.RedactString(value)
}
func (processor *Processor) spanFields(request gateway.RunRequest) servicemetrics.SpanFields {
	return servicemetrics.SpanFields{TenantID: request.TenantID, AppID: request.AppID, Channel: request.BindingID, RequestID: request.InboxID, TraceID: request.TraceID}
}
func (processor *Processor) observeOperation(ctx context.Context, request gateway.RunRequest, operation string, started time.Time, err error) {
	status := "success"
	if err != nil {
		status = "failed"
	}
	processor.Telemetry.Request(ctx, servicemetrics.Labels{TenantID: request.TenantID, AppID: request.AppID, Channel: request.BindingID, Operation: operation, Status: status}, time.Since(started), 0, 0)
}
