package openclaw

import (
	"context"
	"errors"
	"net/http"

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
	poller     *worker.InboxPoller
	dispatcher *dispatcher.Dispatcher
	runtimes   *serviceruntime.Manager
}

// HandlerDecorator mounts a provider adapter around the shared local Gateway.
type HandlerDecorator func(*Handler, http.Handler) (http.Handler, error)

// ComponentDependencies selects durable stores without coupling the HTTP
// protocol package to concrete database clients.
type ComponentDependencies struct {
	Inbox          idempotency.ReadyStore
	Coordinator    sessioncoord.LeaseCoordinator
	Writes         worker.PlatformStore
	RuntimeFactory serviceruntime.Factory
	Audit          audit.Store
	EventBus       EventBus
	WorkerID       string
	// Snapshots optionally resolves pinned published versions from the
	// control-plane store. Nil falls back to the static startup file, which
	// is only appropriate for offline development.
	Snapshots gateway.SnapshotResolver
}

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
	redactor := servicelog.NewRedactor(nil, nil)
	return NewComponent(parent, address, file, routes, ComponentDependencies{
		Inbox: inbox, Coordinator: coordinator, Writes: writes,
		RuntimeFactory: worker.TestRuntimeFactory(writes), Audit: audit.NewMemoryStore(redactor),
		WorkerID: "local-worker",
	}, decorators...)
}

// NewComponent wires a single-process Gateway and Worker to injected durable
// stores. The caller owns the database and Redis clients behind dependencies.
func NewComponent(parent context.Context, address string, file *config.File, routes Routes, dependencies ComponentDependencies, decorators ...HandlerDecorator) (*LocalComponent, error) {
	if parent == nil || address == "" || file == nil || routes == nil {
		return nil, errors.New("openclaw: context, address, config, and routes are required")
	}
	if dependencies.Inbox == nil || dependencies.Coordinator == nil || dependencies.Writes == nil || dependencies.RuntimeFactory == nil || dependencies.Audit == nil {
		return nil, errors.New("openclaw: inbox, coordinator, writes, runtime factory, and audit are required")
	}
	if dependencies.WorkerID == "" {
		dependencies.WorkerID = "worker"
	}
	runtimes, err := serviceruntime.NewManager(dependencies.RuntimeFactory)
	if err != nil {
		return nil, err
	}
	bus := dependencies.EventBus
	if bus == nil {
		bus = NewHub()
	}
	registry := NewRegistry()
	telemetry, err := servicemetrics.New("trpc-agent-service")
	if err != nil {
		_ = runtimes.Close(context.Background())
		return nil, err
	}
	redactor := servicelog.NewRedactor(nil, nil)
	policyEngine := &policy.Engine{
		Identity:           policy.AuthenticatedIdentityAuthorizer{},
		Budgets:            policy.NewMemoryBudget(),
		Approvals:          policy.NewMemoryApprovals(),
		CostMicrosPerToken: 1, // deterministic local accounting for the mock model.
	}
	snapshots := dependencies.Snapshots
	if snapshots == nil {
		snapshots = gateway.FileSnapshotResolver{File: file}
	}
	processor := &worker.Processor{WorkerID: dependencies.WorkerID, Inbox: dependencies.Inbox, Coordinator: dependencies.Coordinator, Writes: dependencies.Writes, Runtimes: runtimes, Snapshots: snapshots, Publisher: MultiPublisher{bus, registry}, Policy: policyEngine, Audit: dependencies.Audit, Redactor: redactor, Telemetry: telemetry}
	dispatch, err := dispatcher.New(parent, processor.Process)
	if err != nil {
		_ = runtimes.Close(context.Background())
		return nil, err
	}
	poller, err := worker.NewInboxPoller(parent, dependencies.Inbox, dispatch, worker.InboxPollerConfig{Owner: dependencies.WorkerID + ":inbox"})
	if err != nil {
		_ = dispatch.Close(context.Background())
		_ = runtimes.Close(context.Background())
		return nil, err
	}
	core := &Handler{Routes: routes, Inbox: dependencies.Inbox, Submitter: dispatch, Hub: bus, Status: registry, Canceler: processor, Approver: policyEngine, ClaimOwner: dependencies.WorkerID + ":gateway", Telemetry: telemetry}
	var handler http.Handler = core.RoutesHandler()
	for _, decorate := range decorators {
		if decorate == nil {
			continue
		}
		handler, err = decorate(core, handler)
		if err != nil {
			_ = poller.Close(context.Background())
			_ = dispatch.Close(context.Background())
			_ = runtimes.Close(context.Background())
			return nil, err
		}
	}
	return &LocalComponent{server: &Server{Address: address, Handler: handler}, poller: poller, dispatcher: dispatch, runtimes: runtimes}, nil
}

// Start starts the local HTTP gateway.
func (component *LocalComponent) Start(ctx context.Context) error { return component.server.Start(ctx) }

// Close drains ingress, queued requests, and Runtime Bundles in dependency order.
func (component *LocalComponent) Close(ctx context.Context) error {
	return errors.Join(component.server.Close(ctx), component.poller.Close(ctx), component.dispatcher.Close(ctx), component.runtimes.Close(ctx))
}
