package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	"github.com/liuzengh/trpc-agent-service/trpcservice/policy"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
	toolmcp "trpc.group/trpc-go/trpc-agent-go/tool/mcp"
	tmcp "trpc.group/trpc-go/trpc-mcp-go"
)

const defaultRemoteTimeout = 10 * time.Second

// SecretResolver resolves an immutable SecretRef without exposing its value.
type SecretResolver func(tenant.SecretRef) (string, error)

type mcpToolSet interface {
	Init(context.Context) error
	Tools(context.Context) []trpctool.Tool
	Close() error
}

type mcpFactory func(tenant.MCPServer, map[string]string) mcpToolSet

// CatalogRegistry builds version-pinned external tools for one Runtime Bundle.
// Models cannot add servers or endpoints; all targets originate in a validated
// published AgentApp configuration.
type CatalogRegistry struct {
	resolve    SecretResolver
	mcpFactory mcpFactory
	httpClient *http.Client
}

// Catalog owns the external resources and model-visible tools for one Bundle.
type Catalog struct {
	tools   map[string]trpctool.Tool
	closers []mcpToolSet
}

// NewCatalogRegistry constructs the production tool registry.
func NewCatalogRegistry(resolve SecretResolver) (*CatalogRegistry, error) {
	if resolve == nil {
		return nil, errors.New("tool catalog: secret resolver is required")
	}
	registry := &CatalogRegistry{resolve: resolve, httpClient: defaultBusinessHTTPClient()}
	registry.mcpFactory = func(server tenant.MCPServer, headers map[string]string) mcpToolSet {
		timeout := configuredTimeout(server.TimeoutSeconds)
		return toolmcp.NewMCPToolSet(toolmcp.ConnectionConfig{
			Transport: "streamable", ServerURL: server.Endpoint, Headers: headers,
			Timeout: timeout, Description: "tenant-published MCP server",
		}, toolmcp.WithName(server.ID), toolmcp.WithToolFilterFunc(trpctool.NewIncludeToolNamesFilter(server.AllowedTools...)),
			toolmcp.WithMCPOptions(tmcp.WithClientGetSSEEnabled(false)), toolmcp.WithSessionReconnect(3))
	}
	return registry, nil
}

// Build resolves credentials, initializes named MCP sessions, verifies the
// configured discovery surface, and constructs fixed HTTPS business tools.
func (registry *CatalogRegistry) Build(ctx context.Context, app tenant.AgentApp) (*Catalog, error) {
	if registry == nil || registry.resolve == nil || registry.mcpFactory == nil || registry.httpClient == nil || ctx == nil {
		return nil, errors.New("tool catalog: registry and context are required")
	}
	catalog := &Catalog{tools: make(map[string]trpctool.Tool)}
	failed := true
	defer func() {
		if failed {
			_ = catalog.Close()
		}
	}()
	for _, server := range app.MCPServers {
		if !server.Enabled {
			continue
		}
		headers, secretValues, err := registry.mcpHeaders(server)
		if err != nil {
			return nil, fmt.Errorf("tool catalog: MCP server %q credential is unavailable", server.ID)
		}
		redactor := servicelog.NewRedactor(nil, secretValues)
		set := registry.mcpFactory(server, headers)
		if set == nil {
			return nil, fmt.Errorf("tool catalog: MCP server %q factory failed", server.ID)
		}
		catalog.closers = append(catalog.closers, set)
		serverCtx, cancel := context.WithTimeout(servicelog.WithRedactor(ctx, redactor), configuredTimeout(server.TimeoutSeconds))
		err = set.Init(serverCtx)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("tool catalog: MCP server %q initialization failed", server.ID)
		}
		discoveredTools := set.Tools(serverCtx)
		cancel()
		discovered := make(map[string]trpctool.Tool)
		for _, candidate := range discoveredTools {
			if candidate == nil || candidate.Declaration() == nil {
				return nil, fmt.Errorf("tool catalog: MCP server %q returned an invalid tool", server.ID)
			}
			discovered[candidate.Declaration().Name] = candidate
		}
		serverCallGate := make(chan struct{}, 1)
		for _, remoteName := range server.AllowedTools {
			candidate := discovered[remoteName]
			callable, ok := candidate.(trpctool.CallableTool)
			if !ok {
				return nil, fmt.Errorf("tool catalog: MCP server %q is missing a published tool", server.ID)
			}
			exposed := "mcp__" + server.ID + "__" + remoteName
			if _, exists := catalog.tools[exposed]; exists {
				return nil, fmt.Errorf("tool catalog: duplicate exposed tool %q", exposed)
			}
			catalog.tools[exposed] = &safeRemoteTool{delegate: callable, declaration: renamedDeclaration(candidate.Declaration(), exposed), redactor: redactor, callGate: serverCallGate}
		}
	}
	for _, configured := range app.BusinessTools {
		if !configured.Enabled {
			continue
		}
		credential, err := registry.resolve(configured.Credential)
		if err != nil {
			return nil, fmt.Errorf("tool catalog: business tool %q credential is unavailable", configured.Name)
		}
		if _, exists := catalog.tools[configured.Name]; exists {
			return nil, fmt.Errorf("tool catalog: duplicate exposed tool %q", configured.Name)
		}
		catalog.tools[configured.Name] = &HTTPJSONTool{config: configured, credential: credential, client: registry.httpClient, redactor: servicelog.NewRedactor(nil, []string{credential})}
	}
	failed = false
	return catalog, nil
}

// Preflight builds and closes an App catalog, including MCP Initialize/ListTools.
func (registry *CatalogRegistry) Preflight(ctx context.Context, app tenant.AgentApp) error {
	catalog, err := registry.Build(ctx, app)
	if err != nil {
		return err
	}
	return catalog.Close()
}

// Tools returns a defensive map copy owned by the caller's Bundle.
func (catalog *Catalog) Tools() map[string]trpctool.Tool {
	if catalog == nil {
		return nil
	}
	result := make(map[string]trpctool.Tool, len(catalog.tools))
	for name, candidate := range catalog.tools {
		result[name] = candidate
	}
	return result
}

// Close releases every MCP session. Errors are intentionally generic so a
// remote server cannot place credentials or internal topology in shutdown logs.
func (catalog *Catalog) Close() error {
	if catalog == nil {
		return nil
	}
	var closeErr error
	for index := len(catalog.closers) - 1; index >= 0; index-- {
		if err := catalog.closers[index].Close(); err != nil {
			closeErr = errors.Join(closeErr, errors.New("tool catalog: MCP session close failed"))
		}
	}
	catalog.closers = nil
	return closeErr
}

func (registry *CatalogRegistry) mcpHeaders(server tenant.MCPServer) (map[string]string, []string, error) {
	if server.Credential.IsZero() {
		return nil, nil, nil
	}
	credential, err := registry.resolve(server.Credential)
	if err != nil {
		return nil, nil, err
	}
	header := strings.TrimSpace(server.CredentialHeader)
	if header == "" {
		header = "Authorization"
	}
	value := credential
	if strings.EqualFold(header, "Authorization") {
		scheme := strings.TrimSpace(server.CredentialScheme)
		if scheme == "" {
			scheme = "Bearer"
		}
		value = scheme + " " + credential
	}
	return map[string]string{header: value}, []string{credential, value}, nil
}

func configuredTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultRemoteTimeout
	}
	return time.Duration(seconds) * time.Second
}

func renamedDeclaration(original *trpctool.Declaration, name string) *trpctool.Declaration {
	return &trpctool.Declaration{Name: name, Description: original.Description, InputSchema: original.InputSchema, OutputSchema: original.OutputSchema}
}

type safeRemoteTool struct {
	delegate    trpctool.CallableTool
	declaration *trpctool.Declaration
	redactor    *servicelog.Redactor
	callGate    chan struct{}
}

func (tool *safeRemoteTool) Declaration() *trpctool.Declaration { return tool.declaration }
func (tool *safeRemoteTool) ToolMetadata() trpctool.ToolMetadata {
	if tool == nil {
		return trpctool.ToolMetadata{}
	}
	metadata := trpctool.MetadataOf(tool.delegate)
	metadata.OpenWorld = true
	return metadata
}
func (tool *safeRemoteTool) Call(ctx context.Context, args []byte) (any, error) {
	if tool == nil || tool.delegate == nil || tool.redactor == nil || tool.callGate == nil || ctx == nil {
		return nil, errors.New("tool: remote MCP tool is unavailable")
	}
	requestPolicy, ok := policy.FromContext(ctx)
	if !ok || requestPolicy.Request.RequestID == "" {
		return nil, policy.ErrToolDenied
	}
	select {
	case tool.callGate <- struct{}{}:
		defer func() { <-tool.callGate }()
	case <-ctx.Done():
		return nil, errors.New("tool: remote MCP call canceled")
	}
	result, err := tool.delegate.Call(servicelog.WithRedactor(ctx, tool.redactor), args)
	if err != nil {
		return nil, errors.New("tool: remote MCP call failed")
	}
	sanitized, err := sanitizeRemoteValue(tool.redactor, result)
	if err != nil {
		return nil, errors.New("tool: remote MCP result is invalid")
	}
	wrapped := &safeRemoteResult{value: sanitized}
	compatible := false
	if provider, ok := result.(interface{ GetCallbackResult() any }); ok {
		compatible = true
		wrapped.callback, err = sanitizeRemoteValue(tool.redactor, provider.GetCallbackResult())
		if err != nil {
			return nil, errors.New("tool: remote MCP result is invalid")
		}
	} else {
		wrapped.callback = sanitized
	}
	if provider, ok := result.(interface{ GetMeta() map[string]any }); ok {
		compatible = true
		if redacted := tool.redactor.RedactValue(provider.GetMeta()); redacted != nil {
			wrapped.meta, _ = redacted.(map[string]any)
		}
	}
	if provider, ok := result.(interface{ RetryResultError() bool }); ok {
		compatible = true
		wrapped.retryError = provider.RetryResultError()
	}
	if compatible {
		return wrapped, nil
	}
	return sanitized, nil
}

func sanitizeRemoteValue(redactor *servicelog.Redactor, value any) (any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var structured any
	if err := json.Unmarshal(payload, &structured); err != nil {
		return nil, err
	}
	return redactor.RedactValue(structured), nil
}

var _ trpctool.CallableTool = (*safeRemoteTool)(nil)
var _ trpctool.MetadataProvider = (*safeRemoteTool)(nil)

type safeRemoteResult struct {
	value      any
	callback   any
	meta       map[string]any
	retryError bool
}

func (result *safeRemoteResult) MarshalJSON() ([]byte, error) { return json.Marshal(result.value) }
func (result *safeRemoteResult) GetCallbackResult() any       { return result.callback }
func (result *safeRemoteResult) GetMeta() map[string]any      { return result.meta }
func (result *safeRemoteResult) RetryResultError() bool       { return result.retryError }
