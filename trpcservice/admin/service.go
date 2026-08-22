// Package admin implements tenant configuration administration.
package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/repository"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"gopkg.in/yaml.v3"
)

// Service validates and publishes immutable tenant configurations.
type Service struct {
	store repository.Store
	now   func() time.Time
}

// NewService creates an administration Service.
func NewService(store repository.Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("admin: nil repository")
	}
	return &Service{store: store, now: time.Now}, nil
}

// Validate validates one tenant configuration without resolving secrets.
func (service *Service) Validate(payload []byte) (*config.File, error) {
	file, err := config.Load(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if len(file.Tenants) != 1 {
		return nil, errors.New("admin: publication must contain exactly one tenant")
	}
	return file, nil
}

// Publish validates and atomically publishes the next configuration version.
func (service *Service) Publish(ctx context.Context, tenantID string, expected tenant.ConfigVersion, payload []byte) (repository.ConfigRecord, error) {
	file, err := service.Validate(payload)
	if err != nil {
		return repository.ConfigRecord{}, err
	}
	configured := file.Tenants[0]
	if configured.ID != tenantID {
		return repository.ConfigRecord{}, fmt.Errorf("admin: tenant scope %q does not match payload", tenantID)
	}
	if configured.ConfigVersion != expected+1 {
		return repository.ConfigRecord{}, fmt.Errorf("admin: payload config_version must be %d", expected+1)
	}
	return service.publish(ctx, configured.Name, configured.Enabled, tenantID, expected, payload, configured.Apps, nil)
}

// Versions lists a tenant's immutable configuration history.
func (service *Service) Versions(ctx context.Context, tenantID string) ([]repository.ConfigRecord, error) {
	return service.store.ListConfigVersions(ctx, tenantID)
}

// Rollback republishes an old payload as a new version; history is never rewritten.
func (service *Service) Rollback(ctx context.Context, tenantID string, expected, target tenant.ConfigVersion) (repository.ConfigRecord, error) {
	record, err := service.store.GetConfigVersion(ctx, tenantID, target)
	if err != nil {
		return repository.ConfigRecord{}, err
	}
	file, err := service.Validate(record.Payload)
	if err != nil {
		return repository.ConfigRecord{}, fmt.Errorf("admin: stored config is invalid: %w", err)
	}
	file.Tenants[0].ConfigVersion = expected + 1
	payload, err := yaml.Marshal(file)
	if err != nil {
		return repository.ConfigRecord{}, fmt.Errorf("admin: encode rollback config: %w", err)
	}
	return service.publish(ctx, file.Tenants[0].Name, file.Tenants[0].Enabled, tenantID, expected, payload, file.Tenants[0].Apps, &target)
}

func (service *Service) publish(ctx context.Context, name string, enabled bool, tenantID string, expected tenant.ConfigVersion, payload []byte, apps []tenant.AgentApp, rollback *tenant.ConfigVersion) (repository.ConfigRecord, error) {
	digest := sha256.Sum256(payload)
	return service.store.PublishConfig(ctx, repository.ConfigRecord{
		TenantID: tenantID, TenantName: name, TenantEnabled: enabled, Payload: append([]byte(nil), payload...),
		SHA256: hex.EncodeToString(digest[:]), RolledBackFrom: rollback, Apps: apps, CreatedAt: service.now().UTC(),
	}, expected)
}
