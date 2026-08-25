package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

func TestResolveLocalEnvironmentAndFile(t *testing.T) {
	t.Setenv("TEST_LOCAL_SECRET", "env-value")
	value, err := ResolveLocal(tenant.SecretRef{Provider: tenant.SecretProviderEnv, Key: "TEST_LOCAL_SECRET"})
	if err != nil || value != "env-value" {
		t.Fatalf("environment value=%q err=%v", value, err)
	}
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err = ResolveLocal(tenant.SecretRef{Provider: tenant.SecretProviderFile, Key: path})
	if err != nil || value != "file-value" {
		t.Fatalf("file value=%q err=%v", value, err)
	}
}

func TestResolveLocalErrorDoesNotRevealReferenceKey(t *testing.T) {
	const lookupKey = "SENSITIVE_LOOKUP_METADATA"
	_, err := ResolveLocal(tenant.SecretRef{Provider: tenant.SecretProviderEnv, Key: lookupKey})
	if err == nil || strings.Contains(err.Error(), lookupKey) {
		t.Fatalf("error leaked lookup metadata: %v", err)
	}
}
