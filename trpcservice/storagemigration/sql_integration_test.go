package storagemigration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/liuzengh/trpc-agent-service/trpcservice/repository"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storage"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

func TestPostgresMigrationWorkerResumesAndCopiesTenantMemory(t *testing.T) {
	baseDSN := os.Getenv("TRPC_AGENT_POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("set TRPC_AGENT_POSTGRES_TEST_DSN to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	control, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := repository.Migrate(ctx, func(ctx context.Context, script string) error { _, err := control.ExecContext(ctx, script); return err }, repository.DirectionUp); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	sourceSchema, targetSchema := "migration_source_"+suffix, "migration_target_"+suffix
	if _, err := control.ExecContext(ctx, "CREATE SCHEMA "+sourceSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := control.ExecContext(ctx, "CREATE SCHEMA "+targetSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = control.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+sourceSchema+" CASCADE")
		_, _ = control.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+targetSchema+" CASCADE")
	})
	sourceDSN := withSearchPath(t, baseDSN, sourceSchema)
	targetDSN := withSearchPath(t, baseDSN, targetSchema)
	sourceDB, err := sql.Open("pgx", sourceDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceDB.Close()
	targetDB, err := sql.Open("pgx", targetDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer targetDB.Close()
	const memoryDDL = `CREATE TABLE runtime_memories (memory_id TEXT PRIMARY KEY,app_name TEXT NOT NULL,user_id TEXT NOT NULL,memory_data JSONB NOT NULL,created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,deleted_at TIMESTAMP);`
	if _, err := sourceDB.ExecContext(ctx, memoryDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := targetDB.ExecContext(ctx, memoryDDL+`CREATE TABLE storage_migration_items (source_route_hash TEXT NOT NULL,table_name TEXT NOT NULL,source_key TEXT NOT NULL,checksum TEXT NOT NULL,copied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(source_route_hash,table_name,source_key));`); err != nil {
		t.Fatal(err)
	}
	tenantID, appID := "migration-"+suffix, "assistant"
	appName, _ := tenant.CanonicalAppName(tenantID, appID)
	for index := 0; index < 5; index++ {
		if _, err := sourceDB.ExecContext(ctx, `INSERT INTO runtime_memories(memory_id,app_name,user_id,memory_data) VALUES($1,$2,'user','{}')`, fmt.Sprintf("memory-%02d", index), appName); err != nil {
			t.Fatal(err)
		}
	}
	configStore, err := repository.NewSQLStore(control)
	if err != nil {
		t.Fatal(err)
	}
	_, err = configStore.PublishConfig(ctx, repository.ConfigRecord{TenantID: tenantID, TenantName: tenantID, TenantEnabled: false, Payload: []byte("test"), SHA256: "test", CreatedBy: "test", Apps: []tenant.AgentApp{{ID: appID, Name: "Assistant", Enabled: true}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR12_SOURCE_DSN", sourceDSN)
	t.Setenv("PR12_TARGET_DSN", targetDSN)
	router, err := storage.NewRouter(baseDSN, control, func(ref tenant.SecretRef) (string, error) { return os.Getenv(ref.Key), nil })
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	source := tenant.BackendConfig{Type: tenant.BackendPostgres, Credential: tenant.SecretRef{Provider: tenant.SecretProviderEnv, Key: "PR12_SOURCE_DSN"}}
	target := tenant.BackendConfig{Type: tenant.BackendPostgres, Credential: tenant.SecretRef{Provider: tenant.SecretProviderEnv, Key: "PR12_TARGET_DSN"}}
	job, err := NewJob(tenantID, appID, 1, DomainMemory, source, target, "integration")
	if err != nil {
		t.Fatal(err)
	}
	store := &SQLStore{DB: control}
	job, err = store.Create(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	copier := &PostgresCopier{Router: router}
	for attempts := 0; attempts < 20; attempts++ {
		claim, ok, err := store.Claim(ctx, "worker-a", 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("migration was not claimable")
		}
		progress, err := copier.Step(ctx, claim, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(ctx, claim, progress); err != nil {
			t.Fatal(err)
		}
		job, err = store.Get(ctx, tenantID, job.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == StatusCompleted {
			break
		}
	}
	if job.Status != StatusCompleted || job.SourceRows != 5 || job.CopiedRows != 5 {
		t.Fatalf("job=%+v", job)
	}
	var count int
	if err := targetDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_memories WHERE app_name=$1`, appName).Scan(&count); err != nil || count != 5 {
		t.Fatalf("target count=%d err=%v", count, err)
	}
	jobs, err := store.List(ctx, tenantID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%d err=%v", len(jobs), err)
	}
}

func withSearchPath(t *testing.T, raw, schema string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
