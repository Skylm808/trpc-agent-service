package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

func TestAuditTenantScopeAndSecretRedaction(t *testing.T) {
	const canary = "audit-canary"
	store := NewMemoryStore(servicelog.NewRedactor(nil, []string{canary}))
	for _, tenantID := range []string{"a", "b"} {
		if err := store.Append(context.Background(), Record{TenantID: tenantID, Decision: "allow", TraceID: "trace", ErrorType: "token=" + canary, Details: map[string]any{"authorization": canary, "message": canary}}); err != nil {
			t.Fatal(err)
		}
	}
	records := store.Records("a")
	if len(records) != 1 || records[0].TenantID != "a" {
		t.Fatalf("records=%+v", records)
	}
	payload, _ := json.Marshal(records)
	if strings.Contains(string(payload), canary) {
		t.Fatalf("audit leaked secret: %s", payload)
	}
}

func TestAuditRetentionIsTenantScoped(t *testing.T) {
	store := NewMemoryStore(nil)
	now := time.Now().UTC()
	for _, record := range []Record{
		{TenantID: "a", Decision: "allow", TraceID: "old-a", CreatedAt: now.Add(-48 * time.Hour)},
		{TenantID: "a", Decision: "allow", TraceID: "new-a", CreatedAt: now},
		{TenantID: "b", Decision: "allow", TraceID: "old-b", CreatedAt: now.Add(-48 * time.Hour)},
	} {
		if err := store.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := PruneTenant(context.Background(), store, "a", tenant.AuditPolicy{RetentionDays: 1}, now)
	if err != nil || deleted != 1 || len(store.Records("a")) != 1 || len(store.Records("b")) != 1 {
		t.Fatalf("deleted=%d err=%v a=%v b=%v", deleted, err, store.Records("a"), store.Records("b"))
	}
}
