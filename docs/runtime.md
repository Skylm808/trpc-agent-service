# Runtime Bundle

Each `(tenant_id, app_id, config_version)` has an immutable Runtime Bundle. It
builds an `LLMAgent` and `Runner` using only tRPC-Agent-Go v1.11.2 public APIs:
`WithSessionService`, `WithMemoryService`, `WithArtifactService`, and
`WithPlugins`. Every run injects the trusted canonical app name and inbox
request ID; the caller can provide only user text and cannot override role or
RunOptions.

The manager builds once per key, allows different tenants to build in parallel,
rejects stale snapshots, and reference-counts leases. Publishing a new version
retires the old Bundle, but it is closed only after all old leases finish. A
caller must hold its lease for the entire run.

The Bundle owns the event channel. On cancellation it calls `ManagedRunner.Cancel`
when supported and continues draining with a bound, preventing a disconnected
client from blocking the Runner. Bundle close is idempotent and closes the
borrowed Session and Memory services after closing the Runner.

The current builder is intentionally offline: it supports the deterministic
mock model, echo/calculator tools, and all-InMemory storage only. Any configured
non-memory backend or non-mock model is rejected instead of silently falling
back. Redis/PostgreSQL production adapters and fenced writes belong to PR4.
