package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/backend"
	"github.com/liuzengh/trpc-agent-service/trpcservice/knowledgebase"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"trpc.group/trpc-go/trpc-agent-go/artifact"
)

// SecretResolver resolves a SecretRef without exposing its value to callers.
type SecretResolver func(tenant.SecretRef) (string, error)

// PostgresTarget is a resolved route. DSN is intentionally never formatted.
type PostgresTarget struct {
	DSN string
	DB  *sql.DB
}

// ArtifactForRoute constructs a migration-owned artifact service.
func (router *Router) ArtifactForRoute(ctx context.Context, route tenant.BackendConfig) (artifact.Service, error) {
	return router.artifactService(ctx, route)
}

// MigrationLedgerDB returns the platform database used for idempotency records.
func (router *Router) MigrationLedgerDB() *sql.DB {
	if router == nil {
		return nil
	}
	return router.defaultTarget.DB
}

// Router resolves each storage domain independently and caches external pools.
// A zero credential selects the platform PostgreSQL pool; a credential resolves
// to a DSN for a separately migrated PostgreSQL cluster.
type Router struct {
	defaultTarget PostgresTarget
	resolve       SecretResolver
	mu            sync.Mutex
	targets       map[[32]byte]PostgresTarget
	closed        bool
}

// NewRouter creates a production router with one mandatory default target.
func NewRouter(defaultDSN string, defaultDB *sql.DB, resolver SecretResolver) (*Router, error) {
	if defaultDSN == "" || defaultDB == nil || resolver == nil {
		return nil, errors.New("storage: default PostgreSQL target and secret resolver are required")
	}
	return &Router{defaultTarget: PostgresTarget{DSN: defaultDSN, DB: defaultDB}, resolve: resolver, targets: make(map[[32]byte]PostgresTarget)}, nil
}

// Services builds the immutable Runtime Bundle services for one route profile.
func (router *Router) Services(ctx context.Context, profile tenant.StorageProfile) (*Services, error) {
	return router.services(ctx, "", "", profile, tenant.KnowledgePolicy{})
}

// ServicesForApp builds all routed services using trusted tenant/app scope.
func (router *Router) ServicesForApp(ctx context.Context, tenantID string, app tenant.AgentApp) (*Services, error) {
	if tenantID == "" || app.ID == "" {
		return nil, errors.New("storage: tenant and app scope are required")
	}
	return router.services(ctx, tenantID, app.ID, app.Storage, app.Knowledge)
}

// KnowledgeForApp resolves only the scoped Knowledge service for Admin ingest
// and search operations.
func (router *Router) KnowledgeForApp(ctx context.Context, tenantID string, app tenant.AgentApp) (*knowledgebase.Service, error) {
	if tenantID == "" || app.ID == "" || !app.Knowledge.Enabled {
		return nil, errors.New("storage: enabled tenant/app knowledge configuration is required")
	}
	return router.knowledgeService(ctx, tenantID, app.ID, app.Storage.Knowledge, app.Knowledge)
}

func (router *Router) services(ctx context.Context, tenantID, appID string, profile tenant.StorageProfile, knowledgePolicy tenant.KnowledgePolicy) (*Services, error) {
	if router == nil || ctx == nil {
		return nil, errors.New("storage: router and context are required")
	}
	if err := ValidateRoutedProfile(profile); err != nil {
		return nil, err
	}
	if !sameRoute(profile.Session, profile.Summary) {
		return nil, errors.New("storage: session and summary must use the same PostgreSQL route")
	}
	sessionTarget, err := router.Resolve(ctx, profile.Session)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve session backend: %w", err)
	}
	memoryTarget, err := router.Resolve(ctx, profile.Memory)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve memory backend: %w", err)
	}
	artifactService, err := router.artifactService(ctx, profile.Artifact)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve artifact backend: %w", err)
	}
	services, err := newPostgresServices(sessionTarget.DSN, memoryTarget.DSN, router.defaultTarget.DB)
	if err != nil {
		if closer, ok := artifactService.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	services.Artifact = artifactService
	services.Knowledge, err = router.knowledgeService(ctx, tenantID, appID, profile.Knowledge, knowledgePolicy)
	if err != nil {
		_ = services.Close()
		return nil, err
	}
	if profile.Session.MigrationTarget != nil {
		target, resolveErr := router.Resolve(ctx, *profile.Session.MigrationTarget)
		if resolveErr != nil {
			_ = services.Close()
			return nil, fmt.Errorf("storage: resolve session migration target: %w", resolveErr)
		}
		shadow, createErr := newPostgresSession(target.DSN)
		if createErr != nil {
			_ = services.Close()
			return nil, createErr
		}
		services.Session = &MirroredSession{Primary: services.Session, Target: shadow}
	}
	if profile.Memory.MigrationTarget != nil {
		target, resolveErr := router.Resolve(ctx, *profile.Memory.MigrationTarget)
		if resolveErr != nil {
			_ = services.Close()
			return nil, fmt.Errorf("storage: resolve memory migration target: %w", resolveErr)
		}
		shadow, createErr := newPostgresMemory(target.DSN)
		if createErr != nil {
			_ = services.Close()
			return nil, createErr
		}
		services.Memory = &MirroredMemory{Primary: services.Memory, Target: shadow}
	}
	if profile.Artifact.MigrationTarget != nil {
		target, resolveErr := router.artifactService(ctx, *profile.Artifact.MigrationTarget)
		if resolveErr != nil {
			_ = services.Close()
			return nil, fmt.Errorf("storage: resolve artifact migration target: %w", resolveErr)
		}
		services.Artifact = &MirroredArtifact{Primary: services.Artifact, Target: target}
	}
	return services, nil
}

// Resolve returns the concrete PostgreSQL target for a backend route.
func (router *Router) Resolve(ctx context.Context, route tenant.BackendConfig) (PostgresTarget, error) {
	if router == nil || ctx == nil || route.Type != tenant.BackendPostgres {
		return PostgresTarget{}, errors.New("storage: only routed PostgreSQL backends are currently available")
	}
	if route.Credential.IsZero() {
		return router.defaultTarget, nil
	}
	dsn, err := router.resolve(route.Credential)
	if err != nil {
		return PostgresTarget{}, errors.New("storage: resolve PostgreSQL credential failed")
	}
	// Include only a one-way digest of the resolved DSN in the cache identity.
	// Rotating a mounted/file secret creates a new pool without logging or
	// retaining the value as a map key visible to diagnostics.
	identity := sha256.Sum256([]byte(string(route.Credential.Provider) + "\x00" + route.Credential.Key + "\x00" + dsn))
	router.mu.Lock()
	if router.closed {
		router.mu.Unlock()
		return PostgresTarget{}, errors.New("storage: router is closed")
	}
	if target, ok := router.targets[identity]; ok {
		router.mu.Unlock()
		return target, nil
	}
	router.mu.Unlock()
	db, err := backend.OpenPostgres(ctx, dsn)
	if err != nil {
		return PostgresTarget{}, errors.New("storage: connect routed PostgreSQL backend failed")
	}
	target := PostgresTarget{DSN: dsn, DB: db}
	router.mu.Lock()
	if router.closed {
		router.mu.Unlock()
		_ = db.Close()
		return PostgresTarget{}, errors.New("storage: router is closed")
	}
	if existing, ok := router.targets[identity]; ok {
		router.mu.Unlock()
		_ = db.Close()
		return existing, nil
	}
	router.targets[identity] = target
	router.mu.Unlock()
	return target, nil
}

// Close releases only pools opened for external routes; the caller owns defaultDB.
func (router *Router) Close() error {
	if router == nil {
		return nil
	}
	router.mu.Lock()
	if router.closed {
		router.mu.Unlock()
		return nil
	}
	router.closed = true
	targets := router.targets
	router.targets = nil
	router.mu.Unlock()
	var result error
	for _, target := range targets {
		result = errors.Join(result, target.DB.Close())
	}
	return result
}

// ValidateRoutedProfile is the production route gate used by Admin publish.
func ValidateRoutedProfile(profile tenant.StorageProfile) error {
	for name, route := range map[string]tenant.BackendConfig{"session": profile.Session, "memory": profile.Memory, "summary": profile.Summary, "audit": profile.Audit} {
		if route.Type != tenant.BackendPostgres {
			return fmt.Errorf("storage: %s backend must be postgres, got %q", name, route.Type)
		}
		if route.MigrationTarget != nil && route.MigrationTarget.Type != tenant.BackendPostgres {
			return fmt.Errorf("storage: %s migration target must be postgres, got %q", name, route.MigrationTarget.Type)
		}
		if route.MigrationTarget != nil && route.MigrationTarget.Credential.IsZero() {
			return fmt.Errorf("storage: %s migration target requires a credential SecretRef", name)
		}
	}
	if profile.Artifact.Type != tenant.BackendPostgres && profile.Artifact.Type != tenant.BackendS3 {
		return fmt.Errorf("storage: artifact backend must be postgres or s3, got %q", profile.Artifact.Type)
	}
	if profile.Artifact.MigrationTarget != nil && profile.Artifact.MigrationTarget.Type != tenant.BackendPostgres && profile.Artifact.MigrationTarget.Type != tenant.BackendS3 {
		return fmt.Errorf("storage: artifact migration target must be postgres or s3, got %q", profile.Artifact.MigrationTarget.Type)
	}
	if profile.Artifact.MigrationTarget != nil && profile.Artifact.Type != tenant.BackendPostgres {
		return errors.New("storage: artifact backfill currently requires a postgres source")
	}
	if profile.Artifact.MigrationTarget != nil && profile.Artifact.MigrationTarget.Type == tenant.BackendPostgres && profile.Artifact.MigrationTarget.Credential.IsZero() {
		return errors.New("storage: external PostgreSQL artifact migration target requires a credential SecretRef")
	}
	if profile.Knowledge.Type != tenant.BackendPostgres && profile.Knowledge.Type != tenant.BackendQdrant {
		return fmt.Errorf("storage: knowledge backend must be postgres or qdrant, got %q", profile.Knowledge.Type)
	}
	if profile.Knowledge.MigrationTarget != nil {
		return errors.New("storage: knowledge migration_target requires a rebuild job and cannot be activated directly")
	}
	if !sameRoute(profile.Session, profile.Summary) {
		return errors.New("storage: session and summary routes must match")
	}
	for name, route := range map[string]tenant.BackendConfig{"audit": profile.Audit} {
		if !route.Credential.IsZero() || route.MigrationTarget != nil {
			return fmt.Errorf("storage: %s external routing is not available before its dedicated delivery PR", name)
		}
	}
	return nil
}

// Preflight resolves every active runtime route and verifies the target schema
// before a configuration may become runnable.
func (router *Router) Preflight(ctx context.Context, profile tenant.StorageProfile) error {
	if err := ValidateRoutedProfile(profile); err != nil {
		return err
	}
	routes := []struct {
		name   string
		route  tenant.BackendConfig
		tables []string
	}{
		{"session", profile.Session, []string{"runtime_session_states", "runtime_session_events", "runtime_session_track_events", "runtime_session_summaries", "runtime_app_states", "runtime_user_states"}},
		{"memory", profile.Memory, []string{"runtime_memories"}},
	}
	if profile.Artifact.Type == tenant.BackendPostgres {
		artifactRoute := profile.Artifact.Clone()
		if artifactRoute.MigrationTarget != nil && artifactRoute.MigrationTarget.Type == tenant.BackendS3 {
			artifactRoute.MigrationTarget = nil
		}
		routes = append(routes, struct {
			name   string
			route  tenant.BackendConfig
			tables []string
		}{"artifact", artifactRoute, []string{"runtime_artifacts"}})
	}
	for _, entry := range routes {
		target, err := router.Resolve(ctx, entry.route)
		if err != nil {
			return fmt.Errorf("storage: %s route preflight failed", entry.name)
		}
		if err := requireTables(ctx, target.DB, entry.tables); err != nil {
			return fmt.Errorf("storage: %s route schema is unavailable", entry.name)
		}
		if entry.route.MigrationTarget != nil {
			shadow, err := router.Resolve(ctx, *entry.route.MigrationTarget)
			if err != nil {
				return fmt.Errorf("storage: %s migration target preflight failed", entry.name)
			}
			tables := append(append([]string(nil), entry.tables...), "storage_migration_items")
			if err := requireTables(ctx, shadow.DB, tables); err != nil {
				return fmt.Errorf("storage: %s migration target schema is unavailable", entry.name)
			}
		}
	}
	return nil
}

// PreflightApp verifies external Artifact/Knowledge clients in addition to the
// shared SQL schemas before a published app can receive new work.
func (router *Router) PreflightApp(ctx context.Context, tenantID string, app tenant.AgentApp) error {
	if err := router.Preflight(ctx, app.Storage); err != nil {
		return err
	}
	artifactService, err := router.artifactService(ctx, app.Storage.Artifact)
	if err != nil {
		return errors.New("storage: artifact route preflight failed")
	}
	if app.Storage.Artifact.Type == tenant.BackendS3 {
		appName, _ := tenant.CanonicalAppName(tenantID, app.ID)
		if _, err := artifactService.ListArtifactKeys(ctx, artifact.SessionInfo{AppName: appName, UserID: "preflight", SessionID: "preflight"}); err != nil {
			if closer, ok := artifactService.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			return errors.New("storage: S3 artifact route is unreachable")
		}
	}
	if closer, ok := artifactService.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	if app.Storage.Artifact.MigrationTarget != nil && app.Storage.Artifact.MigrationTarget.Type == tenant.BackendS3 {
		target, err := router.artifactService(ctx, *app.Storage.Artifact.MigrationTarget)
		if err != nil {
			return errors.New("storage: artifact migration target preflight failed")
		}
		appName, _ := tenant.CanonicalAppName(tenantID, app.ID)
		if _, err := target.ListArtifactKeys(ctx, artifact.SessionInfo{AppName: appName, UserID: "preflight", SessionID: "preflight"}); err != nil {
			if closer, ok := target.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			return errors.New("storage: S3 artifact migration target is unreachable")
		}
		if closer, ok := target.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	knowledgeService, err := router.knowledgeService(ctx, tenantID, app.ID, app.Storage.Knowledge, app.Knowledge)
	if err != nil {
		return errors.New("storage: knowledge route preflight failed")
	}
	if knowledgeService != nil {
		_ = knowledgeService.Close()
	}
	return nil
}

func requireTables(ctx context.Context, db *sql.DB, tables []string) error {
	for _, table := range tables {
		var name sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)`, table).Scan(&name); err != nil || !name.Valid {
			return errors.New("storage: required table is missing")
		}
	}
	return nil
}

func sameRoute(left, right tenant.BackendConfig) bool {
	if left.Type != right.Type || left.Endpoint != right.Endpoint || left.Credential != right.Credential {
		return false
	}
	if left.MigrationTarget == nil || right.MigrationTarget == nil {
		return left.MigrationTarget == nil && right.MigrationTarget == nil
	}
	return sameRoute(*left.MigrationTarget, *right.MigrationTarget)
}
