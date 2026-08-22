package openclaw

import (
	"sync"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
)

// RunStatus is the queryable OpenClaw request state.
type RunStatus struct {
	RequestID string    `json:"request_id"`
	Type      string    `json:"type"`
	Reply     string    `json:"reply,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Registry projects events into request status without high-cardinality metrics.
type Registry struct {
	mu     sync.RWMutex
	status map[string]RunStatus
}

func NewRegistry() *Registry { return &Registry{status: make(map[string]RunStatus)} }
func (registry *Registry) Publish(event gateway.RunEvent) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	registry.status[event.RequestID] = RunStatus{RequestID: event.RequestID, Type: event.Type, Reply: event.Message, Error: event.Error, UpdatedAt: time.Now().UTC()}
	registry.mu.Unlock()
}
func (registry *Registry) Get(requestID string) (RunStatus, bool) {
	if registry == nil {
		return RunStatus{}, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	value, ok := registry.status[requestID]
	return value, ok
}

// MultiPublisher sends one event to independent status, stream, audit, or telemetry projections.
type MultiPublisher []gateway.EventPublisher

func (publishers MultiPublisher) Publish(event gateway.RunEvent) {
	for _, publisher := range publishers {
		if publisher != nil {
			publisher.Publish(event)
		}
	}
}

type Canceler interface{ Cancel(string) bool }
type Approver interface {
	Grant(string, string, string) bool
}
