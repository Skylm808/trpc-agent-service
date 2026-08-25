// Package secret resolves SecretRef values without putting secret material in
// configuration snapshots, logs, traces, or formatted errors.
package secret

import (
	"errors"
	"os"
	"strings"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

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
