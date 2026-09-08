package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
	"github.com/liuzengh/trpc-agent-service/trpcservice/idempotency"
	"github.com/liuzengh/trpc-agent-service/trpcservice/policy"
	serviceruntime "github.com/liuzengh/trpc-agent-service/trpcservice/runtime"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessioncoord"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

func TestProcessorCompletesDurableTurnAndOutbox(t *testing.T) {
	file := processorTestConfig()
	if err := file.Validate(); err != nil {
		t.Fatal(err)
	}
	inbox := idempotency.NewMemoryStore()
	writes := sessioncoord.NewMemoryWriteStore()
	coordinator, err := sessioncoord.NewCoordinator(writes)
	if err != nil {
		t.Fatal(err)
	}
	runtimes, err := serviceruntime.NewManager(TestRuntimeFactory(writes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := runtimes.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	message := gateway.InboundMessage{
		TenantID: "tenant", AppID: "app", BindingID: "wecom", ExternalMessageID: "message",
		ExternalUserID: "external", UserID: "wecom/wecom/external", SessionID: "dm/wecom/external",
		Text: "synthetic", TraceID: "trace", ConfigVersion: 1, ReceivedAt: time.Now().UTC(),
	}
	claim, won, err := inbox.Claim(context.Background(), message, "gateway", time.Second)
	if err != nil || !won {
		t.Fatalf("claim won=%v err=%v", won, err)
	}
	processor := &Processor{
		WorkerID: "worker-a", Inbox: inbox, Coordinator: coordinator, Writes: writes,
		Runtimes: runtimes, Snapshots: gateway.FileSnapshotResolver{File: file},
		Policy: &policy.Engine{Identity: policy.AuthenticatedIdentityAuthorizer{}}, LeaseTTL: time.Second,
	}
	if err := processor.Process(context.Background(), claim.RunRequest()); err != nil {
		t.Fatal(err)
	}
	key := messageKey(message)
	head, events, summary, memories := writes.Snapshot(key)
	if head.LastEventSeq != 1 || len(events) != 1 || summary == nil || len(memories) != 1 {
		t.Fatalf("head=%d events=%d summary=%t memories=%d", head.LastEventSeq, len(events), summary != nil, len(memories))
	}
	if _, ok := writes.Outbox("tenant", "reply:tenant/wecom/message"); !ok {
		t.Fatal("outbox reply missing")
	}
	if err := processor.Process(context.Background(), claim.RunRequest()); !errors.Is(err, idempotency.ErrClaimOwner) {
		t.Fatalf("completed claim rerun error=%v", err)
	}
}

func messageKey(message gateway.InboundMessage) gateway.SessionKey {
	return gateway.SessionKey{TenantID: message.TenantID, AppID: message.AppID, UserID: message.UserID, SessionID: message.SessionID}
}

func processorTestConfig() *config.File {
	backend := tenant.BackendConfig{Type: tenant.BackendInMemory}
	return &config.File{SchemaVersion: config.CurrentSchemaVersion, Tenants: []tenant.Tenant{{
		ID: "tenant", Name: "Tenant", Enabled: true, ConfigVersion: 1,
		Audit: tenant.AuditPolicy{Enabled: true, RetentionDays: 1},
		Apps: []tenant.AgentApp{{
			ID: "app", Name: "App", Enabled: true, Config: tenant.AppConfig{Instruction: "Return a deterministic reply."},
			Model:    tenant.ModelProfile{Provider: "mock", Name: "offline-mock", MaxTokens: 64},
			Tools:    tenant.ToolPolicy{Allow: []string{"echo"}},
			Channels: []tenant.ChannelBinding{{ID: "wecom", Type: tenant.ChannelTypeWeCom, ProviderAccountID: "ww-test", ProviderAppID: "1000002", Token: processorSecret("TOKEN"), Secret: processorSecret("SECRET"), EncryptionKey: processorSecret("AES"), Enabled: true}},
			Storage:  tenant.StorageProfile{Session: backend, Memory: backend, Summary: backend, Artifact: backend, Knowledge: backend, Audit: backend},
		}},
	}}}
}

func processorSecret(key string) tenant.SecretRef {
	return tenant.SecretRef{Provider: tenant.SecretProviderEnv, Key: "SYNTHETIC_" + key}
}
