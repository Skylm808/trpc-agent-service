// Package storage constructs tenant-scoped tRPC-Agent-Go storage services.
package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"trpc.group/trpc-go/trpc-agent-go/artifact"
	artifactmemory "trpc.group/trpc-go/trpc-agent-go/artifact/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	memoryinmemory "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	memorypostgres "trpc.group/trpc-go/trpc-agent-go/memory/postgres"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	sessionpostgres "trpc.group/trpc-go/trpc-agent-go/session/postgres"
)

// Services groups storage instances owned by one Runtime Bundle.
type Services struct {
	Session  session.Service
	Memory   memory.Service
	Artifact artifact.Service
	once     sync.Once
	closeErr error
}

// NewPostgres constructs the Runner services backed by PostgreSQL. Platform
// coordination still uses Redis and the fenced SQL write store.
func NewPostgres(profile tenant.StorageProfile, dsn string, db *sql.DB) (*Services, error) {
	if err := ValidatePostgresProfile(profile); err != nil {
		return nil, err
	}
	if dsn == "" || db == nil {
		return nil, errors.New("storage: PostgreSQL DSN and database are required")
	}
	sessions, err := sessionpostgres.NewService(
		sessionpostgres.WithPostgresClientDSN(dsn),
		sessionpostgres.WithTablePrefix("runtime_"),
		sessionpostgres.WithSkipDBInit(true),
	)
	if err != nil {
		return nil, fmt.Errorf("storage: create PostgreSQL session service: %w", err)
	}
	memories, err := memorypostgres.NewService(
		memorypostgres.WithPostgresClientDSN(dsn),
		memorypostgres.WithTableName("runtime_memories"),
		memorypostgres.WithSkipDBInit(true),
	)
	if err != nil {
		_ = sessions.Close()
		return nil, fmt.Errorf("storage: create PostgreSQL memory service: %w", err)
	}
	return &Services{Session: sessions, Memory: memories, Artifact: &PostgresArtifactService{DB: db}}, nil
}

// ValidatePostgresProfile checks every declared data domain before startup.
func ValidatePostgresProfile(profile tenant.StorageProfile) error {
	for name, backend := range map[string]tenant.BackendConfig{
		"session": profile.Session, "memory": profile.Memory,
		"summary": profile.Summary, "artifact": profile.Artifact,
		"knowledge": profile.Knowledge, "audit": profile.Audit,
	} {
		if backend.Type != tenant.BackendPostgres {
			return fmt.Errorf("storage: %s backend must be postgres, got %q", name, backend.Type)
		}
	}
	return nil
}

// NewTestServices constructs isolated services for deterministic tests.
func NewTestServices(profile tenant.StorageProfile) (*Services, error) {
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
