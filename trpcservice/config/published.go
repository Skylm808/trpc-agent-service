package config

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/repository"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// PublishedCache loads published tenant configurations from the control-plane
// store. Versions are immutable, so parsed files are cached forever; the
// tenant head is re-read on every Current call so a publish takes effect
// without a restart.
type PublishedCache struct {
	store    repository.Store
	mu       sync.Mutex
	versions map[publishedKey]*File
}

type publishedKey struct {
	tenantID string
	version  tenant.ConfigVersion
}

// NewPublishedCache creates a cache over the control-plane store.
func NewPublishedCache(store repository.Store) (*PublishedCache, error) {
	if store == nil {
		return nil, errors.New("config: nil control-plane store")
	}
	return &PublishedCache{store: store, versions: make(map[publishedKey]*File)}, nil
}

// Version returns the immutable published file pinned at one version.
func (cache *PublishedCache) Version(ctx context.Context, tenantID string, version tenant.ConfigVersion) (*File, error) {
	key := publishedKey{tenantID: tenantID, version: version}
	cache.mu.Lock()
	if file, ok := cache.versions[key]; ok {
		cache.mu.Unlock()
		return file, nil
	}
	cache.mu.Unlock()
	record, err := cache.store.GetConfigVersion(ctx, tenantID, version)
	if err != nil {
		return nil, err
	}
	file, err := Load(bytes.NewReader(record.Payload))
	if err != nil {
		return nil, err
	}
	cache.mu.Lock()
	cache.versions[key] = file
	cache.mu.Unlock()
	return file, nil
}

// Current returns the tenant's published head. The head lookup is never
// cached; only the resolved immutable version is.
func (cache *PublishedCache) Current(ctx context.Context, tenantID string) (*File, error) {
	record, err := cache.store.GetCurrentConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return cache.Version(ctx, tenantID, record.Version)
}
