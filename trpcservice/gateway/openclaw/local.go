package openclaw

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/audit"
	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/dispatcher"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
	"github.com/liuzengh/trpc-agent-service/trpcservice/idempotency"
	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	servicemetrics "github.com/liuzengh/trpc-agent-service/trpcservice/metrics"
	"github.com/liuzengh/trpc-agent-service/trpcservice/policy"
	serviceruntime "github.com/liuzengh/trpc-agent-service/trpcservice/runtime"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessioncoord"
	"github.com/liuzengh/trpc-agent-service/trpcservice/worker"
)

// LocalComponent is a minimal runnable single-process composition for development.
// Production should inject SQLStore, RedisCoordinator, SQLWriteStore, and a queue.
type LocalComponent struct {
	server     *Server
	dispatcher *dispatcher.Dispatcher
	runtimes   *serviceruntime.Manager
}

// HandlerDecorator mounts a provider adapter around the shared local Gateway.
type HandlerDecorator func(*Handler, http.Handler) (http.Handler, error)

// NewLocalComponent wires the complete offline HTTP -> Runner -> Outbox chain.
func NewLocalComponent(parent context.Context, address string, file *config.File, routes Routes, decorators ...HandlerDecorator) (*LocalComponent, error) {
	if parent == nil || address == "" || file == nil || routes == nil {
		return nil, errors.New("openclaw: context, address, config, and routes are required")
	}
	writes := sessioncoord.NewMemoryWriteStore()
	coordinator, err := sessioncoord.NewCoordinator(writes)
	if err != nil {
		return nil, err
	}
	inbox := idempotency.NewMemoryStore()
	runtimes, err := serviceruntime.NewManager(worker.RuntimeFactory(writes))
	if err != nil {
		return nil, err
	}
	hub := NewHub()
	registry := NewRegistry()
	telemetry, err := servicemetrics.New("trpc-agent-service")
	if err != nil {
		_ = runtimes.Close(context.Background())
		return nil, err
	}
	redactor := servicelog.NewRedactor(nil, nil)
	auditor := audit.NewMemoryStore(redactor)
	policyEngine := &policy.Engine{
		Identity:           policy.AuthenticatedIdentityAuthorizer{},
		Budgets:            policy.NewMemoryBudget(),
		Approvals:          policy.NewMemoryApprovals(),
		CostMicrosPerToken: 1, // deterministic local accounting for the mock model.
	}
	processor := &worker.Processor{WorkerID: "local-worker", Inbox: inbox, Coordinator: coordinator, Writes: writes, Runtimes: runtimes, Snapshots: gateway.FileSnapshotResolver{File: file}, Publisher: MultiPublisher{hub, registry}, Policy: policyEngine, Audit: auditor, Redactor: redactor, Telemetry: telemetry}
	var dispatch *dispatcher.Dispatcher
	dispatch, err = dispatcher.NewWithErrorHandler(parent, processor.Process, func(ctx context.Context, request gateway.RunRequest, _ error) {
		go retryLocal(ctx, inbox, dispatch, request)
	})
	if err != nil {
		_ = runtimes.Close(context.Background())
		return nil, err
	}
	core := &Handler{Routes: routes, Inbox: inbox, Submitter: dispatch, Hub: hub, Status: registry, Canceler: processor, Approver: policyEngine, ClaimOwner: "local-gateway", Telemetry: telemetry}
	var handler http.Handler = core.RoutesHandler()
	for _, decorate := range decorators {
		if decorate == nil {
			continue
		}
		handler, err = decorate(core, handler)
		if err != nil {
			_ = dispatch.Close(context.Background())
			_ = runtimes.Close(context.Background())
			return nil, err
		}
	}
	return &LocalComponent{server: &Server{Address: address, Handler: handler}, dispatcher: dispatch, runtimes: runtimes}, nil
}

func retryLocal(ctx context.Context, inbox *idempotency.MemoryStore, dispatch *dispatcher.Dispatcher, request gateway.RunRequest) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	message := gateway.InboundMessage{TenantID: request.TenantID, AppID: request.AppID, BindingID: request.BindingID, ExternalMessageID: request.ExternalMessageID, ExternalUserID: request.ExternalUserID, ConversationID: request.ConversationID, UserID: request.UserID, SessionID: request.SessionID, Text: request.Text, TraceID: request.TraceID, ConfigVersion: request.ConfigVersion}
	claim, won, err := inbox.Claim(ctx, message, request.ClaimOwner, 30*time.Second)
	if err != nil || !won {
		return
	}
	request.InboxID, request.InboxSeq = claim.InboxID, claim.InboxSeq
	request.ClaimToken, request.ClaimAttempt, request.ClaimLeaseUntil = claim.ClaimToken, claim.Attempt, claim.LeaseUntil
	_ = dispatch.Submit(request)
}

// Start starts the local HTTP gateway.
func (component *LocalComponent) Start(ctx context.Context) error { return component.server.Start(ctx) }

// Close drains ingress, queued requests, and Runtime Bundles in dependency order.
func (component *LocalComponent) Close(ctx context.Context) error {
	return errors.Join(component.server.Close(ctx), component.dispatcher.Close(ctx), component.runtimes.Close(ctx))
}
