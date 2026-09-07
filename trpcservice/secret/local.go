// Package secret resolves SecretRef values without putting secret material in
// configuration snapshots, logs, traces, or formatted errors.
package secret

import (
	"context"
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
