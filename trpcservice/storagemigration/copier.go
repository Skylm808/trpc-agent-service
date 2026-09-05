package storagemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liuzengh/trpc-agent-service/trpcservice/knowledgebase"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storage"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"trpc.group/trpc-go/trpc-agent-go/artifact"
	"trpc.group/trpc-go/trpc-agent-go/memory"
)

// Copier advances one bounded backfill batch.
type Copier interface {
	Step(context.Context, Job, int) (Progress, error)
}

// PostgresCopier copies whitelisted runtime tables between routed clusters.
type KnowledgeResolver func(context.Context, Job, tenant.BackendConfig) (*knowledgebase.Service, error)

type PostgresCopier struct {
	Router           *storage.Router
	ResolveKnowledge KnowledgeResolver
}

type checkpoint struct {
	Table  int    `json:"table"`
	Cursor string `json:"cursor,omitempty"`
}

type tableSpec struct {
	name, key, filter string
	columns           []string
}

func (copier *PostgresCopier) Step(ctx context.Context, job Job, batchSize int) (Progress, error) {
	if copier == nil || copier.Router == nil || ctx == nil || batchSize <= 0 {
		return Progress{}, errors.New("storage migration: copier, context, and batch size are required")
	}
	appName, err := tenant.CanonicalAppName(job.TenantID, job.AppID)
	if err != nil {
		return Progress{}, err
	}
	if job.Domain == DomainArtifact && job.Source.Type == tenant.BackendPostgres && job.Target.Type == tenant.BackendS3 {
		return copier.stepArtifactToS3(ctx, job, appName, batchSize)
	}
	if job.Domain == DomainArtifact && job.Source.Type == tenant.BackendS3 && job.Target.Type == tenant.BackendPostgres {
		return copier.stepArtifactFromS3(ctx, job, appName, batchSize)
	}
	if job.Domain == DomainKnowledge {
		return copier.stepKnowledge(ctx, job, batchSize)
	}
	if job.Domain == DomainMemory && job.Source.Type == tenant.BackendPostgres && job.Target.Type == tenant.BackendExternal {
		return copier.stepMemoryToExternal(ctx, job, appName, batchSize)
	}
	source, err := copier.Router.Resolve(ctx, job.Source)
	if err != nil {
		return Progress{}, errors.New("storage migration: source backend unavailable")
	}
	target, err := copier.Router.Resolve(ctx, job.Target)
	if err != nil {
		return Progress{}, errors.New("storage migration: target backend unavailable")
	}
	specs, args, err := migrationTables(job.Domain, job.TenantID, job.AppID, appName)
	if err != nil {
		return Progress{}, err
	}
	var mark checkpoint
	if len(job.Checkpoint) > 0 {
		if err := json.Unmarshal(job.Checkpoint, &mark); err != nil {
			return Progress{}, errors.New("storage migration: invalid checkpoint")
		}
	}
	progress := Progress{SourceRows: job.SourceRows, CopiedRows: job.CopiedRows}
	if progress.SourceRows == 0 {
		for index, spec := range specs {
			var count int64
			if err := source.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+spec.name+` WHERE `+spec.filter, args[index]...).Scan(&count); err != nil {
				return Progress{}, err
			}
			progress.SourceRows += count
		}
	}
	for mark.Table < len(specs) {
		spec := specs[mark.Table]
		rows, err := selectBatch(ctx, source.DB, spec, args[mark.Table], mark.Cursor, batchSize)
		if err != nil {
			return Progress{}, err
		}
		if len(rows) == 0 {
			mark.Table++
			mark.Cursor = ""
			continue
		}
		for _, row := range rows {
			copied, err := insertRow(ctx, target.DB, job.SourceRouteHash, spec, row)
			if err != nil {
				return Progress{}, err
			}
			if copied {
				progress.CopiedRows++
			}
			mark.Cursor = row.key
		}
		progress.Checkpoint, _ = json.Marshal(mark)
		if progress.CopiedRows > progress.SourceRows {
			progress.SourceRows = progress.CopiedRows
		}
		return progress, nil
	}
	progress.Done = true
	progress.Checkpoint, _ = json.Marshal(mark)
	if progress.CopiedRows < progress.SourceRows {
		return Progress{}, errors.New("storage migration: copied row count is below source snapshot")
	}
	return progress, nil
}

func (copier *PostgresCopier) stepMemoryToExternal(ctx context.Context, job Job, appName string, batchSize int) (Progress, error) {
	source, err := copier.Router.Resolve(ctx, job.Source)
	if err != nil {
		return Progress{}, errors.New("storage migration: source memory backend unavailable")
	}
	target, err := copier.Router.MemoryForRoute(ctx, job.TenantID, job.AppID, job.Target)
	if err != nil {
		return Progress{}, errors.New("storage migration: target memory backend unavailable")
	}
	defer target.Close()
	ledger := copier.Router.MigrationLedgerDB()
	var mark knowledgeCheckpoint
	if len(job.Checkpoint) > 0 && json.Unmarshal(job.Checkpoint, &mark) != nil {
		return Progress{}, errors.New("storage migration: invalid memory checkpoint")
	}
	progress := Progress{SourceRows: job.SourceRows, CopiedRows: job.CopiedRows}
	if progress.SourceRows == 0 {
		if err := source.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_memories WHERE app_name=$1 AND deleted_at IS NULL`, appName).Scan(&progress.SourceRows); err != nil {
			return Progress{}, err
		}
	}
	rows, err := source.DB.QueryContext(ctx, `SELECT memory_id,memory_data FROM runtime_memories WHERE app_name=$1 AND deleted_at IS NULL AND memory_id>$2 ORDER BY memory_id LIMIT $3`, appName, mark.Cursor, batchSize)
	if err != nil {
		return Progress{}, err
	}
	var batch []struct {
		id   string
		data []byte
	}
	for rows.Next() {
		var row struct {
			id   string
			data []byte
		}
		if err := rows.Scan(&row.id, &row.data); err != nil {
			rows.Close()
			return Progress{}, err
		}
		batch = append(batch, row)
	}
	if err := rows.Close(); err != nil {
		return Progress{}, err
	}
	if len(batch) == 0 {
		progress.Done = true
		progress.Checkpoint, _ = json.Marshal(mark)
		return progress, nil
	}
	for _, row := range batch {
		digest := sha256.Sum256(row.data)
		checksum := hex.EncodeToString(digest[:])
		var stored string
		err := ledger.QueryRowContext(ctx, `SELECT checksum FROM storage_migration_items WHERE source_route_hash=$1 AND table_name='runtime_memories' AND source_key=$2`, job.SourceRouteHash, row.id).Scan(&stored)
		if err == nil {
			if stored != checksum {
				return Progress{}, errors.New("storage migration: source memory changed after checkpoint")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Progress{}, err
		} else {
			var entry memory.Entry
			if json.Unmarshal(row.data, &entry) != nil || entry.Memory == nil || entry.UserID == "" || entry.AppName != appName {
				return Progress{}, errors.New("storage migration: source memory row is invalid")
			}
			metadata := &memory.Metadata{Kind: entry.Memory.Kind, EventTime: entry.Memory.EventTime, Participants: entry.Memory.Participants, Location: entry.Memory.Location}
			if err := target.AddMemory(ctx, memory.UserKey{AppName: appName, UserID: entry.UserID}, entry.Memory.Memory, entry.Memory.Topics, memory.WithMetadata(metadata)); err != nil {
				return Progress{}, err
			}
			if _, err := ledger.ExecContext(ctx, `INSERT INTO storage_migration_items (source_route_hash,table_name,source_key,checksum) VALUES ($1,'runtime_memories',$2,$3) ON CONFLICT DO NOTHING`, job.SourceRouteHash, row.id, checksum); err != nil {
				return Progress{}, err
			}
		}
		progress.CopiedRows++
		mark.Cursor = row.id
	}
	progress.Checkpoint, _ = json.Marshal(mark)
	return progress, nil
}

type knowledgeCheckpoint struct {
	Cursor string `json:"cursor,omitempty"`
}

type knowledgeRow struct {
	documentID, name, content string
	metadata                  []byte
	version                   int64
}

func (copier *PostgresCopier) stepKnowledge(ctx context.Context, job Job, batchSize int) (Progress, error) {
	if copier.ResolveKnowledge == nil {
		return Progress{}, errors.New("storage migration: knowledge resolver unavailable")
	}
	db := copier.Router.MigrationLedgerDB()
	if db == nil {
		return Progress{}, errors.New("storage migration: knowledge catalog unavailable")
	}
	var mark knowledgeCheckpoint
	if len(job.Checkpoint) > 0 && json.Unmarshal(job.Checkpoint, &mark) != nil {
		return Progress{}, errors.New("storage migration: invalid knowledge checkpoint")
	}
	progress := Progress{SourceRows: job.SourceRows, CopiedRows: job.CopiedRows}
	if progress.SourceRows == 0 {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_knowledge_documents WHERE tenant_id=$1 AND app_id=$2 AND status='active'`, job.TenantID, job.AppID).Scan(&progress.SourceRows); err != nil {
			return Progress{}, err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT document_id,name,content,metadata_json,version FROM runtime_knowledge_documents WHERE tenant_id=$1 AND app_id=$2 AND status='active' AND document_id>$3 ORDER BY document_id LIMIT $4`, job.TenantID, job.AppID, mark.Cursor, batchSize)
	if err != nil {
		return Progress{}, err
	}
	var batch []knowledgeRow
	for rows.Next() {
		var row knowledgeRow
		if err := rows.Scan(&row.documentID, &row.name, &row.content, &row.metadata, &row.version); err != nil {
			rows.Close()
			return Progress{}, err
		}
		batch = append(batch, row)
	}
	if err := rows.Close(); err != nil {
		return Progress{}, err
	}
	if len(batch) == 0 {
		progress.Done = true
		progress.Checkpoint, _ = json.Marshal(mark)
		if progress.CopiedRows < progress.SourceRows {
			return Progress{}, errors.New("storage migration: copied knowledge count is below source catalog")
		}
		return progress, nil
	}
	target, err := copier.ResolveKnowledge(ctx, job, job.Target)
	if err != nil || target == nil {
		return Progress{}, errors.New("storage migration: target knowledge backend unavailable")
	}
	defer target.Close()
	for _, row := range batch {
		payload := append(append(append([]byte(row.name), 0), []byte(row.content)...), row.metadata...)
		payload = append(payload, []byte(fmt.Sprint(row.version))...)
		digest := sha256.Sum256(payload)
		checksum := hex.EncodeToString(digest[:])
		var stored string
		err := db.QueryRowContext(ctx, `SELECT checksum FROM storage_migration_items WHERE source_route_hash=$1 AND table_name='runtime_knowledge_documents' AND source_key=$2`, job.SourceRouteHash, row.documentID).Scan(&stored)
		if err == nil {
			if stored != checksum {
				return Progress{}, errors.New("storage migration: source knowledge document changed after checkpoint")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Progress{}, err
		} else {
			var metadata map[string]any
			if json.Unmarshal(row.metadata, &metadata) != nil {
				return Progress{}, errors.New("storage migration: knowledge metadata is invalid")
			}
			if _, err := target.Ingest(ctx, knowledgebase.IngestRequest{DocumentID: row.documentID, Name: row.name, Content: row.content, Metadata: metadata}); err != nil {
				return Progress{}, err
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO storage_migration_items (source_route_hash,table_name,source_key,checksum) VALUES ($1,'runtime_knowledge_documents',$2,$3) ON CONFLICT DO NOTHING`, job.SourceRouteHash, row.documentID, checksum); err != nil {
				return Progress{}, err
			}
		}
		progress.CopiedRows++
		mark.Cursor = row.documentID
	}
	progress.Checkpoint, _ = json.Marshal(mark)
	return progress, nil
}

type artifactCheckpoint struct {
	Cursor string `json:"cursor,omitempty"`
}

type artifactRow struct {
	key, userID, sessionID, filename string
	revision                         int
	mimeType, url, name, checksum    string
	data                             []byte
}

func (copier *PostgresCopier) stepArtifactToS3(ctx context.Context, job Job, appName string, batchSize int) (Progress, error) {
	source, err := copier.Router.Resolve(ctx, job.Source)
	if err != nil {
		return Progress{}, errors.New("storage migration: source backend unavailable")
	}
	target, err := copier.Router.ArtifactForRoute(ctx, job.Target)
	if err != nil {
		return Progress{}, errors.New("storage migration: target artifact backend unavailable")
	}
	if closer, ok := target.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	ledger := copier.Router.MigrationLedgerDB()
	if ledger == nil {
		return Progress{}, errors.New("storage migration: migration ledger unavailable")
	}
	var mark artifactCheckpoint
	if len(job.Checkpoint) > 0 && json.Unmarshal(job.Checkpoint, &mark) != nil {
		return Progress{}, errors.New("storage migration: invalid artifact checkpoint")
	}
	progress := Progress{SourceRows: job.SourceRows, CopiedRows: job.CopiedRows}
	if progress.SourceRows == 0 {
		if err := source.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_artifacts WHERE tenant_id=$1 AND app_id=$2`, job.TenantID, job.AppID).Scan(&progress.SourceRows); err != nil {
			return Progress{}, err
		}
	}
	rows, err := selectArtifactBatch(ctx, source.DB, job.TenantID, job.AppID, mark.Cursor, batchSize)
	if err != nil {
		return Progress{}, err
	}
	if len(rows) == 0 {
		progress.Done = true
		progress.Checkpoint, _ = json.Marshal(mark)
		if progress.CopiedRows < progress.SourceRows {
			return Progress{}, errors.New("storage migration: copied artifact count is below source snapshot")
		}
		return progress, nil
	}
	for _, row := range rows {
		copied, err := copyArtifactRow(ctx, ledger, target, job.SourceRouteHash, appName, row)
		if err != nil {
			return Progress{}, err
		}
		if copied {
			progress.CopiedRows++
		}
		mark.Cursor = row.key
	}
	progress.Checkpoint, _ = json.Marshal(mark)
	return progress, nil
}

func selectArtifactBatch(ctx context.Context, db *sql.DB, tenantID, appID, cursor string, limit int) ([]artifactRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT jsonb_build_array(user_id,session_id,filename,revision)::text,user_id,session_id,filename,revision,mime_type,artifact_url,display_name,data FROM runtime_artifacts WHERE tenant_id=$1 AND app_id=$2 AND jsonb_build_array(user_id,session_id,filename,revision)::text>$3 ORDER BY jsonb_build_array(user_id,session_id,filename,revision)::text LIMIT $4`, tenantID, appID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []artifactRow
	for rows.Next() {
		var row artifactRow
		if err := rows.Scan(&row.key, &row.userID, &row.sessionID, &row.filename, &row.revision, &row.mimeType, &row.url, &row.name, &row.data); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func copyArtifactRow(ctx context.Context, ledger *sql.DB, target artifact.Service, sourceHash, appName string, row artifactRow) (bool, error) {
	value := &artifact.Artifact{Data: row.data, MimeType: row.mimeType, URL: row.url, Name: row.name}
	checksum := storage.ArtifactChecksum(value)
	var storedChecksum string
	err := ledger.QueryRowContext(ctx, `SELECT checksum FROM storage_migration_items WHERE source_route_hash=$1 AND table_name='runtime_artifacts' AND source_key=$2`, sourceHash, row.key).Scan(&storedChecksum)
	if err == nil {
		if storedChecksum != checksum {
			return false, errors.New("storage migration: source artifact changed after checkpoint")
		}
		// The external write may have committed before a Worker lost its job
		// lease. Count the durable ledger row again so the control-plane
		// checkpoint catches up without writing another S3 revision.
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	info := artifact.SessionInfo{AppName: appName, UserID: row.userID, SessionID: row.sessionID}
	current, loadErr := target.LoadArtifact(ctx, info, row.filename, &row.revision)
	if loadErr != nil {
		return false, loadErr
	}
	if current != nil {
		if !bytes.Equal(current.Data, value.Data) || current.MimeType != value.MimeType || current.URL != value.URL || current.Name != value.Name {
			return false, errors.New("storage migration: destination artifact revision conflicts with source")
		}
	} else {
		revision, saveErr := target.SaveArtifact(ctx, info, row.filename, value)
		if saveErr != nil {
			return false, saveErr
		}
		if revision != row.revision {
			return false, errors.New("storage migration: destination artifact revision order mismatch")
		}
	}
	_, err = ledger.ExecContext(ctx, `INSERT INTO storage_migration_items (source_route_hash,table_name,source_key,checksum) VALUES ($1,'runtime_artifacts',$2,$3) ON CONFLICT DO NOTHING`, sourceHash, row.key, checksum)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (copier *PostgresCopier) stepArtifactFromS3(ctx context.Context, job Job, appName string, batchSize int) (Progress, error) {
	source, err := copier.Router.ArtifactForRoute(ctx, job.Source)
	if err != nil {
		return Progress{}, errors.New("storage migration: source artifact backend unavailable")
	}
	if closer, ok := source.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	targetRoute, err := copier.Router.Resolve(ctx, job.Target)
	if err != nil {
		return Progress{}, errors.New("storage migration: target backend unavailable")
	}
	target := &storage.PostgresArtifactService{DB: targetRoute.DB}
	catalog := copier.Router.MigrationLedgerDB()
	if catalog == nil {
		return Progress{}, errors.New("storage migration: artifact catalog unavailable")
	}
	var mark artifactCheckpoint
	if len(job.Checkpoint) > 0 && json.Unmarshal(job.Checkpoint, &mark) != nil {
		return Progress{}, errors.New("storage migration: invalid artifact checkpoint")
	}
	progress := Progress{SourceRows: job.SourceRows, CopiedRows: job.CopiedRows}
	if progress.SourceRows == 0 {
		if err := catalog.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_artifact_catalog WHERE tenant_id=$1 AND app_id=$2`, job.TenantID, job.AppID).Scan(&progress.SourceRows); err != nil {
			return Progress{}, err
		}
	}
	rows, err := selectArtifactCatalogBatch(ctx, catalog, job.TenantID, job.AppID, mark.Cursor, batchSize)
	if err != nil {
		return Progress{}, err
	}
	if len(rows) == 0 {
		progress.Done = true
		progress.Checkpoint, _ = json.Marshal(mark)
		if progress.CopiedRows < progress.SourceRows {
			return Progress{}, errors.New("storage migration: copied artifact count is below source catalog")
		}
		return progress, nil
	}
	for _, row := range rows {
		info := artifact.SessionInfo{AppName: appName, UserID: row.userID, SessionID: row.sessionID}
		value, loadErr := source.LoadArtifact(ctx, info, row.filename, &row.revision)
		if loadErr != nil || value == nil {
			return Progress{}, errors.New("storage migration: cataloged source artifact is unavailable")
		}
		if storage.ArtifactChecksum(value) != row.checksum {
			return Progress{}, errors.New("storage migration: source artifact checksum mismatch")
		}
		row.data, row.mimeType, row.url, row.name = value.Data, value.MimeType, value.URL, value.Name
		copied, copyErr := copyArtifactRow(ctx, targetRoute.DB, target, job.SourceRouteHash, appName, row)
		if copyErr != nil {
			return Progress{}, copyErr
		}
		if copied {
			progress.CopiedRows++
		}
		mark.Cursor = row.key
	}
	progress.Checkpoint, _ = json.Marshal(mark)
	return progress, nil
}

func selectArtifactCatalogBatch(ctx context.Context, db *sql.DB, tenantID, appID, cursor string, limit int) ([]artifactRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT jsonb_build_array(user_id,session_id,filename,revision)::text,user_id,session_id,filename,revision,checksum FROM runtime_artifact_catalog WHERE tenant_id=$1 AND app_id=$2 AND jsonb_build_array(user_id,session_id,filename,revision)::text>$3 ORDER BY jsonb_build_array(user_id,session_id,filename,revision)::text LIMIT $4`, tenantID, appID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []artifactRow
	for rows.Next() {
		var row artifactRow
		if err := rows.Scan(&row.key, &row.userID, &row.sessionID, &row.filename, &row.revision, &row.checksum); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type sourceRow struct {
	key    string
	values []any
}

func selectBatch(ctx context.Context, db *sql.DB, spec tableSpec, scope []any, cursor string, limit int) ([]sourceRow, error) {
	args := append(append([]any(nil), scope...), cursor, limit)
	cursorArg, limitArg := len(scope)+1, len(scope)+2
	query := `SELECT ` + spec.key + `,` + strings.Join(spec.columns, ",") + ` FROM ` + spec.name + ` WHERE ` + spec.filter + ` AND ` + spec.key + `>$` + fmt.Sprint(cursorArg) + ` ORDER BY ` + spec.key + ` LIMIT $` + fmt.Sprint(limitArg)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]sourceRow, 0, limit)
	for rows.Next() {
		item := sourceRow{values: make([]any, len(spec.columns))}
		destinations := make([]any, 0, len(spec.columns)+1)
		destinations = append(destinations, &item.key)
		for index := range item.values {
			destinations = append(destinations, &item.values[index])
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func insertRow(ctx context.Context, db *sql.DB, sourceHash string, spec tableSpec, row sourceRow) (bool, error) {
	payload, err := json.Marshal(row.values)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(payload)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	checksum := hex.EncodeToString(digest[:])
	var inserted int
	err = tx.QueryRowContext(ctx, `INSERT INTO storage_migration_items (source_route_hash,table_name,source_key,checksum) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING RETURNING 1`, sourceHash, spec.name, row.key, checksum).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT checksum FROM storage_migration_items WHERE source_route_hash=$1 AND table_name=$2 AND source_key=$3`, sourceHash, spec.name, row.key).Scan(&stored); err != nil {
			return false, err
		}
		if stored != checksum {
			return false, errors.New("storage migration: source row changed after checkpoint")
		}
		return true, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	placeholders := make([]string, len(row.values))
	for index := range placeholders {
		placeholders[index] = "$" + fmt.Sprint(index+1)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+spec.name+` (`+strings.Join(spec.columns, ",")+`) VALUES (`+strings.Join(placeholders, ",")+`) ON CONFLICT DO NOTHING`, row.values...); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func migrationTables(domain Domain, tenantID, appID, appName string) ([]tableSpec, [][]any, error) {
	switch domain {
	case DomainSession:
		specs := []tableSpec{
			{name: "runtime_session_states", key: "LPAD(id::text,20,'0')", filter: "app_name=$1", columns: []string{"app_name", "user_id", "session_id", "state", "created_at", "updated_at", "expires_at", "deleted_at"}},
			{name: "runtime_session_events", key: "LPAD(id::text,20,'0')", filter: "app_name=$1", columns: []string{"app_name", "user_id", "session_id", "event", "created_at", "updated_at", "expires_at", "deleted_at"}},
			{name: "runtime_session_track_events", key: "LPAD(id::text,20,'0')", filter: "app_name=$1", columns: []string{"app_name", "user_id", "session_id", "track", "event", "created_at", "updated_at", "expires_at", "deleted_at"}},
			{name: "runtime_session_summaries", key: "LPAD(id::text,20,'0')", filter: "app_name=$1", columns: []string{"app_name", "user_id", "session_id", "filter_key", "summary", "updated_at", "expires_at", "deleted_at"}},
			{name: "runtime_app_states", key: "LPAD(id::text,20,'0')", filter: "app_name=$1", columns: []string{"app_name", "key", "value", "created_at", "updated_at", "expires_at", "deleted_at"}},
			{name: "runtime_user_states", key: "LPAD(id::text,20,'0')", filter: "app_name=$1", columns: []string{"app_name", "user_id", "key", "value", "created_at", "updated_at", "expires_at", "deleted_at"}},
		}
		args := make([][]any, len(specs))
		for index := range args {
			args[index] = []any{appName}
		}
		return specs, args, nil
	case DomainMemory:
		return []tableSpec{{name: "runtime_memories", key: "memory_id", filter: "app_name=$1", columns: []string{"memory_id", "app_name", "user_id", "memory_data", "created_at", "updated_at", "deleted_at"}}}, [][]any{{appName}}, nil
	case DomainArtifact:
		return []tableSpec{{name: "runtime_artifacts", key: "jsonb_build_array(user_id,session_id,filename,revision)::text", filter: "tenant_id=$1 AND app_id=$2", columns: []string{"tenant_id", "app_id", "user_id", "session_id", "filename", "revision", "mime_type", "artifact_url", "display_name", "data", "created_at"}}}, [][]any{{tenantID, appID}}, nil
	default:
		return nil, nil, errors.New("storage migration: unsupported domain")
	}
}
