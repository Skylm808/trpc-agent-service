#!/usr/bin/env bash
set -euo pipefail
umask 077

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
project="${TRPC_AGENT_COMPOSE_PROJECT:-trpc-agent-service-pr14-check}"
compose=(docker compose -p "$project")

for command in docker curl jq rg; do
  command -v "$command" >/dev/null 2>&1 || { echo "ERROR required command is unavailable: $command" >&2; exit 1; }
done
[[ "$project" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || { echo "ERROR invalid Compose project name" >&2; exit 1; }

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/trpc-agent-pr21.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT

"${compose[@]}" config --quiet

for alert in AgentHighErrorRate AgentDLQNotEmpty AgentQueueBacklogGrowing AgentNoLiveWorker AgentPostgreSQLUnavailable; do
  rg --fixed-strings --quiet "alert: $alert" "$repo_root/deploy/prometheus-alerts.yml" || {
    echo "ERROR missing required alert: $alert" >&2
    exit 1
  }
done
rg --fixed-strings --quiet 'exporters: [otlp/tempo]' "$repo_root/deploy/otel-collector.yaml"
rg --fixed-strings --quiet 'uid: tempo' "$repo_root/deploy/grafana/provisioning/datasources/tempo.yml"
echo "PASS Compose, Tempo exporter, Grafana datasource, and five production alerts are structurally present"

if [[ "${TRPC_AGENT_OBSERVABILITY_ACCEPTANCE_LIVE:-0}" != "1" ]]; then
  echo "PR21 observability structural acceptance passed (set TRPC_AGENT_OBSERVABILITY_ACCEPTANCE_LIVE=1 for live checks)"
  exit 0
fi

running_services="$("${compose[@]}" ps --status running --services)"
for service in tempo otel-collector prometheus grafana; do
  rg --fixed-strings --line-regexp --quiet "$service" <<<"$running_services" || { echo "ERROR required service is not running: $service" >&2; exit 1; }
done

"${compose[@]}" exec -T prometheus promtool check config /etc/prometheus/prometheus.yml >"$work_dir/promtool.txt"

curl --fail --silent --show-error --retry 12 --retry-all-errors --retry-delay 2 --max-time 5 http://127.0.0.1:3200/ready >"$work_dir/tempo-ready.txt"
curl --fail --silent --show-error --retry 12 --retry-all-errors --retry-delay 2 --max-time 5 http://127.0.0.1:9090/-/ready >"$work_dir/prometheus-ready.txt"
curl --fail --silent --show-error --retry 12 --retry-all-errors --retry-delay 2 --max-time 5 http://127.0.0.1:3000/api/health >"$work_dir/grafana-health.json"
jq -e '.database == "ok"' "$work_dir/grafana-health.json" >/dev/null
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:9090/api/v1/rules >"$work_dir/rules.json"
for alert in AgentHighErrorRate AgentDLQNotEmpty AgentQueueBacklogGrowing AgentNoLiveWorker AgentPostgreSQLUnavailable; do
  jq -e --arg alert "$alert" '[.data.groups[].rules[] | select(.name == $alert)] | length == 1' "$work_dir/rules.json" >/dev/null
done
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:3000/api/datasources/uid/tempo >"$work_dir/tempo-datasource.json"
jq -e '.uid == "tempo" and .type == "tempo"' "$work_dir/tempo-datasource.json" >/dev/null
echo "PASS Tempo, Prometheus alert evaluation, and Grafana Tempo datasource are live"

trace_id="${TRPC_AGENT_ACCEPTANCE_TRACE_ID:-}"
if [[ -n "$trace_id" ]]; then
  [[ "$trace_id" =~ ^[0-9a-fA-F]{32}$ ]] || { echo "ERROR acceptance trace ID must be 32 hexadecimal characters" >&2; exit 1; }
  curl --fail --silent --show-error --max-time 10 "http://127.0.0.1:3200/api/traces/$trace_id" >"$work_dir/trace.json"
  jq -r '.. | objects | .name? // empty' "$work_dir/trace.json" | sort -u >"$work_dir/span-names.txt"
  for span in channel.callback inbox.claim worker.run runner.execute model.stream session.write memory.summary.write outbox.write outbox.deliver; do
    rg --fixed-strings --line-regexp --quiet "$span" "$work_dir/span-names.txt" || { echo "ERROR persisted trace is missing stage: $span" >&2; exit 1; }
  done
  echo "PASS persisted trace connects Callback, Inbox, Worker, Model, Storage, and Outbox delivery"

  canary="${TRPC_AGENT_ACCEPTANCE_PRIVATE_CANARY:-}"
  if [[ -n "$canary" ]]; then
    if rg --fixed-strings --quiet -- "$canary" "$work_dir/trace.json"; then
      echo "ERROR private canary appeared in persisted trace" >&2
      exit 1
    fi
    curl --fail --silent --show-error --max-time 10 http://127.0.0.1:9090/api/v1/label/__name__/values >"$work_dir/metric-names.json"
    if rg --fixed-strings --quiet -- "$canary" "$work_dir/metric-names.json"; then
      echo "ERROR private canary appeared in metric metadata" >&2
      exit 1
    fi
    echo "PASS supplied private canary is absent from persisted trace and metric metadata"
  fi
fi

echo "PR21 live observability acceptance passed (sanitized output; no trace payload or credential emitted)"
