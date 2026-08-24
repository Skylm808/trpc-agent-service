package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
	"github.com/liuzengh/trpc-agent-service/trpcservice/channels/wecom"
	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway/openclaw"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("trpc-service", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	showVersion := flags.Bool("version", false, "print version and exit")
	configPath := flags.String("config", "", "validated tenant config for the local gateway")
	listenAddress := flags.String("listen", "127.0.0.1:8080", "local OpenClaw HTTP listen address")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if *showVersion {
		fmt.Printf("trpc-agent-service %s\n", trpcservice.Version)
		return 0
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	var options []trpcservice.Option
	if *configPath != "" {
		component, err := localGateway(ctx, *configPath, *listenAddress)
		if err != nil {
			fmt.Fprintf(os.Stderr, "initialize local gateway: %v\n", err)
			return 1
		}
		options = append(options, trpcservice.WithComponents(component))
	}
	app, err := trpcservice.NewApp(options...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize service: %v\n", err)
		return 1
	}

	fmt.Printf("trpc-agent-service %s\n", trpcservice.Version)
	if err := app.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "service stopped with error: %v\n", err)
		return 1
	}
	return 0
}

func localGateway(ctx context.Context, path, address string) (*openclaw.LocalComponent, error) {
	fileHandle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fileHandle.Close()
	file, err := config.Load(fileHandle)
	if err != nil {
		return nil, err
	}
	var bindings []openclaw.Route
	var weComBindings []wecom.Binding
	for _, currentTenant := range file.Tenants {
		if !currentTenant.Enabled {
			continue
		}
		for _, app := range currentTenant.Apps {
			if !app.Enabled {
				continue
			}
			for _, binding := range app.Channels {
				if !binding.Enabled {
					continue
				}
				switch binding.Type {
				case tenant.ChannelTypeHTTP:
					envName := gatewayTokenEnv(binding.ID)
					credential := os.Getenv(envName)
					if credential == "" {
						return nil, fmt.Errorf("enabled HTTP binding %q requires environment variable %s", binding.ID, envName)
					}
					bindings = append(bindings, openclaw.Route{TenantID: currentTenant.ID, AppID: app.ID, BindingID: binding.ID, ChannelType: binding.Type, ConfigVersion: currentTenant.ConfigVersion, Credential: credential})
				case tenant.ChannelTypeWeCom:
					token, err := resolveLocalSecret(binding.Token)
					if err != nil {
						return nil, fmt.Errorf("resolve WeCom callback token for binding %q: %w", binding.ID, err)
					}
					aesKey, err := resolveLocalSecret(binding.EncryptionKey)
					if err != nil {
						return nil, fmt.Errorf("resolve WeCom encryption key for binding %q: %w", binding.ID, err)
					}
					crypt, err := wecom.NewCrypt(token, aesKey, binding.ProviderAccountID)
					if err != nil {
						return nil, fmt.Errorf("initialize WeCom binding %q: %w", binding.ID, err)
					}
					weComBindings = append(weComBindings, wecom.Binding{TenantID: currentTenant.ID, AppID: app.ID, BindingID: binding.ID, CorpID: binding.ProviderAccountID, AgentID: binding.ProviderAppID, ConfigVersion: currentTenant.ConfigVersion, Crypt: crypt})
				}
			}
		}
	}
	if len(bindings) == 0 && len(weComBindings) == 0 {
		return nil, errors.New("config has no enabled local channel bindings")
	}
	routes, err := openclaw.NewStaticRoutes(bindings...)
	if err != nil {
		return nil, err
	}
	var decorators []openclaw.HandlerDecorator
	if len(weComBindings) > 0 {
		decorators = append(decorators, func(core *openclaw.Handler, next http.Handler) (http.Handler, error) {
			adapter, err := wecom.NewHandler(core, weComBindings...)
			if err != nil {
				return nil, err
			}
			mux := http.NewServeMux()
			mux.Handle("/channels/wecom/", adapter)
			mux.Handle("/", next)
			return mux, nil
		})
	}
	return openclaw.NewLocalComponent(ctx, address, file, routes, decorators...)
}

func resolveLocalSecret(ref tenant.SecretRef) (string, error) {
	var value string
	switch ref.Provider {
	case tenant.SecretProviderEnv:
		value = os.Getenv(ref.Key)
	case tenant.SecretProviderFile:
		content, err := os.ReadFile(ref.Key)
		if err != nil {
			return "", errors.New("read local secret file")
		}
		value = strings.TrimRight(string(content), "\r\n")
	default:
		return "", fmt.Errorf("local secret provider %q is unsupported", ref.Provider)
	}
	if value == "" {
		return "", errors.New("resolved local secret is empty")
	}
	return value, nil
}

func gatewayTokenEnv(bindingID string) string {
	var name strings.Builder
	name.WriteString("TRPC_AGENT_GATEWAY_TOKEN_")
	for _, char := range bindingID {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			name.WriteRune(unicode.ToUpper(char))
		} else {
			name.WriteByte('_')
		}
	}
	return name.String()
}
