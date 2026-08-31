package storagemigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liuzengh/trpc-agent-service/trpcservice/storage"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// Copier advances one bounded backfill batch.
type Copier interface {
	Step(context.Context, Job, int) (Progress, error)
}

// PostgresCopier copies whitelisted runtime tables between routed clusters.
type PostgresCopier struct{ Router *storage.Router }

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
	var inserted int
	err = tx.QueryRowContext(ctx, `INSERT INTO storage_migration_items (source_route_hash,table_name,source_key,checksum) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING RETURNING 1`, sourceHash, spec.name, row.key, hex.EncodeToString(digest[:])).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
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
