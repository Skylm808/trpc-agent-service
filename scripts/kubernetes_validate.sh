#!/usr/bin/env bash
set -euo pipefail

command -v kubectl >/dev/null 2>&1 || {
  echo "kubectl is required to render Kubernetes manifests" >&2
  exit 1
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
render_dir="$(mktemp -d "${TMPDIR:-/tmp}/trpc-agent-k8s.XXXXXX")"
trap 'rm -rf "$render_dir"' EXIT

kubectl kustomize "$repo_root/deploy/kubernetes/base" >"$render_dir/base.yaml"
kubectl kustomize "$repo_root/deploy/kubernetes/migration" >"$render_dir/migration.yaml"

require_text() {
  local pattern="$1" file="$2"
  grep -Eq "$pattern" "$file" || {
    echo "missing required manifest pattern: $pattern" >&2
    exit 1
  }
}

for kind in Namespace ServiceAccount ConfigMap Service Deployment PodDisruptionBudget HorizontalPodAutoscaler NetworkPolicy; do
  require_text "^kind: ${kind}$" "$render_dir/base.yaml"
done
require_text '^kind: Job$' "$render_dir/migration.yaml"
require_text 'path: /readyz' "$render_dir/base.yaml"
require_text 'path: /healthz' "$render_dir/base.yaml"
require_text 'secretKeyRef:' "$render_dir/base.yaml"
require_text 'automountServiceAccountToken: false' "$render_dir/base.yaml"
require_text 'readOnlyRootFilesystem: true' "$render_dir/base.yaml"
require_text 'maxUnavailable: 0' "$render_dir/base.yaml"

if grep -Eq '^kind: Secret$|^[[:space:]]*stringData:' "$render_dir/base.yaml" "$render_dir/migration.yaml"; then
  echo "rendered manifests must not contain Kubernetes Secret values" >&2
  exit 1
fi
if grep -Eq '^kind: Job$' "$render_dir/base.yaml"; then
  echo "migration Job must remain separate from the service rollout" >&2
  exit 1
fi

echo "Kubernetes manifests rendered and passed structural safety checks"
