package tenant

import "fmt"

// ConfigVersion identifies an immutable published tenant configuration.
type ConfigVersion uint64

// SecretProvider identifies the system that owns a secret value.
type SecretProvider string

const (
	// SecretProviderEnv resolves secrets from process environment variables.
	SecretProviderEnv SecretProvider = "env"
	// SecretProviderFile resolves secrets from mounted secret files.
	SecretProviderFile SecretProvider = "file"
	// SecretProviderVault resolves secrets from a Vault-compatible service.
	SecretProviderVault SecretProvider = "vault"
	// SecretProviderKMS resolves encrypted secrets through a KMS provider.
	SecretProviderKMS SecretProvider = "kms"
)

// SecretRef identifies a secret without containing its value.
type SecretRef struct {
	Provider SecretProvider `json:"provider" yaml:"provider"`
	Key      string         `json:"key" yaml:"key"`
}

// IsZero reports whether no secret reference is configured.
func (ref SecretRef) IsZero() bool {
	return ref.Provider == "" && ref.Key == ""
}

// String intentionally omits the provider key so formatted errors and logs do
// not reveal secret lookup metadata.
func (ref SecretRef) String() string {
	if ref.IsZero() {
		return "<secret-ref:none>"
	}
	return fmt.Sprintf("<secret-ref:%s>", ref.Provider)
}

// Tenant is the top-level isolation boundary.
type Tenant struct {
	ID            string        `json:"tenant_id" yaml:"tenant_id"`
	Name          string        `json:"name" yaml:"name"`
	Enabled       bool          `json:"enabled" yaml:"enabled"`
	ConfigVersion ConfigVersion `json:"config_version" yaml:"config_version"`
	Audit         AuditPolicy   `json:"audit" yaml:"audit"`
	Apps          []AgentApp    `json:"apps" yaml:"apps"`
}

// AgentApp contains one tenant-owned Agent application's runtime policy.
type AgentApp struct {
	ID       string           `json:"app_id" yaml:"app_id"`
	Name     string           `json:"name" yaml:"name"`
	Enabled  bool             `json:"enabled" yaml:"enabled"`
	Config   AppConfig        `json:"config" yaml:"config"`
	Model    ModelProfile     `json:"model" yaml:"model"`
	Tools    ToolPolicy       `json:"tools" yaml:"tools"`
	Channels []ChannelBinding `json:"channels" yaml:"channels"`
	Storage  StorageProfile   `json:"storage" yaml:"storage"`
}

// AppConfig contains model-independent Agent behavior.
type AppConfig struct {
	Instruction string `json:"instruction" yaml:"instruction"`
}

// ModelProfile selects and configures one model provider.
type ModelProfile struct {
	Provider    string    `json:"provider" yaml:"provider"`
	Name        string    `json:"name" yaml:"name"`
	BaseURL     string    `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	APIKey      SecretRef `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	Temperature *float64  `json:"temperature,omitempty" yaml:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
}

// ToolPolicy controls tenant-visible and tenant-executable tools.
type ToolPolicy struct {
	Allow                  []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny                   []string `json:"deny,omitempty" yaml:"deny,omitempty"`
	RequireApproval        []string `json:"require_approval,omitempty" yaml:"require_approval,omitempty"`
	RequestTokenBudget     int64    `json:"request_token_budget,omitempty" yaml:"request_token_budget,omitempty"`
	MonthlyCostBudgetCents int64    `json:"monthly_cost_budget_cents,omitempty" yaml:"monthly_cost_budget_cents,omitempty"`
}

// ChannelType identifies a supported inbound and outbound surface.
type ChannelType string

const (
	// ChannelTypeHTTP is the development and automation gateway.
	ChannelTypeHTTP ChannelType = "http"
	// ChannelTypeWeCom is the WeCom channel.
	ChannelTypeWeCom ChannelType = "wecom"
	// ChannelTypeFeishu is the Feishu (Lark) channel.
	ChannelTypeFeishu ChannelType = "feishu"
)

// ChannelBinding binds one provider account to a tenant application.
type ChannelBinding struct {
	ID                string      `json:"binding_id" yaml:"binding_id"`
	Type              ChannelType `json:"type" yaml:"type"`
	ProviderAccountID string      `json:"provider_account_id" yaml:"provider_account_id"`
	ProviderAppID     string      `json:"provider_app_id,omitempty" yaml:"provider_app_id,omitempty"`
	WebhookURL        string      `json:"webhook_url,omitempty" yaml:"webhook_url,omitempty"`
	Token             SecretRef   `json:"token,omitempty" yaml:"token,omitempty"`
	Secret            SecretRef   `json:"secret,omitempty" yaml:"secret,omitempty"`
	EncryptionKey     SecretRef   `json:"encryption_key,omitempty" yaml:"encryption_key,omitempty"`
	Enabled           bool        `json:"enabled" yaml:"enabled"`
}

// BackendType identifies a storage implementation.
type BackendType string

const (
	BackendInMemory      BackendType = "inmemory"
	BackendRedis         BackendType = "redis"
	BackendMySQL         BackendType = "mysql"
	BackendPostgres      BackendType = "postgres"
	BackendSQLite        BackendType = "sqlite"
	BackendMongoDB       BackendType = "mongodb"
	BackendExternal      BackendType = "external"
	BackendLocal         BackendType = "local"
	BackendS3            BackendType = "s3"
	BackendCOS           BackendType = "cos"
	BackendQdrant        BackendType = "qdrant"
	BackendMilvus        BackendType = "milvus"
	BackendElasticsearch BackendType = "elasticsearch"
)

// BackendConfig selects one backend without embedding credentials.
type BackendConfig struct {
	Type            BackendType    `json:"type" yaml:"type"`
	Endpoint        string         `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Credential      SecretRef      `json:"credential,omitempty" yaml:"credential,omitempty"`
	Namespace       string         `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	MigrationTarget *BackendConfig `json:"migration_target,omitempty" yaml:"migration_target,omitempty"`
}

// StorageProfile routes each data domain independently.
type StorageProfile struct {
	Session   BackendConfig `json:"session" yaml:"session"`
	Memory    BackendConfig `json:"memory" yaml:"memory"`
	Summary   BackendConfig `json:"summary" yaml:"summary"`
	Artifact  BackendConfig `json:"artifact" yaml:"artifact"`
	Knowledge BackendConfig `json:"knowledge" yaml:"knowledge"`
	Audit     BackendConfig `json:"audit" yaml:"audit"`
}

// AuditPolicy controls tenant audit retention and content handling.
type AuditPolicy struct {
	Enabled       bool     `json:"enabled" yaml:"enabled"`
	RetentionDays int      `json:"retention_days" yaml:"retention_days"`
	StoreContent  bool     `json:"store_content" yaml:"store_content"`
	RedactFields  []string `json:"redact_fields,omitempty" yaml:"redact_fields,omitempty"`
}

// Clone returns a deep copy safe for mutation by the caller.
func (value Tenant) Clone() Tenant {
	cloned := value
	cloned.Audit = value.Audit.Clone()
	cloned.Apps = make([]AgentApp, len(value.Apps))
	for i := range value.Apps {
		cloned.Apps[i] = value.Apps[i].Clone()
	}
	return cloned
}

// Clone returns a deep copy safe for mutation by the caller.
func (value AgentApp) Clone() AgentApp {
	cloned := value
	if value.Model.Temperature != nil {
		temperature := *value.Model.Temperature
		cloned.Model.Temperature = &temperature
	}
	cloned.Tools.Allow = append([]string(nil), value.Tools.Allow...)
	cloned.Tools.Deny = append([]string(nil), value.Tools.Deny...)
	cloned.Tools.RequireApproval = append(
		[]string(nil), value.Tools.RequireApproval...,
	)
	cloned.Channels = append([]ChannelBinding(nil), value.Channels...)
	cloned.Storage = value.Storage.Clone()
	return cloned
}

// Clone returns a deep copy of all independently routed storage domains.
func (value StorageProfile) Clone() StorageProfile {
	cloned := value
	cloned.Session = value.Session.Clone()
	cloned.Memory = value.Memory.Clone()
	cloned.Summary = value.Summary.Clone()
	cloned.Artifact = value.Artifact.Clone()
	cloned.Knowledge = value.Knowledge.Clone()
	cloned.Audit = value.Audit.Clone()
	return cloned
}

// Clone returns a deep copy of a route and its one-level migration target.
func (value BackendConfig) Clone() BackendConfig {
	cloned := value
	if value.MigrationTarget != nil {
		target := value.MigrationTarget.Clone()
		cloned.MigrationTarget = &target
	}
	return cloned
}

// Clone returns a deep copy safe for mutation by the caller.
func (value AuditPolicy) Clone() AuditPolicy {
	cloned := value
	cloned.RedactFields = append([]string(nil), value.RedactFields...)
	return cloned
}
