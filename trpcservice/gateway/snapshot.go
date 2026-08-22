package gateway

import (
	"context"
	"fmt"

	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// SnapshotResolver loads the exact immutable configuration pinned at ingress.
type SnapshotResolver interface {
	Resolve(context.Context, string, string, tenant.ConfigVersion) (config.RuntimeSnapshot, error)
}

// FileSnapshotResolver resolves snapshots from a validated static config file.
type FileSnapshotResolver struct{ File *config.File }

// Resolve rejects a request if the published version no longer matches its pin.
func (resolver FileSnapshotResolver) Resolve(_ context.Context, tenantID, appID string, version tenant.ConfigVersion) (config.RuntimeSnapshot, error) {
	snapshot, err := resolver.File.Snapshot(tenantID, appID)
	if err != nil {
		return config.RuntimeSnapshot{}, err
	}
	if snapshot.Version() != version {
		return config.RuntimeSnapshot{}, fmt.Errorf("gateway: pinned config version %d is unavailable (current %d)", version, snapshot.Version())
	}
	return snapshot, nil
}
