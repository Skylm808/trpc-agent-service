// Package runtime manages immutable, tenant-scoped tRPC-Agent-Go runtimes.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	serviceagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storage"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/plugin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

// ErrDrainTimeout indicates that a canceled Runner did not close its stream.
var ErrDrainTimeout = errors.New("runtime: event stream drain timeout")

// RunInput contains only trusted routing fields; callers cannot supply RunOptions.
type RunInput struct {
	RequestID, UserID, SessionID string
	Text                         string
}

// RunResult contains the complete event sequence emitted before closure.
type RunResult struct{ Events []*event.Event }

// Bundle owns one immutable tenant/app/config-version runtime.
type Bundle struct {
	tenantID, appID, appName string
	version                  tenant.ConfigVersion
	toolNames                []string
	runner                   runner.Runner
	services                 *storage.Services
	drainTimeout             time.Duration
	closeOnce                sync.Once
	closeErr                 error
}

// NewBundle assembles LLMAgent, storage, tools, plugins, and Runner through public v1.11.2 APIs.
func NewBundle(snapshot config.RuntimeSnapshot, plugins ...plugin.Plugin) (*Bundle, error) {
	pluginNames := make(map[string]struct{}, len(plugins))
	for _, candidate := range plugins {
		if candidate == nil || candidate.Name() == "" {
			return nil, errors.New("runtime: plugin and plugin name are required")
		}
		if _, exists := pluginNames[candidate.Name()]; exists {
			return nil, fmt.Errorf("runtime: duplicate plugin %q", candidate.Name())
		}
		pluginNames[candidate.Name()] = struct{}{}
	}
	app := snapshot.App()
	if app.Model.Provider != "mock" {
		return nil, fmt.Errorf("runtime: model provider %q is not available", app.Model.Provider)
	}
	appName, err := tenant.CanonicalAppName(snapshot.TenantID(), snapshot.AppID())
	if err != nil {
		return nil, err
	}
	tools, toolNames, err := serviceagent.Tools(app.Tools.Allow, app.Tools.Deny)
	if err != nil {
		return nil, err
	}
	services, err := storage.NewInMemory(app.Storage)
	if err != nil {
		return nil, err
	}
	agent := llmagent.New(app.Name, llmagent.WithModel(serviceagent.MockModel{}), llmagent.WithInstruction(app.Config.Instruction), llmagent.WithTools(tools))
	run := runner.NewRunner(appName, agent, runner.WithSessionService(services.Session), runner.WithMemoryService(services.Memory), runner.WithArtifactService(services.Artifact), runner.WithPlugins(plugins...))
	return &Bundle{tenantID: snapshot.TenantID(), appID: snapshot.AppID(), appName: appName, version: snapshot.Version(), toolNames: append([]string(nil), toolNames...), runner: run, services: services, drainTimeout: time.Second}, nil
}

// Scope returns the immutable bundle identity.
func (bundle *Bundle) Scope() (string, string, tenant.ConfigVersion) {
	return bundle.tenantID, bundle.appID, bundle.version
}

// AppName returns the trusted tRPC-Agent-Go isolation key.
func (bundle *Bundle) AppName() string { return bundle.appName }

// ToolNames returns the configured tool surface.
func (bundle *Bundle) ToolNames() []string { return append([]string(nil), bundle.toolNames...) }

// Run executes and owns the event stream until it closes or bounded draining expires.
func (bundle *Bundle) Run(ctx context.Context, input RunInput) (RunResult, error) {
	if ctx == nil {
		return RunResult{}, errors.New("runtime: nil context")
	}
	if bundle == nil || bundle.runner == nil {
		return RunResult{}, errors.New("runtime: bundle is not initialized")
	}
	if input.RequestID == "" || input.UserID == "" || input.SessionID == "" || input.Text == "" {
		return RunResult{}, errors.New("runtime: request, user, session IDs, and text are required")
	}
	stream, err := bundle.runner.Run(ctx, input.UserID, input.SessionID, model.NewUserMessage(input.Text), trpcagent.WithRequestID(input.RequestID), trpcagent.WithAppName(bundle.appName))
	if err != nil {
		return RunResult{}, err
	}
	var result RunResult
	var canceled bool
	var drain <-chan time.Time
	for {
		select {
		case item, ok := <-stream:
			if !ok {
				if canceled {
					return result, ctx.Err()
				}
				return result, nil
			}
			if item != nil {
				result.Events = append(result.Events, item)
			}
		case <-ctx.Done():
			if !canceled {
				canceled = true
				if managed, ok := bundle.runner.(runner.ManagedRunner); ok {
					managed.Cancel(input.RequestID)
				}
				timer := time.NewTimer(bundle.drainTimeout)
				defer timer.Stop()
				drain = timer.C
			}
		case <-drain:
			return result, errors.Join(ctx.Err(), ErrDrainTimeout)
		}
	}
}

// Close idempotently releases Runner and externally injected storage services.
func (bundle *Bundle) Close() error {
	if bundle == nil {
		return nil
	}
	bundle.closeOnce.Do(func() {
		var runnerErr error
		if bundle.runner != nil {
			runnerErr = bundle.runner.Close()
		}
		bundle.closeErr = errors.Join(runnerErr, bundle.services.Close())
	})
	return bundle.closeErr
}
