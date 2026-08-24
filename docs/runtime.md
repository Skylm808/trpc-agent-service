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

The repository still contains a deterministic local builder for unit tests and
the quickstart. It is not a production deployment path and is not counted as a
storage backend. It rejects unsupported providers instead of silently falling
back. A production composition must inject the shared PostgreSQL Session/Inbox/
Outbox path, Redis coordination, the selected Memory/Knowledge/Artifact
adapters, and a real model provider. Production configuration must never fall
back to process-local state.
