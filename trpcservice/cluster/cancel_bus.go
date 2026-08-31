package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// PubSubBackend is the shared transient notification surface. Durable intent
// remains in PostgreSQL and is checked by Workers independently of Pub/Sub.
type PubSubBackend interface {
	Publish(context.Context, string, []byte) error
	Subscribe(context.Context, string) (<-chan []byte, func() error, error)
}

// DurableCanceler atomically records cancellation for an active request.
type DurableCanceler interface {
	Cancel(string, string) bool
}

// LocalCancel stops a request only if this node currently owns its Runner.
type LocalCancel func(string, string) bool

// CancelBus persists cancellation before broadcasting it to every Worker.
type CancelBus struct {
	backend PubSubBackend
	store   DurableCanceler
	local   LocalCancel
	channel string
	close   func() error
	done    chan struct{}
	once    sync.Once
}

type cancelCommand struct {
	TenantID  string `json:"tenant_id"`
	RequestID string `json:"request_id"`
}

// NewCancelBus subscribes this node before accepting cancellation requests.
func NewCancelBus(ctx context.Context, backend PubSubBackend, store DurableCanceler, local LocalCancel, prefix string) (*CancelBus, error) {
	if ctx == nil || backend == nil || store == nil || local == nil {
		return nil, errors.New("cluster: cancel backend, store, callback, and context are required")
	}
	if prefix == "" {
		prefix = "trpc-agent-service"
	}
	bus := &CancelBus{backend: backend, store: store, local: local, channel: prefix + ":run-control:cancel", done: make(chan struct{})}
	input, closeSubscription, err := backend.Subscribe(ctx, bus.channel)
	if err != nil {
		return nil, err
	}
	bus.close = closeSubscription
	go bus.consume(input)
	return bus, nil
}

// Cancel records durable intent and then sends a low-latency notification.
func (bus *CancelBus) Cancel(tenantID, requestID string) bool {
	if bus == nil || tenantID == "" || requestID == "" || !bus.store.Cancel(tenantID, requestID) {
		return false
	}
	payload, err := json.Marshal(cancelCommand{TenantID: tenantID, RequestID: requestID})
	if err != nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = bus.backend.Publish(ctx, bus.channel, payload)
	cancel()
	return true
}

func (bus *CancelBus) consume(input <-chan []byte) {
	defer close(bus.done)
	for payload := range input {
		var command cancelCommand
		if json.Unmarshal(payload, &command) == nil && command.TenantID != "" && command.RequestID != "" {
			bus.local(command.TenantID, command.RequestID)
		}
	}
}

// Close stops this node's subscription.
func (bus *CancelBus) Close() error {
	if bus == nil {
		return nil
	}
	var err error
	bus.once.Do(func() {
		if bus.close != nil {
			err = bus.close()
		}
	})
	return err
}
