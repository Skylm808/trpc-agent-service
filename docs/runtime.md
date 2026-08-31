# Runtime Bundle

Each `(tenant_id, app_id, config_version)` has an immutable Runtime Bundle. It
builds an `LLMAgent` and `Runner` using only tRPC-Agent-Go v1.11.2 public APIs:
`WithSessionService`, `WithMemoryService`, `WithArtifactService`, Knowledge
search tools, and `WithPlugins`. Every run injects the trusted canonical app name and inbox
request ID; the caller can provide only user text and cannot override role or
RunOptions.

The manager builds once per key, allows different tenants to build in parallel,
rejects a stale snapshot from replacing an activated head, and reference-counts
leases. Publishing a new version retires the old Bundle only after the new one
builds successfully; a failed build leases the last valid Bundle and retries the
new build on a later request. The old Bundle is closed only after all old leases
finish. A caller must hold its lease for the entire run.

The Bundle owns the event channel. On cancellation it calls `ManagedRunner.Cancel`
when supported and continues draining with a bound, preventing a disconnected
client from blocking the Runner. Bundle close is idempotent and closes the
borrowed Session and Memory services after closing the Runner.

The service entrypoint injects PostgreSQL Session/Memory, routed PostgreSQL or
S3 Artifact, optional PGVector/Qdrant Knowledge, Inbox, Outbox, and Audit services. Redis provides lease/fencing and the distributed
run-event bus. Production model profiles (`deepseek`, `openai`, and
`openai-compatible`) use tRPC-Agent-Go's public OpenAI-compatible Model. API
credentials are resolved from `SecretRef` once per immutable Bundle and are not
copied into snapshots, logs, traces, or errors. The deterministic quickstart
keeps a separate mock fixture; production startup rejects it. Unsupported
storage profiles are rejected before the Gateway starts.
