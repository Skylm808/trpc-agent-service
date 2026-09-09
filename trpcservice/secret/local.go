// Package secret resolves SecretRef values without putting secret material in
// configuration snapshots, logs, traces, or formatted errors.
package secret

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// Provider resolves lookup metadata owned by one secret backend. Implementors
// must never include lookup keys or resolved values in returned errors.
type Provider interface {
	Resolve(context.Context, string) (string, error)
}

// ProviderFunc adapts a function to Provider.
type ProviderFunc func(context.Context, string) (string, error)

func (resolve ProviderFunc) Resolve(ctx context.Context, key string) (string, error) {
	return resolve(ctx, key)
}

// Resolver provides a vendor-neutral boundary for Vault/KMS integrations while
// keeping env and mounted-file support built in.
type Resolver struct {
	mu        sync.RWMutex
	providers map[tenant.SecretProvider]Provider
}

// ScopedResolver prevents a SecretRef from being reused by another tenant.
// The first authenticated configuration that claims a provider/key pair owns
// it for the lifetime of this process; values are still resolved by Base.
type ScopedResolver struct {
	Base func(context.Context, tenant.SecretRef) (string, error)
	// Ownership is the durable authorization boundary used by production
	// deployments. A nil store keeps the deterministic in-process behavior for
	// unit tests and local development.
	Ownership OwnershipStore
	mu        sync.Mutex
	owner     map[string]string
}

// OwnershipStore atomically binds one provider/key to its first tenant/app.
// Implementations must not return secret material in errors.
type OwnershipStore interface {
	Claim(context.Context, tenant.SecretRef, string, string) error
}

// SQLOwnershipStore persists SecretRef ownership in PostgreSQL.
type SQLOwnershipStore struct{ DB *sql.DB }

func (store *SQLOwnershipStore) Claim(ctx context.Context, ref tenant.SecretRef, tenantID, appID string) error {
	if store == nil || store.DB == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(appID) == "" || ref.IsZero() {
		return errors.New("secret: ownership store is unavailable")
	}
	_, err := store.DB.ExecContext(ctx, `INSERT INTO secret_ownership (provider, secret_key, tenant_id, app_id, created_at) VALUES ($1,$2,$3,$4,NOW()) ON CONFLICT (provider,secret_key) DO NOTHING`, ref.Provider, ref.Key, tenantID, appID)
	if err != nil {
		return errors.New("secret: ownership claim failed")
	}
	var ownerTenant, ownerApp string
	if err := store.DB.QueryRowContext(ctx, `SELECT tenant_id, app_id FROM secret_ownership WHERE provider=$1 AND secret_key=$2`, ref.Provider, ref.Key).Scan(&ownerTenant, &ownerApp); err != nil {
		return errors.New("secret: ownership lookup failed")
	}
	if ownerTenant != tenantID || ownerApp != appID {
		return errors.New("secret: reference is not authorized for this tenant")
	}
	return nil
}

func NewScopedResolver(base func(context.Context, tenant.SecretRef) (string, error)) *ScopedResolver {
	return &ScopedResolver{Base: base, owner: make(map[string]string)}
}

func (resolver *ScopedResolver) Resolve(ctx context.Context, tenantID, appID string, ref tenant.SecretRef) (string, error) {
	if resolver == nil || resolver.Base == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(appID) == "" || ref.IsZero() {
		return "", errors.New("secret: scoped resolution is unavailable")
	}
	if resolver.Ownership != nil {
		if err := resolver.Ownership.Claim(ctx, ref, tenantID, appID); err != nil {
			return "", err
		}
	}
	key := string(ref.Provider) + "\x00" + ref.Key
	resolver.mu.Lock()
	if resolver.owner == nil {
		resolver.owner = make(map[string]string)
	}
	if owner, ok := resolver.owner[key]; ok && owner != tenantID {
		resolver.mu.Unlock()
		return "", errors.New("secret: reference is not authorized for this tenant")
	}
	resolver.owner[key] = tenantID
	resolver.mu.Unlock()
	return resolver.Base(ctx, ref)
}

// SetOwnershipStore installs the durable ACL used by production bootstrap.
func (resolver *ScopedResolver) SetOwnershipStore(store OwnershipStore) {
	if resolver != nil {
		resolver.Ownership = store
	}
}

func NewResolver() *Resolver {
	return &Resolver{providers: make(map[tenant.SecretProvider]Provider)}
}

// Register adds a deployment adapter. Built-in env/file providers cannot be
// replaced. Registration is normally completed before serving requests.
func (resolver *Resolver) Register(name tenant.SecretProvider, provider Provider) error {
	if resolver == nil || provider == nil || (name != tenant.SecretProviderVault && name != tenant.SecretProviderKMS) {
		return errors.New("secret: invalid external provider registration")
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if _, exists := resolver.providers[name]; exists {
		return errors.New("secret: external provider is already registered")
	}
	resolver.providers[name] = provider
	return nil
}

// Resolve resolves a reference without exposing its lookup key in errors.
func (resolver *Resolver) Resolve(ctx context.Context, ref tenant.SecretRef) (string, error) {
	if ref.Provider == tenant.SecretProviderEnv || ref.Provider == tenant.SecretProviderFile {
		return ResolveLocal(ref)
	}
	if resolver == nil {
		return "", errors.New("secret: external provider is unavailable")
	}
	resolver.mu.RLock()
	provider := resolver.providers[ref.Provider]
	resolver.mu.RUnlock()
	if provider == nil {
		return "", errors.New("secret: external provider is unavailable")
	}
	value, err := provider.Resolve(ctx, ref.Key)
	if err != nil {
		return "", errors.New("secret: external provider resolution failed")
	}
	if value == "" {
		return "", errors.New("secret: resolved value is empty")
	}
	return value, nil
}

var defaultResolver = NewResolver()

// RegisterProvider installs a process-wide Vault/KMS adapter. It intentionally
// does not expose a way to replace the built-in env/file resolvers.
func RegisterProvider(name tenant.SecretProvider, provider Provider) error {
	return defaultResolver.Register(name, provider)
}

// Resolve uses the process resolver. Deployments can register Vault or KMS
// adapters during bootstrap; absent adapters fail closed.
func Resolve(ctx context.Context, ref tenant.SecretRef) (string, error) {
	return defaultResolver.Resolve(ctx, ref)
}

// ResolveLocal resolves environment and mounted-file references. Vault and KMS
// references require a deployment-specific resolver and are rejected here.
func ResolveLocal(ref tenant.SecretRef) (string, error) {
	var value string
	switch ref.Provider {
	case tenant.SecretProviderEnv:
		value = os.Getenv(ref.Key)
	case tenant.SecretProviderFile:
		content, err := os.ReadFile(ref.Key)
		if err != nil {
			return "", errors.New("secret: read mounted secret failed")
		}
		value = strings.TrimRight(string(content), "\r\n")
	default:
		return "", errors.New("secret: local provider is unsupported")
	}
	if value == "" {
		return "", errors.New("secret: resolved value is empty")
	}
	return value, nil
}
