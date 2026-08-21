#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

unformatted="$(gofmt -l .)"
if [[ -n "$unformatted" ]]; then
  echo "error: gofmt required for:" >&2
  echo "$unformatted" >&2
  exit 1
fi
go mod verify
go vet ./...
go test ./...
go test -race ./...

if go list -deps ./... | grep -E '^trpc\.group/trpc-go/trpc-agent-go/.*/internal(/|$)' >/dev/null; then
  echo "error: imports from trpc-agent-go internal packages are forbidden" >&2
  exit 1
fi

./build.sh
