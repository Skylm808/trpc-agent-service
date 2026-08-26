package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
	"github.com/liuzengh/trpc-agent-service/trpcservice/admin"
	"github.com/liuzengh/trpc-agent-service/trpcservice/audit"
	"github.com/liuzengh/trpc-agent-service/trpcservice/backend"
	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway/openclaw"
	"github.com/liuzengh/trpc-agent-service/trpcservice/idempotency"
	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	"github.com/liuzengh/trpc-agent-service/trpcservice/repository"
	"github.com/liuzengh/trpc-agent-service/trpcservice/secret"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessioncoord"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storage"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/liuzengh/trpc-agent-service/trpcservice/worker"
	"gopkg.in/yaml.v3"
)

const (
	postgresDSNEnv = "TRPC_AGENT_POSTGRES_DSN"
	redisURLEnv    = "TRPC_AGENT_REDIS_URL"
)

type durableComponent struct {
	inner trpcservice.Component
	db    *sql.DB
	redis *backend.Redis
}

func (component *durableComponent) Start(ctx context.Context) error {
	return component.inner.Start(ctx)
}

func (component *durableComponent) Close(ctx context.Context) error {
	return errors.Join(component.inner.Close(ctx), component.redis.Close(), component.db.Close())
}

func newDurableComponent(ctx context.Context, address string, file *config.File, routes openclaw.Routes, decorators ...openclaw.HandlerDecorator) (trpcservice.Component, error) {
	if err := validatePersistentProfiles(file); err != nil {
		return nil, err
	}
	postgresDSN := os.Getenv(postgresDSNEnv)
	redisURL := os.Getenv(redisURLEnv)
	connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	db, err := backend.OpenPostgres(connectCtx, postgresDSN)
	if err != nil {
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	closeDB := true
	defer func() {
		if closeDB {
			_ = db.Close()
		}
	}()
	if err := bootstrapConfig(connectCtx, db, file); err != nil {
		return nil, err
	}
	redisBackend, err := backend.OpenRedis(connectCtx, redisURL)
	if err != nil {
		return nil, fmt.Errorf("connect Redis: %w", err)
	}
	closeRedis := true
	defer func() {
		if closeRedis {
			_ = redisBackend.Close()
		}
	}()

	writes := &sessioncoord.SQLWriteStore{DB: db}
	coordinator := &sessioncoord.RedisCoordinator{Redis: redisBackend, Fencer: writes}
	factory := worker.RuntimeFactoryWithServices(writes, func(snapshot config.RuntimeSnapshot) (*storage.Services, error) {
		return storage.NewPostgres(snapshot.App().Storage, postgresDSN, db)
	})
	redactor := servicelog.NewRedactor(nil, nil)
	bus := &openclaw.RedisEventBus{Backend: redisBackend}
	component, err := openclaw.NewComponent(ctx, address, file, routes, openclaw.ComponentDependencies{
		Inbox:          &idempotency.SQLStore{DB: db},
		Coordinator:    coordinator,
		Writes:         writes,
		RuntimeFactory: factory,
		Audit:          &audit.SQLStore{DB: db, Redactor: redactor},
		EventBus:       bus,
		WorkerID:       nodeID(),
	}, decorators...)
	if err != nil {
		return nil, err
	}
	closeDB = false
	closeRedis = false
	return &durableComponent{inner: component, db: db, redis: redisBackend}, nil
}

func nodeID() string {
	if configured := os.Getenv("TRPC_AGENT_NODE_ID"); configured != "" {
		return configured
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return hostname
	}
	return "worker"
}

func migrateSchema(ctx context.Context) error {
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := backend.OpenPostgres(connectCtx, os.Getenv(postgresDSNEnv))
	if err != nil {
		return err
	}
	defer db.Close()
	return applyMigrations(connectCtx, db)
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('trpc-agent-service:migrations', 0))"); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if err := repository.Migrate(ctx, func(ctx context.Context, script string) error {
		_, err := tx.ExecContext(ctx, script)
		return err
	}, repository.DirectionUp); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func validatePersistentProfiles(file *config.File) error {
	if file == nil {
		return errors.New("persistent config is required")
	}
	for _, currentTenant := range file.Tenants {
		if !currentTenant.Enabled {
			continue
		}
		for _, app := range currentTenant.Apps {
			if app.Enabled {
				if app.Model.Provider == "mock" {
					return fmt.Errorf("tenant %q app %q: mock model is test-only", currentTenant.ID, app.ID)
				}
				if _, err := secret.ResolveLocal(app.Model.APIKey); err != nil {
					return fmt.Errorf("tenant %q app %q: model credential is unavailable", currentTenant.ID, app.ID)
				}
				if err := storage.ValidatePostgresProfile(app.Storage); err != nil {
					return fmt.Errorf("tenant %q app %q: %w", currentTenant.ID, app.ID, err)
				}
			}
		}
	}
	return nil
}

func bootstrapConfig(ctx context.Context, db *sql.DB, file *config.File) error {
	store, err := repository.NewSQLStore(db)
	if err != nil {
		return err
	}
	service, err := admin.NewService(store)
	if err != nil {
		return err
	}
	for _, configured := range file.Tenants {
		payload, err := yaml.Marshal(&config.File{SchemaVersion: file.SchemaVersion, Tenants: []tenant.Tenant{configured}})
		if err != nil {
			return fmt.Errorf("encode tenant %q bootstrap: %w", configured.ID, err)
		}
		versions, err := service.Versions(ctx, configured.ID)
		if err != nil {
			return fmt.Errorf("read tenant %q versions: %w", configured.ID, err)
		}
		if len(versions) == 0 {
			if configured.ConfigVersion != 1 {
				return fmt.Errorf("tenant %q fresh bootstrap requires config_version 1", configured.ID)
			}
			if _, err := service.Publish(ctx, configured.ID, 0, payload); err != nil {
				return fmt.Errorf("publish tenant %q bootstrap: %w", configured.ID, err)
			}
			continue
		}
		current := versions[0]
		if current.Version != configured.ConfigVersion {
			return fmt.Errorf("tenant %q database config version is %d, file has %d", configured.ID, current.Version, configured.ConfigVersion)
		}
		digest := sha256.Sum256(payload)
		if current.SHA256 != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("tenant %q config content differs from published version %d", configured.ID, configured.ConfigVersion)
		}
	}
	return nil
}
