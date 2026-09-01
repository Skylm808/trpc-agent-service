#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="${TRPC_AGENT_POSTGRES_IMAGE:-postgres:16-alpine}"
CONTAINER="trpc-agent-service-migrations-$$"
DATABASE="trpc_agent_service"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}

diagnose() {
  local line="$1"
  echo "PostgreSQL integration test failed near line ${line}" >&2
  docker logs --tail 80 "$CONTAINER" >&2 || true
}

trap cleanup EXIT
trap 'diagnose "$LINENO"' ERR

docker run -d --name "$CONTAINER" \
	-p 127.0.0.1::5432 \
  -e POSTGRES_PASSWORD=test-only \
  -e POSTGRES_DB="$DATABASE" \
  "$IMAGE" >/dev/null

ready=false
for _ in $(seq 1 60); do
  # The official image briefly starts an initialization postmaster and then
  # replaces PID 1 with the final postgres process. pg_isready alone can race
  # that handoff on a fresh GitHub runner, so require both the final PID 1 and
  # a successful query against the requested database.
  if docker exec "$CONTAINER" sh -c 'test "$(cat /proc/1/comm)" = postgres' >/dev/null 2>&1 && \
    docker exec "$CONTAINER" psql -At -U postgres -d "$DATABASE" -c 'SELECT 1' 2>/dev/null | grep -qx 1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo "PostgreSQL did not finish initialization within 60 seconds" >&2
  exit 1
fi

for file in \
  000001_control_plane.up.sql \
  000001_control_plane.down.sql \
  000002_message_runtime.up.sql \
  000002_message_runtime.down.sql \
  000003_persistent_runtime.up.sql \
  000003_persistent_runtime.down.sql \
  000004_inbox_recovery.up.sql \
  000004_inbox_recovery.down.sql \
  000005_outbox_delivery.up.sql \
  000005_outbox_delivery.down.sql \
  000006_cluster_control.up.sql \
  000006_cluster_control.down.sql \
  000007_storage_migrations.up.sql \
  000007_storage_migrations.down.sql; do
  docker cp "$ROOT/migrations/$file" "$CONTAINER:/tmp/$file" >/dev/null
done

psql_file() {
  docker exec "$CONTAINER" psql -q -v ON_ERROR_STOP=1 -U postgres -d "$DATABASE" -f "/tmp/$1"
}

up() {
  psql_file 000001_control_plane.up.sql
  psql_file 000002_message_runtime.up.sql
  psql_file 000003_persistent_runtime.up.sql
  psql_file 000004_inbox_recovery.up.sql
  psql_file 000005_outbox_delivery.up.sql
  psql_file 000006_cluster_control.up.sql
  psql_file 000007_storage_migrations.up.sql
}

down() {
  psql_file 000007_storage_migrations.down.sql
  psql_file 000006_cluster_control.down.sql
  psql_file 000005_outbox_delivery.down.sql
  psql_file 000004_inbox_recovery.down.sql
  psql_file 000003_persistent_runtime.down.sql
  psql_file 000002_message_runtime.down.sql
  psql_file 000001_control_plane.down.sql
}

up
up

expected_tables=$'agent_apps\naudit_logs\nchannel_bindings\nconfig_versions\nderived_jobs\nidentity_mappings\ninbox_messages\nmemory_entries\nmessage_events\nmigration_jobs\noutbox_messages\npolicy_budget_reservations\npolicy_budget_usage\nrun_statuses\nruntime_app_states\nruntime_artifacts\nruntime_knowledge_documents\nruntime_memories\nruntime_session_events\nruntime_session_states\nruntime_session_summaries\nruntime_session_track_events\nruntime_user_states\nsession_heads\nsession_summaries\nstorage_migration_items\ntenants\ntool_approvals\nworker_nodes'
actual_tables="$(docker exec "$CONTAINER" psql -At -U postgres -d "$DATABASE" -c "SELECT table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY table_name")"
if [[ "$actual_tables" != "$expected_tables" ]]; then
  echo "unexpected PostgreSQL tables:" >&2
  echo "$actual_tables" >&2
  exit 1
fi

expected_indexes=$'idx_derived_jobs_ready\nidx_inbox_recovery_ready\nidx_migration_jobs_claim\nidx_migration_jobs_config_domain\nidx_outbox_delivery_ready\nidx_run_statuses_binding_updated\nidx_worker_nodes_live\nuq_inbox_session_seq\nuq_inbox_tenant_id\nuq_message_events_tenant_inbox'
actual_indexes="$(docker exec "$CONTAINER" psql -At -U postgres -d "$DATABASE" -c "SELECT indexname FROM pg_indexes WHERE schemaname='public' AND indexname IN ('uq_message_events_tenant_inbox','uq_inbox_tenant_id','uq_inbox_session_seq','idx_derived_jobs_ready','idx_inbox_recovery_ready','idx_outbox_delivery_ready','idx_run_statuses_binding_updated','idx_worker_nodes_live','idx_migration_jobs_claim','idx_migration_jobs_config_domain') ORDER BY indexname")"
if [[ "$actual_indexes" != "$expected_indexes" ]]; then
  echo "missing PostgreSQL migration indexes:" >&2
  echo "$actual_indexes" >&2
  exit 1
fi

postgres_port="$(docker port "$CONTAINER" 5432/tcp | sed 's/.*://')"
(
  cd "$ROOT"
  TRPC_AGENT_POSTGRES_TEST_DSN="postgres://postgres:test-only@127.0.0.1:${postgres_port}/${DATABASE}?sslmode=disable" \
    go test ./trpcservice/idempotency -run TestSQLClaimReadyRecoveryConcurrencyOrderAndDLQ -count=1
  TRPC_AGENT_POSTGRES_TEST_DSN="postgres://postgres:test-only@127.0.0.1:${postgres_port}/${DATABASE}?sslmode=disable" \
    go test ./trpcservice/delivery -run TestSQLStoreClaimsOutboxAcrossWorkersAndRecovers -count=1
  TRPC_AGENT_POSTGRES_TEST_DSN="postgres://postgres:test-only@127.0.0.1:${postgres_port}/${DATABASE}?sslmode=disable" \
    go test ./trpcservice/recovery -run TestPostgresTenantScopedInboxOutboxRecovery -count=1
  TRPC_AGENT_POSTGRES_TEST_DSN="postgres://postgres:test-only@127.0.0.1:${postgres_port}/${DATABASE}?sslmode=disable" \
    go test ./trpcservice/cluster -run TestPostgresCrossNodeStatusCancelHeartbeatBudgetAndApproval -count=1
  TRPC_AGENT_POSTGRES_TEST_DSN="postgres://postgres:test-only@127.0.0.1:${postgres_port}/${DATABASE}?sslmode=disable" \
    go test ./trpcservice/storagemigration -run TestPostgresMigrationWorkerResumesAndCopiesTenantMemory -count=1
)

down
remaining="$(docker exec "$CONTAINER" psql -At -U postgres -d "$DATABASE" -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")"
if [[ "$remaining" != "0" ]]; then
  echo "migration down left $remaining public tables" >&2
  exit 1
fi

up
echo "PostgreSQL migrations and integration paths through message recovery and storage migration: up/up/down/up passed"
