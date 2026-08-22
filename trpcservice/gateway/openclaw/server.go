package openclaw

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
)

// Server adapts the gateway handler to the root App component lifecycle.
type Server struct {
	Address  string
	Handler  http.Handler
	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
}

// Start binds before returning, so lifecycle startup failures are observable.
func (component *Server) Start(_ context.Context) error {
	if component == nil || component.Handler == nil || component.Address == "" {
		return errors.New("openclaw: address and handler are required")
	}
	listener, err := net.Listen("tcp", component.Address)
	if err != nil {
		return err
	}
	component.mu.Lock()
	component.listener = listener
	component.server = &http.Server{Handler: component.Handler}
	server := component.server
	component.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
	return nil
}

// Close gracefully drains HTTP callbacks and streams.
func (component *Server) Close(ctx context.Context) error {
	component.mu.Lock()
	server := component.server
	component.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}
