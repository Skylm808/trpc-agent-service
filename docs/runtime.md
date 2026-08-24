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

The service entrypoint injects PostgreSQL Session, Memory, Artifact, Inbox,
Outbox, and Audit services. Redis provides lease/fencing and the distributed
run-event bus. The deterministic quickstart keeps a separate test fixture so it
can run without external services. Unsupported storage profiles are rejected
before the Gateway starts.
