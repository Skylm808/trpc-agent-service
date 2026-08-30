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

	"github.com/liuzengh/trpc-agent-service/trpcservice/audit"
	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	"github.com/liuzengh/trpc-agent-service/trpcservice/repository"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"gopkg.in/yaml.v3"
)

// ErrInvalidConfig distinguishes caller-correctable publication errors from
// repository and activation failures that must be reported as server errors.
var ErrInvalidConfig = errors.New("admin: invalid configuration")

// Service validates and publishes immutable tenant configurations.
type Service struct {
	store    repository.Store
	now      func() time.Time
	audit    audit.Store
	redactor *servicelog.Redactor
	profiles func(*config.File) error
}

// Option customizes a Service.
type Option func(*Service)

// WithAudit records every publish and rollback decision in the shared audit
// log. The stored payload never contains resolved secret material.
func WithAudit(store audit.Store) Option {
	return func(service *Service) { service.audit = store }
}

// WithRedactor redacts error text before it reaches responses or audit.
func WithRedactor(redactor *servicelog.Redactor) Option {
	return func(service *Service) {
		if redactor != nil {
			service.redactor = redactor
		}
	}
}

// WithProfileValidator adds a deployment-level validation gate, for example
// rejecting non-persistent storage profiles in production. It runs during
// Validate, Publish, and Rollback so invalid configs can never be published.
func WithProfileValidator(validator func(*config.File) error) Option {
	return func(service *Service) { service.profiles = validator }
}

// NewService creates an administration Service.
func NewService(store repository.Store, options ...Option) (*Service, error) {
	if store == nil {
		return nil, errors.New("admin: nil repository")
	}
	service := &Service{store: store, now: time.Now, redactor: servicelog.NewRedactor(nil, nil)}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service, nil
}

// Validate validates one tenant configuration without resolving secrets.
func (service *Service) Validate(payload []byte) (*config.File, error) {
	file, err := config.Load(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if len(file.Tenants) != 1 {
		return nil, fmt.Errorf("%w: publication must contain exactly one tenant", ErrInvalidConfig)
	}
	if service.profiles != nil {
		if err := service.profiles(file); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
	}
	return file, nil
}

// Publish validates and atomically publishes the next configuration version.
func (service *Service) Publish(ctx context.Context, tenantID string, expected tenant.ConfigVersion, payload []byte) (record repository.ConfigRecord, err error) {
	started := service.now()
	defer func() {
		var newVersion tenant.ConfigVersion
		if err == nil {
			newVersion = record.Version
		}
		service.recordDecision(ctx, tenantID, "config.publish", expected, newVersion, err, started)
	}()
	file, err := service.Validate(payload)
	if err != nil {
		return repository.ConfigRecord{}, err
	}
	configured := file.Tenants[0]
	if configured.ID != tenantID {
		return repository.ConfigRecord{}, fmt.Errorf("%w: tenant scope %q does not match payload", ErrInvalidConfig, tenantID)
	}
	if configured.ConfigVersion != expected+1 {
		return repository.ConfigRecord{}, fmt.Errorf("%w: payload config_version must be %d", ErrInvalidConfig, expected+1)
	}
	return service.publish(ctx, configured.Name, configured.Enabled, tenantID, expected, payload, configured.Apps, nil)
}

// Versions lists a tenant's immutable configuration history.
func (service *Service) Versions(ctx context.Context, tenantID string) ([]repository.ConfigRecord, error) {
	return service.store.ListConfigVersions(ctx, tenantID)
}

// Current returns the tenant's published head version.
func (service *Service) Current(ctx context.Context, tenantID string) (repository.ConfigRecord, error) {
	return service.store.GetCurrentConfig(ctx, tenantID)
}

// Rollback republishes an old payload as a new version; history is never rewritten.
func (service *Service) Rollback(ctx context.Context, tenantID string, expected, target tenant.ConfigVersion) (record repository.ConfigRecord, err error) {
	started := service.now()
	defer func() {
		var newVersion tenant.ConfigVersion
		if err == nil {
			newVersion = record.Version
		}
		service.recordDecision(ctx, tenantID, "config.rollback", expected, newVersion, err, started)
	}()
	record, err = service.store.GetConfigVersion(ctx, tenantID, target)
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
		SHA256: hex.EncodeToString(digest[:]), RolledBackFrom: rollback, Apps: apps,
		CreatedBy: ActorFrom(ctx), CreatedAt: service.now().UTC(),
	}, expected)
}

// recordDecision appends one redacted audit record per publish/rollback call.
func (service *Service) recordDecision(ctx context.Context, tenantID, action string, oldVersion, newVersion tenant.ConfigVersion, callErr error, started time.Time) {
	if service.audit == nil || tenantID == "" {
		return
	}
	decision, errorType := "allow", ""
	if callErr != nil {
		decision = "error"
		errorType = fmt.Sprintf("%T", callErr)
	}
	traceID := TraceFrom(ctx)
	if traceID == "" {
		traceID = "admin:" + action
	}
	details := map[string]any{
		"actor":       ActorFrom(ctx),
		"action":      action,
		"old_version": uint64(oldVersion),
		"new_version": uint64(newVersion),
	}
	if callErr != nil {
		details["error"] = service.redactor.RedactString(callErr.Error())
	}
	auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = service.audit.Append(auditCtx, audit.Record{
		TenantID: tenantID, AgentName: action, Decision: decision, Latency: service.now().Sub(started),
		ErrorType: errorType, TraceID: traceID, RequestID: traceID, Details: details,
	})
}
