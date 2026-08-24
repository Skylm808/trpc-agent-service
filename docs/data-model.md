# Data model

PostgreSQL is the production control-plane system of record. Every primary key,
unique key, foreign-key path, and operational index starts with `tenant_id`.
Process-local test doubles are not storage backends and must not be selected by
a production composition.

`tenants.current_config_version` is the optimistic publication head.
`config_versions` is immutable: publishing uses a row lock and compare-and-swap;
rollback copies an old payload into a new version. Secret values are never
stored in configuration payloads—only `SecretRef` provider/key pairs are.

The runtime data model is event-oriented:

```
tenant -> agent_app -> session_head -> message_event
                    |                -> session_summary(cutoff_event_seq)
                    -> memory_entry(source_event_seq, stable memory_id)
channel_binding -> inbox_message -> outbox_message
tenant -> audit_log
tenant -> migration_job
```

`session_heads.last_event_seq`, `last_fence`, and `state_version` are the CAS
coordinates used by PR4. A future fenced writer must update the head, append the
event, and publish its state delta in one transaction. Summary jobs may only
replace a summary when their cutoff is newer. Memory writes use the stable
`memory_id` plus tenant/user/app and the source event unique key for idempotency.

Inbox uniqueness is `(tenant_id, binding_id, external_message_id)`. Outbox
uniqueness is `(tenant_id, dedupe_key)`. Audit rows contain channel, user,
session, agent, tool, decision, latency, error, cost, and trace correlation.

The down migration is destructive and is intended for tests or an explicitly
approved disaster-recovery procedure, never routine production rollback.

The minimal Admin HTTP surface is mounted by `admin.Handler`:

- `POST /v1/tenants/{tenant}/configs/validate`
- `POST /v1/tenants/{tenant}/configs/publish?expected_version=N`
- `GET /v1/tenants/{tenant}/configs`
- `POST /v1/tenants/{tenant}/configs/rollback?expected_version=N&target_version=M`

It deliberately does not implement authentication. A deployment must put the
handler behind an authenticated tenant-scoping middleware before exposing it.
The unit contract tests verify required schema clauses. CI also runs
`scripts/postgres_migrations_test.sh` against ephemeral PostgreSQL 16 and
verifies first up, repeated up, down, an empty public schema, and up again.
