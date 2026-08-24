package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

var (
	sessionBackends = backendSet(
		tenant.BackendInMemory,
		tenant.BackendRedis,
		tenant.BackendMySQL,
		tenant.BackendPostgres,
		tenant.BackendSQLite,
		tenant.BackendMongoDB,
	)
	memoryBackends = backendSet(
		tenant.BackendInMemory,
		tenant.BackendRedis,
		tenant.BackendMySQL,
		tenant.BackendPostgres,
		tenant.BackendSQLite,
		tenant.BackendExternal,
	)
	artifactBackends = backendSet(
		tenant.BackendInMemory,
		tenant.BackendLocal,
		tenant.BackendS3,
		tenant.BackendCOS,
	)
	knowledgeBackends = backendSet(
		tenant.BackendInMemory,
		tenant.BackendQdrant,
		tenant.BackendMilvus,
		tenant.BackendElasticsearch,
	)
	auditBackends = backendSet(
		tenant.BackendInMemory,
		tenant.BackendMySQL,
		tenant.BackendPostgres,
		tenant.BackendSQLite,
		tenant.BackendExternal,
	)
)

// Validate checks all configuration invariants without resolving secrets.
func (file *File) Validate() error {
	if file == nil {
		return errors.New("config: nil file")
	}
	if file.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf(
			"config: schema_version must be %d", CurrentSchemaVersion,
		)
	}
	if len(file.Tenants) == 0 {
		return errors.New("config: at least one tenant is required")
	}

	tenantIDs := make(map[string]struct{}, len(file.Tenants))
	for tenantIndex := range file.Tenants {
		bindingIDs := make(map[string]struct{})
		current := &file.Tenants[tenantIndex]
		path := fmt.Sprintf("tenants[%d]", tenantIndex)
		if err := validateID(path+".tenant_id", current.ID); err != nil {
			return err
		}
		if _, exists := tenantIDs[current.ID]; exists {
			return fmt.Errorf("config: duplicate tenant_id %q", current.ID)
		}
		tenantIDs[current.ID] = struct{}{}
		if strings.TrimSpace(current.Name) == "" {
			return fmt.Errorf("config: %s.name is required", path)
		}
		if current.ConfigVersion == 0 {
			return fmt.Errorf("config: %s.config_version must be positive", path)
		}
		if err := validateAudit(path+".audit", current.Audit); err != nil {
			return err
		}
		if len(current.Apps) == 0 {
			return fmt.Errorf("config: %s.apps must not be empty", path)
		}

		appIDs := make(map[string]struct{}, len(current.Apps))
		for appIndex := range current.Apps {
			app := &current.Apps[appIndex]
			appPath := fmt.Sprintf("%s.apps[%d]", path, appIndex)
			if err := validateID(appPath+".app_id", app.ID); err != nil {
				return err
			}
			if _, exists := appIDs[app.ID]; exists {
				return fmt.Errorf(
					"config: duplicate app_id %q in tenant %q", app.ID, current.ID,
				)
			}
			appIDs[app.ID] = struct{}{}
			if err := validateApp(appPath, app, bindingIDs); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateApp(
	path string,
	app *tenant.AgentApp,
	bindingIDs map[string]struct{},
) error {
	if strings.TrimSpace(app.Name) == "" {
		return fmt.Errorf("config: %s.name is required", path)
	}
	if strings.TrimSpace(app.Config.Instruction) == "" {
		return fmt.Errorf("config: %s.config.instruction is required", path)
	}
	if err := validateModel(path+".model", app.Model); err != nil {
		return err
	}
	if err := validateTools(path+".tools", app.Tools); err != nil {
		return err
	}
	if len(app.Channels) == 0 {
		return fmt.Errorf("config: %s.channels must not be empty", path)
	}
	for index := range app.Channels {
		channelPath := fmt.Sprintf("%s.channels[%d]", path, index)
		binding := app.Channels[index]
		if err := validateChannel(channelPath, binding); err != nil {
			return err
		}
		if _, exists := bindingIDs[binding.ID]; exists {
			return fmt.Errorf("config: duplicate binding_id %q", binding.ID)
		}
		bindingIDs[binding.ID] = struct{}{}
	}
	if err := validateBackend(
		path+".storage.session", app.Storage.Session, sessionBackends,
	); err != nil {
		return err
	}
	if err := validateBackend(
		path+".storage.memory", app.Storage.Memory, memoryBackends,
	); err != nil {
		return err
	}
	if err := validateBackend(
		path+".storage.summary", app.Storage.Summary, sessionBackends,
	); err != nil {
		return err
	}
	if err := validateBackend(
		path+".storage.artifact", app.Storage.Artifact, artifactBackends,
	); err != nil {
		return err
	}
	if err := validateBackend(
		path+".storage.knowledge", app.Storage.Knowledge, knowledgeBackends,
	); err != nil {
		return err
	}
	return validateBackend(path+".storage.audit", app.Storage.Audit, auditBackends)
}

func validateModel(path string, model tenant.ModelProfile) error {
	if strings.TrimSpace(model.Provider) == "" {
		return fmt.Errorf("config: %s.provider is required", path)
	}
	if strings.TrimSpace(model.Name) == "" {
		return fmt.Errorf("config: %s.name is required", path)
	}
	if model.Provider != "mock" {
		if err := validateSecretRef(path+".api_key", model.APIKey, true); err != nil {
			return err
		}
	} else if err := validateSecretRef(path+".api_key", model.APIKey, false); err != nil {
		return err
	}
	if model.Temperature != nil && (*model.Temperature < 0 || *model.Temperature > 2) {
		return fmt.Errorf("config: %s.temperature must be between 0 and 2", path)
	}
	if model.MaxTokens < 0 {
		return fmt.Errorf("config: %s.max_tokens must not be negative", path)
	}
	return validateHTTPURL(path+".base_url", model.BaseURL, false)
}

func validateTools(path string, policy tenant.ToolPolicy) error {
	if policy.RequestTokenBudget < 0 || policy.MonthlyCostBudgetCents < 0 {
		return fmt.Errorf("config: %s budgets must not be negative", path)
	}
	allow, err := uniqueStrings(path+".allow", policy.Allow)
	if err != nil {
		return err
	}
	deny, err := uniqueStrings(path+".deny", policy.Deny)
	if err != nil {
		return err
	}
	approval, err := uniqueStrings(path+".require_approval", policy.RequireApproval)
	if err != nil {
		return err
	}
	for name := range allow {
		if _, exists := deny[name]; exists {
			return fmt.Errorf("config: tool %q is both allowed and denied", name)
		}
	}
	for name := range approval {
		if _, exists := deny[name]; exists {
			return fmt.Errorf(
				"config: tool %q requires approval but is denied", name,
			)
		}
	}
	return nil
}

func validateChannel(path string, binding tenant.ChannelBinding) error {
	if err := validateID(path+".binding_id", binding.ID); err != nil {
		return err
	}
	if strings.TrimSpace(binding.ProviderAccountID) == "" {
		return fmt.Errorf("config: %s.provider_account_id is required", path)
	}
	switch binding.Type {
	case tenant.ChannelTypeHTTP:
		if err := validateSecretRef(path+".token", binding.Token, false); err != nil {
			return err
		}
		if err := validateSecretRef(path+".secret", binding.Secret, false); err != nil {
			return err
		}
	case tenant.ChannelTypeWeCom:
		if strings.TrimSpace(binding.ProviderAppID) == "" {
			return fmt.Errorf("config: %s.provider_app_id is required", path)
		}
		if agentID, err := strconv.ParseInt(binding.ProviderAppID, 10, 64); err != nil || agentID <= 0 {
			return fmt.Errorf("config: %s.provider_app_id must be a positive integer", path)
		}
		if err := validateSecretRef(path+".token", binding.Token, true); err != nil {
			return err
		}
		if err := validateSecretRef(path+".secret", binding.Secret, true); err != nil {
			return err
		}
		if err := validateSecretRef(path+".encryption_key", binding.EncryptionKey, true); err != nil {
			return err
		}
	case tenant.ChannelTypeTelegram:
		if err := validateSecretRef(path+".token", binding.Token, true); err != nil {
			return err
		}
		if err := validateSecretRef(path+".secret", binding.Secret, false); err != nil {
			return err
		}
	default:
		return fmt.Errorf("config: %s.type is unsupported", path)
	}
	return validateHTTPURL(path+".webhook_url", binding.WebhookURL, false)
}

func validateBackend(
	path string,
	backend tenant.BackendConfig,
	allowed map[tenant.BackendType]struct{},
) error {
	if _, exists := allowed[backend.Type]; !exists {
		return fmt.Errorf(
			"config: %s.type %q is unsupported", path, backend.Type,
		)
	}
	if err := validateSecretRef(path+".credential", backend.Credential, false); err != nil {
		return err
	}
	if strings.TrimSpace(backend.Namespace) != backend.Namespace {
		return fmt.Errorf("config: %s.namespace must be trimmed", path)
	}
	if backend.Endpoint == "" {
		return nil
	}
	if strings.Contains(backend.Endpoint, "#") {
		return fmt.Errorf(
			"config: %s.endpoint must not contain fragments; use SecretRef", path,
		)
	}
	parsed, err := url.Parse(backend.Endpoint)
	if err != nil {
		return fmt.Errorf("config: %s.endpoint is invalid", path)
	}
	if parsed.User != nil {
		return fmt.Errorf(
			"config: %s.endpoint must not contain credentials; use SecretRef", path,
		)
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf(
			"config: %s.endpoint must not contain query parameters; use SecretRef", path,
		)
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return fmt.Errorf(
			"config: %s.endpoint must not contain fragments; use SecretRef", path,
		)
	}
	return nil
}

func validateSecretRef(path string, ref tenant.SecretRef, required bool) error {
	if ref.IsZero() {
		if required {
			return fmt.Errorf("config: %s is required", path)
		}
		return nil
	}
	if ref.Provider == "" || strings.TrimSpace(ref.Key) == "" {
		return fmt.Errorf("config: %s must include provider and key", path)
	}
	switch ref.Provider {
	case tenant.SecretProviderEnv,
		tenant.SecretProviderFile,
		tenant.SecretProviderVault,
		tenant.SecretProviderKMS:
	default:
		return fmt.Errorf("config: %s provider is unsupported", path)
	}
	return nil
}

func validateAudit(path string, policy tenant.AuditPolicy) error {
	if policy.Enabled && policy.RetentionDays <= 0 {
		return fmt.Errorf(
			"config: %s.retention_days must be positive when enabled", path,
		)
	}
	if policy.RetentionDays < 0 {
		return fmt.Errorf("config: %s.retention_days must not be negative", path)
	}
	_, err := uniqueStrings(path+".redact_fields", policy.RedactFields)
	return err
}

func validateID(path, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("config: %s must be non-empty and trimmed", path)
	}
	if len(value) > 128 {
		return fmt.Errorf("config: %s exceeds 128 bytes", path)
	}
	return nil
}

func validateHTTPURL(path, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("config: %s is required", path)
		}
		return nil
	}
	if strings.Contains(value, "#") {
		return fmt.Errorf(
			"config: %s must not contain fragments; use SecretRef", path,
		)
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("config: %s must be an HTTP(S) URL", path)
	}
	if parsed.User != nil {
		return fmt.Errorf("config: %s must not contain credentials", path)
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf(
			"config: %s must not contain query parameters; use SecretRef", path,
		)
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return fmt.Errorf(
			"config: %s must not contain fragments; use SecretRef", path,
		)
	}
	return nil
}

func uniqueStrings(path string, values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("config: %s entries must be non-empty and trimmed", path)
		}
		if _, exists := result[value]; exists {
			return nil, fmt.Errorf("config: %s contains duplicate %q", path, value)
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func backendSet(values ...tenant.BackendType) map[tenant.BackendType]struct{} {
	result := make(map[tenant.BackendType]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
