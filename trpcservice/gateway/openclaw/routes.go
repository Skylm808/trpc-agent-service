package openclaw

import (
	"crypto/subtle"
	"errors"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// Route is a trusted binding resolved from server-owned credentials.
type Route struct {
	TenantID, AppID, BindingID string
	ChannelType                tenant.ChannelType
	ConfigVersion              tenant.ConfigVersion
	Credential                 string
}

// Routes resolves a credential to one tenant binding.
type Routes interface {
	Resolve(string, string) (Route, error)
}

// StaticRoutes is an immutable local registry suitable for tests and Compose.
type StaticRoutes struct{ routes map[string]Route }

// NewStaticRoutes validates and copies routes.
func NewStaticRoutes(routes ...Route) (*StaticRoutes, error) {
	registry := &StaticRoutes{routes: make(map[string]Route, len(routes))}
	for _, route := range routes {
		if route.TenantID == "" || route.AppID == "" || route.BindingID == "" || route.ChannelType == "" || route.ConfigVersion == 0 || route.Credential == "" {
			return nil, errors.New("openclaw: route scope, version, channel, and credential are required")
		}
		if _, exists := registry.routes[route.BindingID]; exists {
			return nil, errors.New("openclaw: duplicate binding")
		}
		registry.routes[route.BindingID] = route
	}
	return registry, nil
}

// Resolve performs a constant-time credential comparison.
func (routes *StaticRoutes) Resolve(bindingID, credential string) (Route, error) {
	route, ok := routes.routes[bindingID]
	if !ok || len(credential) != len(route.Credential) || subtle.ConstantTimeCompare([]byte(credential), []byte(route.Credential)) != 1 {
		return Route{}, errors.New("openclaw: invalid binding credential")
	}
	return route, nil
}

// Hub fans execution events out to an optional streaming HTTP client.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan StreamEvent]struct{}
}

func NewHub() *Hub { return &Hub{subs: make(map[string]map[chan StreamEvent]struct{})} }

func (hub *Hub) Subscribe(requestID string) (<-chan StreamEvent, func()) {
	stream := make(chan StreamEvent, 32)
	hub.mu.Lock()
	if hub.subs[requestID] == nil {
		hub.subs[requestID] = make(map[chan StreamEvent]struct{})
	}
	hub.subs[requestID][stream] = struct{}{}
	hub.mu.Unlock()
	var once sync.Once
	return stream, func() {
		once.Do(func() {
			hub.mu.Lock()
			delete(hub.subs[requestID], stream)
			if len(hub.subs[requestID]) == 0 {
				delete(hub.subs, requestID)
			}
			hub.mu.Unlock()
		})
	}
}

// Unsubscribe removes a stream after disconnect or terminal delivery.
func (hub *Hub) Unsubscribe(requestID string, target <-chan StreamEvent) {
	if hub == nil || target == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for stream := range hub.subs[requestID] {
		if (<-chan StreamEvent)(stream) == target {
			delete(hub.subs[requestID], stream)
		}
	}
	if len(hub.subs[requestID]) == 0 {
		delete(hub.subs, requestID)
	}
}

// Publish implements gateway.EventPublisher. Slow clients cannot block Workers.
func (hub *Hub) Publish(event gateway.RunEvent) {
	projected := StreamEvent{Type: event.Type, RequestID: event.RequestID, TraceID: event.TraceID, Delta: event.Delta, Message: event.Message, ToolName: event.ToolName, Error: event.Error, Terminal: event.Terminal}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for stream := range hub.subs[event.RequestID] {
		select {
		case stream <- projected:
		default:
			if event.Terminal {
				select {
				case <-stream:
				default:
				}
				select {
				case stream <- projected:
				default:
				}
			}
		}
		if event.Terminal {
			delete(hub.subs[event.RequestID], stream)
		}
	}
	if event.Terminal && len(hub.subs[event.RequestID]) == 0 {
		delete(hub.subs, event.RequestID)
	}
}
