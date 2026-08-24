#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="${TRPC_AGENT_POSTGRES_IMAGE:-postgres:16-alpine}"
CONTAINER="trpc-agent-service-migrations-$$"
DATABASE="trpc_agent_service"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$CONTAINER" \
  -e POSTGRES_PASSWORD=test-only \
  -e POSTGRES_DB="$DATABASE" \
  "$IMAGE" >/dev/null

for _ in $(seq 1 30); do
  if docker exec "$CONTAINER" pg_isready -U postgres -d "$DATABASE" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$CONTAINER" pg_isready -U postgres -d "$DATABASE" >/dev/null

for file in \
  000001_control_plane.up.sql \
  000001_control_plane.down.sql \
  000002_message_runtime.up.sql \
  000002_message_runtime.down.sql \
  000003_persistent_runtime.up.sql \
  000003_persistent_runtime.down.sql; do
  docker cp "$ROOT/migrations/$file" "$CONTAINER:/tmp/$file" >/dev/null
done

psql_file() {
  docker exec "$CONTAINER" psql -q -v ON_ERROR_STOP=1 -U postgres -d "$DATABASE" -f "/tmp/$1"
}

up() {
  psql_file 000001_control_plane.up.sql
  psql_file 000002_message_runtime.up.sql
  psql_file 000003_persistent_runtime.up.sql
}

down() {
  psql_file 000003_persistent_runtime.down.sql
  psql_file 000002_message_runtime.down.sql
  psql_file 000001_control_plane.down.sql
}

up
up

expected_tables=$'agent_apps\naudit_logs\nchannel_bindings\nconfig_versions\nderived_jobs\nidentity_mappings\ninbox_messages\nmemory_entries\nmessage_events\nmigration_jobs\noutbox_messages\nruntime_app_states\nruntime_artifacts\nruntime_knowledge_documents\nruntime_memories\nruntime_session_events\nruntime_session_states\nruntime_session_summaries\nruntime_session_track_events\nruntime_user_states\nsession_heads\nsession_summaries\ntenants'
actual_tables="$(docker exec "$CONTAINER" psql -At -U postgres -d "$DATABASE" -c "SELECT table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY table_name")"
if [[ "$actual_tables" != "$expected_tables" ]]; then
  echo "unexpected PostgreSQL tables:" >&2
  echo "$actual_tables" >&2
  exit 1
fi

expected_indexes=$'idx_derived_jobs_ready\nuq_inbox_session_seq\nuq_inbox_tenant_id\nuq_message_events_tenant_inbox'
actual_indexes="$(docker exec "$CONTAINER" psql -At -U postgres -d "$DATABASE" -c "SELECT indexname FROM pg_indexes WHERE schemaname='public' AND indexname IN ('uq_message_events_tenant_inbox','uq_inbox_tenant_id','uq_inbox_session_seq','idx_derived_jobs_ready') ORDER BY indexname")"
if [[ "$actual_indexes" != "$expected_indexes" ]]; then
  echo "missing PostgreSQL migration indexes:" >&2
  echo "$actual_indexes" >&2
  exit 1
fi

down
remaining="$(docker exec "$CONTAINER" psql -At -U postgres -d "$DATABASE" -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")"
if [[ "$remaining" != "0" ]]; then
  echo "migration down left $remaining public tables" >&2
  exit 1
fi

up
echo "PostgreSQL migrations: up/up/down/up passed"
