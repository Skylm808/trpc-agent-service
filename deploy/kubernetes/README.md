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
