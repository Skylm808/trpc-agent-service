// Package worker connects durable gateway work to fenced tRPC-Agent runtimes.
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
	"github.com/liuzengh/trpc-agent-service/trpcservice/idempotency"
	serviceruntime "github.com/liuzengh/trpc-agent-service/trpcservice/runtime"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessioncoord"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storage"
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
	defer func() {
		if runErr != nil {
			processor.publish(gateway.RunEvent{Type: "run.error", RequestID: request.InboxID, TraceID: request.TraceID, Error: runErr.Error(), Terminal: true})
		}
	}()
	claim := idempotency.Claim{InboxID: request.InboxID, Owner: request.ClaimOwner, ClaimToken: request.ClaimToken, Attempt: request.ClaimAttempt, InboxSeq: request.InboxSeq, LeaseUntil: request.ClaimLeaseUntil, Message: gateway.InboundMessage{TenantID: request.TenantID, BindingID: request.BindingID, ExternalMessageID: request.ExternalMessageID}}
	completed := false
	defer func() {
		if !completed && runErr != nil {
			_ = processor.Inbox.Fail(context.Background(), claim, runErr, time.Now().UTC().Add(processor.retryDelay()))
		}
	}()
	lease, err := processor.Coordinator.Acquire(ctx, request.Key(), processor.WorkerID, processor.leaseTTL())
	if err != nil {
		return err
	}
	defer processor.Coordinator.Release(lease)
	snapshot, err := processor.Snapshots.Resolve(ctx, request.TenantID, request.AppID, request.ConfigVersion)
	if err != nil {
		return err
	}
	runtimeLease, err := processor.Runtimes.Acquire(snapshot)
	if err != nil {
		return err
	}
	defer runtimeLease.Release()
	processor.publish(gateway.RunEvent{Type: "run.started", RequestID: request.InboxID, TraceID: request.TraceID})
	runCtx, cancelRun := context.WithCancel(ctx)
	renewDone := make(chan struct{})
	renewFailure := make(chan error, 1)
	go processor.renew(runCtx, cancelRun, lease, renewDone, renewFailure)
	result, err := runtimeLease.Runtime.Run(sessioncoord.WithLease(runCtx, lease), serviceruntime.RunInput{RequestID: request.InboxID, UserID: request.UserID, SessionID: request.SessionID, Text: request.Text})
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
		return err
	}
	reply := projectEvents(processor.Publisher, request, result)
	if reply == "" {
		return errors.New("worker: runner produced no final message")
	}
	eventID := "agent:" + request.InboxID
	seq, err := processor.Writes.CommitTurn(ctx, sessioncoord.TurnWrite{Key: request.Key(), Fence: lease.Token, InboxSeq: request.InboxSeq, InboxID: request.InboxID, EventID: eventID, EventType: "assistant", Payload: reply, TraceID: request.TraceID, StateDelta: map[string]string{"last_inbox_id": request.InboxID}})
	if err != nil {
		return err
	}
	if err := processor.Writes.PublishSummary(ctx, request.Key(), lease.Token, sessioncoord.Summary{Version: seq, CutoffEventSeq: seq, Content: reply}); err != nil {
		return err
	}
	if err := processor.Writes.UpsertMemory(ctx, request.Key(), lease.Token, sessioncoord.Memory{MemoryID: "memory:" + eventID, SourceEventID: eventID, SourceEventSeq: seq, Version: 1, Status: "active", Content: reply}); err != nil {
		return err
	}
	outbound := gateway.OutboundMessage{TenantID: request.TenantID, AppID: request.AppID, BindingID: request.BindingID, OutboxID: "outbox:" + request.InboxID, DedupeKey: "reply:" + request.InboxID, UserID: request.UserID, SessionID: request.SessionID, Text: reply, TraceID: request.TraceID, SourceInboxID: request.InboxID, SourceEventID: eventID}
	if err := processor.Writes.PublishOutbox(ctx, request.Key(), lease.Token, outbound); err != nil {
		return err
	}
	if err := processor.Inbox.Complete(ctx, claim); err != nil {
		return err
	}
	completed = true
	processor.publish(gateway.RunEvent{Type: "run.completed", RequestID: request.InboxID, TraceID: request.TraceID, Message: reply, Terminal: true})
	return nil
}

func (processor *Processor) validate(request gateway.RunRequest) error {
	if processor == nil || processor.WorkerID == "" || processor.Inbox == nil || processor.Coordinator == nil || processor.Writes == nil || processor.Runtimes == nil || processor.Snapshots == nil {
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

func (processor *Processor) renew(ctx context.Context, cancel context.CancelFunc, lease sessioncoord.Lease, done chan<- struct{}, failure chan<- error) {
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
		}
	}
}

func projectEvents(publisher gateway.EventPublisher, request gateway.RunRequest, result serviceruntime.RunResult) string {
	var reply string
	for _, item := range result.Events {
		if item == nil || item.Response == nil {
			continue
		}
		if item.Response.IsToolCallResponse() || item.Response.IsToolResultResponse() {
			name := ""
			for _, choice := range item.Choices {
				if len(choice.Message.ToolCalls) > 0 {
					name = choice.Message.ToolCalls[0].Function.Name
					break
				}
			}
			if publisher != nil {
				publisher.Publish(gateway.RunEvent{Type: "tool", RequestID: request.InboxID, TraceID: request.TraceID, ToolName: name})
			}
		}
		for _, choice := range item.Choices {
			content := strings.TrimSpace(choice.Message.Content)
			if content == "" {
				continue
			}
			if item.Response.IsFinalResponse() {
				reply = content
			}
			if publisher != nil {
				publisher.Publish(gateway.RunEvent{Type: "message.delta", RequestID: request.InboxID, TraceID: request.TraceID, Delta: content})
			}
		}
	}
	return reply
}
