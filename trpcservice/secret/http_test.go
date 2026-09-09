package secret

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPProviderResolvesVaultWithoutLeakingMetadata(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/secret/data/model" || request.Header.Get("X-Vault-Token") != "vault-token" {
			t.Fatalf("unexpected request path=%q", request.URL.Path)
		}
		_, _ = response.Write([]byte(`{"data":{"data":{"value":"resolved"}}}`))
	}))
	defer server.Close()
	provider := &HTTPProvider{Endpoint: server.URL, Token: "vault-token", Mode: HTTPProviderVault, Client: server.Client()}
	value, err := provider.Resolve(context.Background(), "secret/data/model")
	if err != nil || value != "resolved" {
		t.Fatalf("value=%q err=%v", value, err)
	}
}
