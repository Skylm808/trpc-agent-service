# Kubernetes production deployment

These manifests deploy separate stateless Gateway and Worker roles. Gateway Pods own HTTP/Admin/channel ingress and only append claimed requests to Redis Streams. Worker Pods own Redis consumption, Inbox recovery, Runner execution, Outbox delivery, storage-migration work, and audit retention. All durable state stays in PostgreSQL/Redis, so neither role requires sticky sessions.

The executable accepts `--role gateway`, `--role worker`, or the backward-compatible `--role all`. Gateway mode does not construct Runtime Bundles or start queue consumers. Worker mode does not bind an HTTP listener or mount Channel/Admin routes. Outbox and maintenance loops remain colocated with Worker in this increment; splitting those into dedicated roles is still planned.

## Required inputs

Build and push the exact commit, then replace `trpc-agent-service:replace-me` in both Kustomizations. Create `trpc-agent-secrets` in namespace `trpc-agent` through an external secret manager or an operator-approved command. It must contain:

- `postgres-dsn`
- `redis-url`
- `deepseek-api-key`
- `admin-tokens`

Never commit a rendered Secret. The example HTTP binding is disabled by default; replace the bootstrap ConfigMap with a reviewed tenant version and explicit channel SecretRef before first production traffic. After a tenant has a published version, PostgreSQL remains the source of truth and the file is only a seed.

## Ordered rollout

```bash
kubectl apply -f deploy/kubernetes/base/namespace.yaml
# create/sync trpc-agent-secrets here
kubectl apply -k deploy/kubernetes/migration
kubectl -n trpc-agent wait --for=condition=complete job/trpc-agent-migrate --timeout=10m
kubectl apply -k deploy/kubernetes/base
kubectl -n trpc-agent rollout status deployment/trpc-agent-gateway --timeout=10m
kubectl -n trpc-agent rollout status deployment/trpc-agent-worker --timeout=10m
```

Migration is intentionally separate from the Deployment. For another release, delete only the completed migration Job object and apply it again; do not delete PostgreSQL, Redis, or application data volumes. The migration transaction uses a PostgreSQL advisory lock.

The ingress controller namespace must have label `trpc-agent.io/ingress=allowed`. The default Service is ClusterIP; TLS, WAF/IP allowlisting, and public IM callback routing belong to the cluster ingress layer. The application probes use `/healthz` for process liveness and `/readyz` for bounded PostgreSQL/Redis readiness.

Gateway and Worker have independent HPAs, so callback traffic and Runner load no longer force the same replica count. CPU/memory remain a safe baseline, not final sizing: they cannot see model-provider saturation or queue delay. Production should add a custom-metrics adapter for `agent_queue_depth`, model latency, and Outbox backlog after the capacity run establishes thresholds.

Render and structurally validate both Kustomizations before publishing an image:

```bash
./scripts/kubernetes_validate.sh
```

Capacity, guarded failure injection, and release gates are documented in [capacity](../../docs/capacity.md), [fault drills](../../docs/fault-drills.md), and [production acceptance](../../docs/production-acceptance.md). A real cluster admission dry-run remains environment-specific because CRD/admission and policy behavior cannot be validated without that cluster's API server.

## Reproducible local cluster demo

The `demo` overlays run the production Gateway/Worker split on a dedicated kind cluster with three
replicas of each role, persistent PostgreSQL/Redis StatefulSets, a protocol-compatible model stub and
an OpenTelemetry Collector. The model stub returns only a fixed synthetic answer and neither component
logs request bodies. Create or select the dedicated cluster and run:

```bash
kind create cluster --name trpc-agent-pr24 --wait 180s
./scripts/pr24_kubernetes_acceptance.sh --run
```

Set `TRPC_AGENT_K8S_CREATE_KIND=1` to let the script create the named kind cluster when absent. An
existing non-kind context is refused unless `TRPC_AGENT_K8S_CONFIRM_CONTEXT` exactly matches it. The
script verifies the full Gateway → Redis → Worker → model → PostgreSQL path, bounded callback load,
single-Pod recovery, dependency outages, Sender retry/DLQ behavior, PDB/HPA admission, rolling update,
rollback, config persistence and PVC retention. It scales only explicitly named demo resources and
never deletes the namespace, StatefulSet, PVC, volume, or database. Credentials are generated into a
mode-0600 temporary file and stored only as a Kubernetes Secret; the report contains summaries only.

To validate manifests and the no-inline-Secret boundary without a cluster:

```bash
./scripts/pr24_kubernetes_acceptance.sh --validate
```
