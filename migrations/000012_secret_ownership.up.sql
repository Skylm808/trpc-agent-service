-- Bind external secret references to the first tenant/app that claims them.
CREATE TABLE IF NOT EXISTS secret_ownership (
  provider TEXT NOT NULL,
  secret_key TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, secret_key)
);
CREATE INDEX IF NOT EXISTS idx_secret_ownership_scope
  ON secret_ownership (tenant_id, app_id);
