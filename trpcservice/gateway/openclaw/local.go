package openclaw

import (
	"context"
	"errors"

	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/dispatcher"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
	"github.com/liuzengh/trpc-agent-service/trpcservice/idempotency"
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

// NewLocalComponent wires the complete offline HTTP -> Runner -> Outbox chain.
func NewLocalComponent(parent context.Context, address string, file *config.File, routes Routes) (*LocalComponent, error) {
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
	processor := &worker.Processor{WorkerID: "local-worker", Inbox: inbox, Coordinator: coordinator, Writes: writes, Runtimes: runtimes, Snapshots: gateway.FileSnapshotResolver{File: file}, Publisher: hub}
	dispatch, err := dispatcher.New(parent, processor.Process)
	if err != nil {
		_ = runtimes.Close(context.Background())
		return nil, err
	}
	handler := (&Handler{Routes: routes, Inbox: inbox, Submitter: dispatch, Hub: hub, ClaimOwner: "local-gateway"}).RoutesHandler()
	return &LocalComponent{server: &Server{Address: address, Handler: handler}, dispatcher: dispatch, runtimes: runtimes}, nil
}

// Start starts the local HTTP gateway.
func (component *LocalComponent) Start(ctx context.Context) error { return component.server.Start(ctx) }

// Close drains ingress, queued requests, and Runtime Bundles in dependency order.
func (component *LocalComponent) Close(ctx context.Context) error {
	return errors.Join(component.server.Close(ctx), component.dispatcher.Close(ctx), component.runtimes.Close(ctx))
}
