#!/usr/bin/env bash
set -euo pipefail
umask 077

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster_name="${TRPC_AGENT_K8S_CLUSTER_NAME:-trpc-agent-pr24}"
expected_context="kind-${cluster_name}"
namespace="trpc-agent"
mode="${1:---validate}"
local_port="${TRPC_AGENT_K8S_LOCAL_PORT:-28080}"
report_path="${TRPC_AGENT_K8S_REPORT:-$repo_root/docs/acceptance/pr24-kubernetes-demo.md}"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/trpc-agent-pr24.XXXXXX")"
port_forward_pid=""
postgres_scaled=false
redis_scaled=false
model_scaled=false
collector_scaled=false

cleanup() {
  [[ -n "$port_forward_pid" ]] && kill "$port_forward_pid" >/dev/null 2>&1 || true
  if [[ "$postgres_scaled" == true ]]; then kubectl -n "$namespace" scale statefulset/demo-postgres --replicas=1 >/dev/null 2>&1 || true; fi
  if [[ "$redis_scaled" == true ]]; then kubectl -n "$namespace" scale statefulset/demo-redis --replicas=1 >/dev/null 2>&1 || true; fi
  if [[ "$model_scaled" == true ]]; then kubectl -n "$namespace" scale deployment/demo-model --replicas=1 >/dev/null 2>&1 || true; fi
  if [[ "$collector_scaled" == true ]]; then kubectl -n "$namespace" scale deployment/demo-otel-collector --replicas=1 >/dev/null 2>&1 || true; fi
  rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM
trap 'status=$?; echo "PR24 acceptance failed at line $LINENO" >&2; exit "$status"' ERR

for command in kubectl docker curl jq go openssl; do
  command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 1; }
done
if [[ "$mode" != "--validate" && "$mode" != "--run" ]]; then
  echo "usage: $0 [--validate|--run]" >&2
  exit 1
fi

"$repo_root/scripts/kubernetes_validate.sh"
kubectl kustomize "$repo_root/deploy/kubernetes/demo/infra" >"$work_dir/infra.yaml"
kubectl kustomize "$repo_root/deploy/kubernetes/demo/app" >"$work_dir/app.yaml"
kubectl kustomize "$repo_root/deploy/kubernetes/demo/migration" >"$work_dir/migration.yaml"
if grep -Eq '^kind: Secret$|^[[:space:]]*stringData:' "$work_dir"/*.yaml; then
  echo "demo manifests must not contain credentials" >&2
  exit 1
fi
grep -q 'kind: StatefulSet' "$work_dir/infra.yaml"
grep -q 'kind: HorizontalPodAutoscaler' "$work_dir/app.yaml"
grep -q 'kind: PodDisruptionBudget' "$work_dir/app.yaml"
grep -q 'newTag: pr24-demo' "$repo_root/deploy/kubernetes/demo/app/kustomization.yaml"
echo "PASS PR24 demo manifests and secret boundary"
if [[ "$mode" == "--validate" ]]; then
  exit 0
fi

if [[ "${TRPC_AGENT_K8S_CREATE_KIND:-0}" == "1" ]]; then
  command -v kind >/dev/null 2>&1 || { echo "kind is required when TRPC_AGENT_K8S_CREATE_KIND=1" >&2; exit 1; }
  if ! kind get clusters | grep -qx "$cluster_name"; then
    kind create cluster --name "$cluster_name" --wait 180s
  fi
fi
context="$(kubectl config current-context)"
if [[ "$context" != "$expected_context" && "${TRPC_AGENT_K8S_CONFIRM_CONTEXT:-}" != "$context" ]]; then
  echo "refusing context $context; use dedicated $expected_context or set TRPC_AGENT_K8S_CONFIRM_CONTEXT to the exact current context" >&2
  exit 1
fi

image="trpc-agent-service:pr24-demo"
if [[ "${TRPC_AGENT_K8S_SKIP_BUILD:-0}" != "1" ]]; then
  docker build -t "$image" "$repo_root" >/dev/null
  if [[ "$context" == kind-* ]]; then
    kind load docker-image --name "${context#kind-}" "$image" >/dev/null
  fi
fi

kubectl apply -f "$repo_root/deploy/kubernetes/demo/infra/namespace.yaml" >/dev/null
if kubectl -n "$namespace" get secret trpc-agent-secrets >/dev/null 2>&1; then
  gateway_token="$(kubectl -n "$namespace" get secret trpc-agent-secrets -o jsonpath='{.data.gateway-token}' | base64 --decode)"
else
  postgres_password="$(openssl rand -hex 18)"
  admin_token="$(openssl rand -hex 18)"
  admin_value="demo-admin=${admin_token}:demo"
  gateway_token="$(openssl rand -hex 18)"
  model_api_key="$(openssl rand -hex 18)"
  kubectl -n "$namespace" create secret generic trpc-agent-secrets \
    --from-literal=postgres-password="$postgres_password" \
    --from-literal=postgres-dsn="postgres://trpc_agent:${postgres_password}@demo-postgres:5432/trpc_agent?sslmode=disable" \
    --from-literal=redis-url="redis://demo-redis:6379/0" \
    --from-literal=deepseek-api-key="$model_api_key" \
    --from-literal=admin-tokens="$admin_value" \
    --from-literal=gateway-token="$gateway_token" \
    --dry-run=client -o yaml >"$work_dir/secret.yaml"
  kubectl apply -f "$work_dir/secret.yaml" >/dev/null
fi

kubectl apply -k "$repo_root/deploy/kubernetes/demo/infra" >/dev/null
kubectl -n "$namespace" rollout status statefulset/demo-postgres --timeout=5m >/dev/null
kubectl -n "$namespace" rollout status statefulset/demo-redis --timeout=5m >/dev/null
kubectl -n "$namespace" rollout restart deployment/demo-model deployment/demo-otel-collector >/dev/null
kubectl -n "$namespace" rollout status deployment/demo-model --timeout=5m >/dev/null
kubectl -n "$namespace" rollout status deployment/demo-otel-collector --timeout=5m >/dev/null

kubectl -n "$namespace" delete job trpc-agent-migrate --ignore-not-found --wait=true >/dev/null
kubectl apply -k "$repo_root/deploy/kubernetes/demo/migration" >/dev/null
kubectl -n "$namespace" wait --for=condition=complete job/trpc-agent-migrate --timeout=5m >/dev/null
kubectl apply -k "$repo_root/deploy/kubernetes/demo/app" >/dev/null
kubectl -n "$namespace" rollout status deployment/trpc-agent-gateway --timeout=5m >/dev/null
kubectl -n "$namespace" rollout status deployment/trpc-agent-worker --timeout=5m >/dev/null

ready_count() {
  kubectl -n "$namespace" get pods -l "app.kubernetes.io/name=trpc-agent-service,app.kubernetes.io/component=$1" -o json | jq '[.items[] | select(.status.phase=="Running") | select(any(.status.containerStatuses[]?; .ready==true))] | length'
}
gateway_ready="$(ready_count gateway)"
worker_ready="$(ready_count worker)"
[[ "$gateway_ready" -ge 3 && "$worker_ready" -ge 3 ]] || { echo "expected at least three ready Gateway and Worker Pods" >&2; exit 1; }
kubectl -n "$namespace" get pdb -o json | jq -e \
  '[.items[] | select(.status.currentHealthy >= 3 and .status.desiredHealthy == 2 and .status.disruptionsAllowed >= 1)] | length == 2' >/dev/null
kubectl -n "$namespace" get hpa -o json | jq -e \
  '[.items[] | {min:.spec.minReplicas,max:.spec.maxReplicas,target:.spec.scaleTargetRef.name}] | length == 2 and all(.[]; .min == 3 and .max >= 20 and (.target == "trpc-agent-gateway" or .target == "trpc-agent-worker"))' >/dev/null
echo "PASS Gateway/Worker replicas, PDB and HPA"

start_port_forward() {
  if [[ -n "$port_forward_pid" ]]; then
    kill "$port_forward_pid" >/dev/null 2>&1 || true
    wait "$port_forward_pid" >/dev/null 2>&1 || true
    port_forward_pid=""
  fi
  kubectl -n "$namespace" port-forward service/trpc-agent-service "${local_port}:8080" >"$work_dir/port-forward.log" 2>&1 &
  port_forward_pid=$!
  for _ in $(seq 1 30); do
    curl --fail --silent --max-time 2 "http://127.0.0.1:${local_port}/healthz" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "Gateway port-forward did not become healthy" >&2
  return 1
}
start_port_forward
go run "$repo_root/cmd/capacity" -scenario health -requests 100 -concurrency 10 -base-url "http://127.0.0.1:${local_port}" -max-error-rate 0 -max-p95 2s >"$work_dir/capacity.json"
jq -e '.failed == 0 and .requests == 100' "$work_dir/capacity.json" >/dev/null
echo "PASS bounded capacity smoke"
start_port_forward

config_before="$(kubectl -n "$namespace" exec demo-postgres-0 -- psql -At -U trpc_agent -d trpc_agent -c "SELECT COALESCE(MAX(current_config_version),0) FROM tenants WHERE tenant_id='demo'" 2>/dev/null)"
[[ "$config_before" -ge 1 ]] || { echo "published demo config is missing" >&2; exit 1; }

printf 'Authorization: Bearer %s\nX-Channel-Binding: demo-http\nContent-Type: application/json\n' "$gateway_token" >"$work_dir/gateway.headers"
submit_probe() {
  local probe_id accepted_file
  probe_id="pr24-$(date -u +%Y%m%dT%H%M%S)-$$-$RANDOM"
  accepted_file="$work_dir/accepted-${probe_id}.json"
  jq -n --arg id "$probe_id" '{channel:"http",from:("pr24-synthetic-"+$id),message_id:$id,text:"PR24 synthetic probe"}' >"$work_dir/message.json"
  curl --fail --silent --max-time 5 -X POST --header @"$work_dir/gateway.headers" --data-binary @"$work_dir/message.json" "http://127.0.0.1:${local_port}/v1/gateway/messages" >"$accepted_file"
  jq -er '.request_id' "$accepted_file"
}
wait_completed() {
  local request_id="$1" attempts="${2:-45}" terminal=""
  for _ in $(seq 1 "$attempts"); do
    curl --fail --silent --max-time 3 --header @"$work_dir/gateway.headers" --get --data-urlencode "request_id=$request_id" "http://127.0.0.1:${local_port}/v1/gateway/status" >"$work_dir/status.json"
    terminal="$(jq -r '.type // ""' "$work_dir/status.json")"
    [[ "$terminal" == "run.completed" ]] && return 0
    sleep 2
  done
  return 1
}
request_id="$(submit_probe)"
wait_completed "$request_id" || { echo "synthetic Runner probe timed out" >&2; exit 1; }
echo "PASS Gateway -> Redis -> Worker -> model -> PostgreSQL chain"

for component in gateway worker; do
  target="$(kubectl -n "$namespace" get pod -l "app.kubernetes.io/name=trpc-agent-service,app.kubernetes.io/component=$component" -o jsonpath='{.items[0].metadata.name}')"
  kubectl -n "$namespace" delete pod "$target" --wait=false >/dev/null
  kubectl -n "$namespace" rollout status "deployment/trpc-agent-${component}" --timeout=5m >/dev/null
  [[ "$(ready_count "$component")" -ge 3 ]]
done
echo "PASS single Gateway and Worker Pod recovery"

expect_unready() {
  status="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 3 "http://127.0.0.1:${local_port}/readyz" || true)"
  [[ "$status" != "200" ]]
}
for backend in redis postgres; do
  if [[ "$backend" == redis ]]; then resource="statefulset/demo-redis"; redis_scaled=true; else resource="statefulset/demo-postgres"; postgres_scaled=true; fi
  kubectl -n "$namespace" scale "$resource" --replicas=0 >/dev/null
  kubectl -n "$namespace" wait --for=delete pod -l "app.kubernetes.io/name=demo-${backend}" --timeout=3m >/dev/null
  sleep 6
  expect_unready || { echo "$backend outage did not affect readiness" >&2; exit 1; }
  kubectl -n "$namespace" scale "$resource" --replicas=1 >/dev/null
  kubectl -n "$namespace" rollout status "$resource" --timeout=5m >/dev/null
  if [[ "$backend" == redis ]]; then redis_scaled=false; else postgres_scaled=false; fi
  kubectl -n "$namespace" wait --for=condition=Ready pod -l 'app.kubernetes.io/name=trpc-agent-service,app.kubernetes.io/component=gateway' --timeout=5m >/dev/null
  kubectl -n "$namespace" wait --for=condition=Ready pod -l 'app.kubernetes.io/name=trpc-agent-service,app.kubernetes.io/component=worker' --timeout=5m >/dev/null
  start_port_forward
  curl --fail --silent --max-time 3 "http://127.0.0.1:${local_port}/readyz" >/dev/null
done
echo "PASS Redis/PostgreSQL outage readiness and recovery"

kubectl -n "$namespace" scale deployment/demo-model --replicas=0 >/dev/null
model_scaled=true
kubectl -n "$namespace" wait --for=delete pod -l app.kubernetes.io/name=demo-model --timeout=3m >/dev/null
model_fault_request="$(submit_probe)"
sleep 2
kubectl -n "$namespace" scale deployment/demo-model --replicas=1 >/dev/null
kubectl -n "$namespace" rollout status deployment/demo-model --timeout=5m >/dev/null
model_scaled=false
wait_completed "$model_fault_request" 60 || { echo "request did not recover after model outage" >&2; exit 1; }

kubectl -n "$namespace" scale deployment/demo-otel-collector --replicas=0 >/dev/null
collector_scaled=true
kubectl -n "$namespace" wait --for=delete pod -l app.kubernetes.io/name=demo-otel-collector --timeout=3m >/dev/null
curl --fail --silent --max-time 3 "http://127.0.0.1:${local_port}/healthz" >/dev/null
kubectl -n "$namespace" scale deployment/demo-otel-collector --replicas=1 >/dev/null
kubectl -n "$namespace" rollout status deployment/demo-otel-collector --timeout=5m >/dev/null
collector_scaled=false
echo "PASS model request retry and Collector dependency recovery"

GOCACHE="${GOCACHE:-/tmp/trpc-agent-pr24-cache}" go test "$repo_root/trpcservice/delivery" -run 'TestWorker|TestFeishuSenderFlowsThroughOutboxWorker' -count=1 >/dev/null
echo "PASS Sender retry/DLQ deterministic fault regression"

rollout_marker="$(date -u +%Y%m%dT%H%M%S)"
for deployment in trpc-agent-gateway trpc-agent-worker; do
  kubectl -n "$namespace" set env deployment/"$deployment" "PR24_ROLLOUT_MARKER=$rollout_marker" >/dev/null
  kubectl -n "$namespace" rollout status "deployment/$deployment" --timeout=5m >/dev/null
  kubectl -n "$namespace" rollout undo "deployment/$deployment" >/dev/null
  kubectl -n "$namespace" rollout status "deployment/$deployment" --timeout=5m >/dev/null
done
echo "PASS rolling upgrade and rollback"

kubectl -n "$namespace" delete pod demo-postgres-0 --wait=false >/dev/null
kubectl -n "$namespace" rollout status statefulset/demo-postgres --timeout=5m >/dev/null
config_after="$(kubectl -n "$namespace" exec demo-postgres-0 -- psql -At -U trpc_agent -d trpc_agent -c "SELECT COALESCE(MAX(current_config_version),0) FROM tenants WHERE tenant_id='demo'" 2>/dev/null)"
[[ "$config_after" == "$config_before" ]] || { echo "config version changed after PostgreSQL restart" >&2; exit 1; }
pvc_count="$(kubectl -n "$namespace" get pvc -o json | jq '.items | length')"
[[ "$pvc_count" -ge 2 ]] || { echo "demo data PVCs are missing" >&2; exit 1; }
echo "PASS PostgreSQL config persistence and retained PVCs"

mkdir -p "$(dirname "$report_path")"
capacity_p95="$(jq -r '.p95_ms' "$work_dir/capacity.json")"
image_id="$(docker image inspect "$image" --format '{{.Id}}' | cut -c1-19)"
git_baseline="$(git -C "$repo_root" rev-parse HEAD)"
if ! git -C "$repo_root" diff --quiet --ignore-submodules -- || [[ -n "$(git -C "$repo_root" ls-files --others --exclude-standard)" ]]; then
  git_baseline="${git_baseline}+candidate"
fi
cat >"$report_path" <<REPORT
# PR24 Kubernetes Demo 验收报告

- 日期：$(date -u +%Y-%m-%dT%H:%M:%SZ)
- Git 基线：${git_baseline}
- Kubernetes context：${context}
- 镜像：${image_id}（仅记录不可逆摘要）
- 拓扑：Gateway 3 副本、Worker 3 副本、PostgreSQL/Redis StatefulSet、Mock Model、OTel Collector
- 容量冒烟：100 请求，失败 0，p95 ${capacity_p95}ms
- 单 Pod 恢复：通过
- PostgreSQL/Redis 故障与 readiness 恢复：通过
- Model 请求重试/Collector 故障恢复：通过
- Sender retry/DLQ 回归：通过
- PDB live status、HPA contract/admission、滚动升级与 rollback：通过
- PostgreSQL 重启后配置版本：${config_before} → ${config_after}
- PVC：${pvc_count} 个，验收脚本未删除 namespace、PVC 或数据卷

报告不包含 Secret、用户消息正文、数据库密码、完整镜像 ID 或外部标识。
REPORT
echo "PR24 Kubernetes acceptance passed; report=$report_path"
