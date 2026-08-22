// Package storage constructs tenant-scoped tRPC-Agent-Go storage services.
package storage

import (
	"errors"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"trpc.group/trpc-go/trpc-agent-go/artifact"
	artifactmemory "trpc.group/trpc-go/trpc-agent-go/artifact/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	memoryinmemory "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// Services groups storage instances owned by one Runtime Bundle.
type Services struct {
	Session  session.Service
	Memory   memory.Service
	Artifact artifact.Service
	once     sync.Once
	closeErr error
}

// NewInMemory constructs isolated development services.
func NewInMemory(profile tenant.StorageProfile) (*Services, error) {
	for name, backend := range map[string]tenant.BackendConfig{"session": profile.Session, "memory": profile.Memory, "summary": profile.Summary, "artifact": profile.Artifact, "knowledge": profile.Knowledge, "audit": profile.Audit} {
		if backend.Type != tenant.BackendInMemory {
			return nil, errors.New("storage: " + name + " backend is not available in the offline runtime")
		}
	}
	return &Services{Session: sessioninmemory.NewSessionService(), Memory: memoryinmemory.NewMemoryService(), Artifact: artifactmemory.NewService()}, nil
}

// Close releases services injected into Runner; Runner treats them as borrowed.
func (services *Services) Close() error {
	if services == nil {
		return nil
	}
	services.once.Do(func() {
		var memoryErr, sessionErr error
		if services.Memory != nil {
			memoryErr = services.Memory.Close()
		}
		if services.Session != nil {
			sessionErr = services.Session.Close()
		}
		services.closeErr = errors.Join(memoryErr, sessionErr)
	})
	return services.closeErr
}
