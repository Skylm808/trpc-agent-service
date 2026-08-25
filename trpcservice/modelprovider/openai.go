// Package modelprovider constructs real tenant-scoped model clients.
package modelprovider

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/liuzengh/trpc-agent-service/trpcservice/secret"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
)

const (
	ProviderDeepSeek         = "deepseek"
	ProviderOpenAI           = "openai"
	ProviderOpenAICompatible = "openai-compatible"
)

// New resolves the profile's SecretRef once and constructs an OpenAI-compatible
// client. Returned errors never include the secret value or lookup key.
func New(profile tenant.ModelProfile) (model.Model, error) {
	return newOpenAICompatible(profile, nil)
}

func newOpenAICompatible(profile tenant.ModelProfile, transport http.RoundTripper) (model.Model, error) {
	provider := strings.ToLower(strings.TrimSpace(profile.Provider))
	if provider != ProviderDeepSeek && provider != ProviderOpenAI && provider != ProviderOpenAICompatible {
		return nil, fmt.Errorf("model provider: unsupported provider %q", profile.Provider)
	}
	if strings.TrimSpace(profile.Name) == "" {
		return nil, errors.New("model provider: model name is required")
	}
	apiKey, err := secret.ResolveLocal(profile.APIKey)
	if err != nil {
		return nil, errors.New("model provider: resolve API credential failed")
	}
	options := []openaimodel.Option{openaimodel.WithAPIKey(apiKey)}
	if transport != nil {
		options = append(options, openaimodel.WithHTTPClientOptions(openaimodel.WithHTTPClientTransport(transport)))
	}
	if profile.BaseURL != "" {
		options = append(options, openaimodel.WithBaseURL(profile.BaseURL))
	}
	if provider == ProviderDeepSeek {
		options = append(options, openaimodel.WithVariant(openaimodel.VariantDeepSeek))
	}
	return openaimodel.New(profile.Name, options...), nil
}
