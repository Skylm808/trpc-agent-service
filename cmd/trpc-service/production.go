package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
	"github.com/liuzengh/trpc-agent-service/trpcservice/admin"
	"github.com/liuzengh/trpc-agent-service/trpcservice/audit"
	"github.com/liuzengh/trpc-agent-service/trpcservice/backend"
	"github.com/liuzengh/trpc-agent-service/trpcservice/channels"
	"github.com/liuzengh/trpc-agent-service/trpcservice/channels/feishu"
	"github.com/liuzengh/trpc-agent-service/trpcservice/channels/wecom"
	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/delivery"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway/openclaw"
	"github.com/liuzengh/trpc-agent-service/trpcservice/idempotency"
	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	servicemetrics "github.com/liuzengh/trpc-agent-service/trpcservice/metrics"
	"github.com/liuzengh/trpc-agent-service/trpcservice/repository"
	"github.com/liuzengh/trpc-agent-service/trpcservice/secret"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessioncoord"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storage"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/liuzengh/trpc-agent-service/trpcservice/worker"
	"gopkg.in/yaml.v3"
)

const (
	postgresDSNEnv  = "TRPC_AGENT_POSTGRES_DSN"
	redisURLEnv     = "TRPC_AGENT_REDIS_URL"
	adminTokensEnv  = "TRPC_AGENT_ADMIN_TOKENS"
	bindingLookupQL = `SELECT cb.tenant_id, cb.app_id, t.current_config_version
		FROM channel_bindings cb
		JOIN tenants t ON t.tenant_id = cb.tenant_id
		JOIN agent_apps a ON a.tenant_id = cb.tenant_id AND a.app_id = cb.app_id
		WHERE cb.binding_id = $1 AND cb.channel_type = $2 AND cb.enabled AND a.enabled AND t.enabled`
)

type durableComponent struct {
	inner    trpcservice.Component
	delivery *delivery.Worker
	db       *sql.DB
	redis    *backend.Redis
}

func (component *durableComponent) Start(ctx context.Context) error {
	if err := component.inner.Start(ctx); err != nil {
		return err
	}
	if component.delivery != nil {
		if err := component.delivery.Start(ctx); err != nil {
			return errors.Join(err, component.inner.Close(context.Background()))
		}
	}
	return nil
}

func (component *durableComponent) Close(ctx context.Context) error {
	innerErr := component.inner.Close(ctx)
	var deliveryErr error
	if component.delivery != nil {
		deliveryErr = component.delivery.Close(ctx)
	}
	return errors.Join(innerErr, deliveryErr, component.redis.Close(), component.db.Close())
}

func newDurableComponent(ctx context.Context, address string, file *config.File) (trpcservice.Component, error) {
	if err := validatePersistentProfiles(file); err != nil {
		return nil, err
	}
	postgresDSN := os.Getenv(postgresDSNEnv)
	redisURL := os.Getenv(redisURLEnv)
	connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	db, err := backend.OpenPostgres(connectCtx, postgresDSN)
	if err != nil {
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	closeDB := true
	defer func() {
		if closeDB {
			_ = db.Close()
		}
	}()
	bootstrapStore, err := repository.NewSQLStore(db)
	if err != nil {
		return nil, err
	}
	if err := bootstrapConfig(connectCtx, bootstrapStore, file); err != nil {
		return nil, err
	}
	redisBackend, err := backend.OpenRedis(connectCtx, redisURL)
	if err != nil {
		return nil, fmt.Errorf("connect Redis: %w", err)
	}
	closeRedis := true
	defer func() {
		if closeRedis {
			_ = redisBackend.Close()
		}
	}()

	store, err := repository.NewSQLStore(db)
	if err != nil {
		return nil, err
	}
	published, err := config.NewPublishedCache(store)
	if err != nil {
		return nil, err
	}
	routes, err := openclaw.NewSQLRoutes(db, expectedCredential(published))
	if err != nil {
		return nil, err
	}

	writes := &sessioncoord.SQLWriteStore{DB: db}
	coordinator := &sessioncoord.RedisCoordinator{Redis: redisBackend, Fencer: writes}
	factory := worker.RuntimeFactoryWithServices(writes, func(snapshot config.RuntimeSnapshot) (*storage.Services, error) {
		return storage.NewPostgres(snapshot.App().Storage, postgresDSN, db)
	})
	redactor := servicelog.NewRedactor(nil, nil)
	bus := &openclaw.RedisEventBus{Backend: redisBackend}
	workerID := nodeID()
	component, err := openclaw.NewComponent(ctx, address, file, routes, openclaw.ComponentDependencies{
		Inbox:          &idempotency.SQLStore{DB: db},
		Coordinator:    coordinator,
		Writes:         writes,
		RuntimeFactory: factory,
		Audit:          &audit.SQLStore{DB: db, Redactor: redactor},
		EventBus:       bus,
		WorkerID:       workerID,
		Snapshots:      gateway.StoreSnapshotResolver{Published: published},
	}, productionDecorators(db, store, published, redactor)...)
	if err != nil {
		return nil, err
	}
	router := &publishedDeliveryRoutes{db: db, published: published, senders: make(map[deliverySenderKey]channels.TextSender)}
	telemetry, err := servicemetrics.New("trpc-agent-service")
	if err != nil {
		_ = component.Close(context.Background())
		return nil, err
	}
	outboxWorker, err := delivery.NewWorker(
		&delivery.SQLStore{DB: db},
		router,
		&delivery.RedisFixedWindowLimiter{Redis: redisBackend},
		telemetry,
		delivery.WorkerConfig{Owner: workerID + ":outbox"},
	)
	if err != nil {
		_ = component.Close(context.Background())
		return nil, err
	}
	closeDB = false
	closeRedis = false
	return &durableComponent{inner: component, delivery: outboxWorker, db: db, redis: redisBackend}, nil
}

type deliverySenderKey struct {
	tenantID, bindingID string
	version             tenant.ConfigVersion
}

// publishedDeliveryRoutes keeps provider clients (and their token caches)
// per immutable config version while resolving every Outbox row against its
// ingress-pinned version. This lets old requests finish with their old sender
// and makes newly published channel credentials effective without a restart.
type publishedDeliveryRoutes struct {
	db        *sql.DB
	published *config.PublishedCache
	mu        sync.Mutex
	senders   map[deliverySenderKey]channels.TextSender
}

func (routes *publishedDeliveryRoutes) Keys() []delivery.BindingKey {
	if routes == nil || routes.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := routes.db.QueryContext(ctx, `SELECT tenant_id,binding_id FROM channel_bindings WHERE channel_type IN ($1,$2)`, tenant.ChannelTypeWeCom, tenant.ChannelTypeFeishu)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var keys []delivery.BindingKey
	for rows.Next() {
		var key delivery.BindingKey
		if err := rows.Scan(&key.TenantID, &key.BindingID); err != nil {
			return nil
		}
		keys = append(keys, key)
	}
	if rows.Err() != nil {
		return nil
	}
	return keys
}

func (routes *publishedDeliveryRoutes) Resolve(message gateway.OutboundMessage) (channels.TextSender, error) {
	if routes == nil || routes.published == nil || message.TenantID == "" || message.AppID == "" || message.BindingID == "" {
		return nil, errors.New("delivery: incomplete published route")
	}
	version := message.ConfigVersion
	if version == 0 {
		record, err := routes.published.Current(context.Background(), message.TenantID)
		if err != nil || len(record.Tenants) != 1 {
			return nil, errors.New("delivery: current published route is unavailable")
		}
		version = record.Tenants[0].ConfigVersion
	}
	key := deliverySenderKey{tenantID: message.TenantID, bindingID: message.BindingID, version: version}
	routes.mu.Lock()
	if sender := routes.senders[key]; sender != nil {
		routes.mu.Unlock()
		return sender, nil
	}
	routes.mu.Unlock()

	file, err := routes.published.Version(context.Background(), message.TenantID, version)
	if err != nil {
		return nil, errors.New("delivery: published route version is unavailable")
	}
	snapshot, err := file.Snapshot(message.TenantID, message.AppID)
	if err != nil {
		return nil, errors.New("delivery: published app route is unavailable")
	}
	var selected *tenant.ChannelBinding
	for _, binding := range snapshot.App().Channels {
		if binding.ID == message.BindingID && binding.Enabled && (binding.Type == tenant.ChannelTypeWeCom || binding.Type == tenant.ChannelTypeFeishu) {
			copy := binding
			selected = &copy
			break
		}
	}
	if selected == nil {
		return nil, errors.New("delivery: published channel binding is unavailable")
	}
	appSecret, err := secret.ResolveLocal(selected.Secret)
	if err != nil {
		return nil, errors.New("delivery: channel application secret is unavailable")
	}
	var sender channels.TextSender
	switch selected.Type {
	case tenant.ChannelTypeWeCom:
		agentID, err := strconv.ParseInt(selected.ProviderAppID, 10, 64)
		if err != nil || agentID <= 0 {
			return nil, errors.New("delivery: WeCom AgentID is invalid")
		}
		sender = &wecom.Sender{AgentID: agentID, Tokens: &wecom.CredentialTokenSource{CorpID: selected.ProviderAccountID, CorpSecret: appSecret}}
	case tenant.ChannelTypeFeishu:
		sender = &feishu.Sender{Tokens: &feishu.AppTokenSource{AppID: selected.ProviderAccountID, AppSecret: appSecret}}
	default:
		return nil, errors.New("delivery: channel type is unsupported")
	}
	routes.mu.Lock()
	if existing := routes.senders[key]; existing != nil {
		routes.mu.Unlock()
		return existing, nil
	}
	routes.senders[key] = sender
	routes.mu.Unlock()
	return sender, nil
}

var _ delivery.RouteResolver = (*publishedDeliveryRoutes)(nil)

// productionDecorators mounts the dynamic WeCom and Feishu callback adapters
// and the authenticated administration API around the gateway.
func productionDecorators(db *sql.DB, store repository.Store, published *config.PublishedCache, redactor *servicelog.Redactor) []openclaw.HandlerDecorator {
	wecomDecorator := func(core *openclaw.Handler, next http.Handler) (http.Handler, error) {
		adapter, err := wecom.NewDynamicHandler(core, wecomBindingProvider(db, published))
		if err != nil {
			return nil, err
		}
		mux := http.NewServeMux()
		mux.Handle("/channels/wecom/", adapter)
		mux.Handle("/", next)
		return mux, nil
	}
	feishuDecorator := func(core *openclaw.Handler, next http.Handler) (http.Handler, error) {
		adapter, err := feishu.NewDynamicHandler(core, feishuBindingProvider(db, published))
		if err != nil {
			return nil, err
		}
		mux := http.NewServeMux()
		mux.Handle("/channels/feishu/", adapter)
		mux.Handle("/", next)
		return mux, nil
	}
	adminDecorator := func(_ *openclaw.Handler, next http.Handler) (http.Handler, error) {
		credentials, err := admin.ParseCredentials(os.Getenv(adminTokensEnv))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", adminTokensEnv, err)
		}
		authenticator, err := admin.NewAuthenticator(credentials)
		if err != nil {
			return nil, err
		}
		service, err := admin.NewService(store,
			admin.WithAudit(&audit.SQLStore{DB: db, Redactor: redactor}),
			admin.WithRedactor(redactor),
			admin.WithProfileValidator(validatePersistentProfiles),
		)
		if err != nil {
			return nil, err
		}
		handler, err := admin.NewHandler(service)
		if err != nil {
			return nil, err
		}
		mux := http.NewServeMux()
		mux.Handle("/v1/tenants/", authenticator.Wrap(handler))
		mux.Handle("/", next)
		return mux, nil
	}
	return []openclaw.HandlerDecorator{wecomDecorator, feishuDecorator, adminDecorator}
}

// expectedCredential resolves the server-owned credential for one published
// binding. SecretRefs resolve at request time; the legacy per-binding
// environment variable remains as a fallback for HTTP bindings that do not
// declare a token SecretRef.
func expectedCredential(published *config.PublishedCache) openclaw.ExpectedCredential {
	return func(ctx context.Context, tenantID, bindingID string, version tenant.ConfigVersion) (string, error) {
		binding, err := publishedBinding(published, ctx, tenantID, bindingID, version)
		if err != nil {
			return "", err
		}
		if !binding.Token.IsZero() {
			return secret.ResolveLocal(binding.Token)
		}
		credential := os.Getenv(gatewayTokenEnv(bindingID))
		if credential == "" {
			return "", fmt.Errorf("no credential configured for binding %q", bindingID)
		}
		return credential, nil
	}
}

// wecomBindingProvider resolves enabled WeCom bindings from the control plane
// at callback time, so publishes, disables, and rollbacks take effect without
// interrupting in-flight messages.
func wecomBindingProvider(db *sql.DB, published *config.PublishedCache) wecom.BindingProvider {
	return func(bindingID string) (wecom.Binding, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var tenantID, appID string
		var version tenant.ConfigVersion
		err := db.QueryRowContext(ctx, bindingLookupQL+" LIMIT 1", bindingID, tenant.ChannelTypeWeCom).Scan(&tenantID, &appID, &version)
		if err != nil {
			return wecom.Binding{}, false
		}
		binding, err := publishedBinding(published, ctx, tenantID, bindingID, version)
		if err != nil || binding.Type != tenant.ChannelTypeWeCom {
			return wecom.Binding{}, false
		}
		token, err := secret.ResolveLocal(binding.Token)
		if err != nil {
			return wecom.Binding{}, false
		}
		aesKey, err := secret.ResolveLocal(binding.EncryptionKey)
		if err != nil {
			return wecom.Binding{}, false
		}
		crypt, err := wecom.NewCrypt(token, aesKey, binding.ProviderAccountID)
		if err != nil {
			return wecom.Binding{}, false
		}
		return wecom.Binding{
			TenantID: tenantID, AppID: appID, BindingID: bindingID,
			CorpID: binding.ProviderAccountID, AgentID: binding.ProviderAppID,
			ConfigVersion: version, Crypt: crypt,
		}, true
	}
}

// feishuBindingProvider resolves every enabled Feishu binding candidate for
// one binding_id from the control plane at callback time. Multiple tenants
// may declare the same binding_id; the adapter narrows encrypted callbacks
// with the server-owned Encrypt Key, then requires a unique Verification
// Token and app_id match, so cross-tenant ambiguity fails closed.
func feishuBindingProvider(db *sql.DB, published *config.PublishedCache) feishu.BindingProvider {
	return func(bindingID string) []feishu.Binding {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, err := db.QueryContext(ctx, bindingLookupQL, bindingID, tenant.ChannelTypeFeishu)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var candidates []feishu.Binding
		for rows.Next() {
			var tenantID, appID string
			var version tenant.ConfigVersion
			if err := rows.Scan(&tenantID, &appID, &version); err != nil {
				return nil
			}
			binding, err := publishedBinding(published, ctx, tenantID, bindingID, version)
			if err != nil || binding.Type != tenant.ChannelTypeFeishu {
				continue
			}
			token, err := secret.ResolveLocal(binding.Token)
			if err != nil {
				continue
			}
			candidate := feishu.Binding{
				TenantID: tenantID, AppID: appID, BindingID: bindingID,
				FeishuAppID: binding.ProviderAccountID, VerificationToken: token,
				ConfigVersion: version,
			}
			if !binding.EncryptionKey.IsZero() {
				encryptKey, err := secret.ResolveLocal(binding.EncryptionKey)
				if err != nil {
					continue
				}
				candidate.EncryptKey = encryptKey
			}
			candidates = append(candidates, candidate)
		}
		return candidates
	}
}

// publishedBinding extracts one binding from the immutable published file.
func publishedBinding(published *config.PublishedCache, ctx context.Context, tenantID, bindingID string, version tenant.ConfigVersion) (tenant.ChannelBinding, error) {
	file, err := published.Version(ctx, tenantID, version)
	if err != nil {
		return tenant.ChannelBinding{}, err
	}
	for _, currentTenant := range file.Tenants {
		if currentTenant.ID != tenantID {
			continue
		}
		for _, app := range currentTenant.Apps {
			for _, binding := range app.Channels {
				if binding.ID == bindingID {
					return binding, nil
				}
			}
		}
	}
	return tenant.ChannelBinding{}, fmt.Errorf("binding %q not found in tenant %q version %d", bindingID, tenantID, version)
}

func nodeID() string {
	if configured := os.Getenv("TRPC_AGENT_NODE_ID"); configured != "" {
		return configured
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return hostname
	}
	return "worker"
}

func migrateSchema(ctx context.Context) error {
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := backend.OpenPostgres(connectCtx, os.Getenv(postgresDSNEnv))
	if err != nil {
		return err
	}
	defer db.Close()
	return applyMigrations(connectCtx, db)
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('trpc-agent-service:migrations', 0))"); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if err := repository.Migrate(ctx, func(ctx context.Context, script string) error {
		_, err := tx.ExecContext(ctx, script)
		return err
	}, repository.DirectionUp); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func validatePersistentProfiles(file *config.File) error {
	if file == nil {
		return errors.New("persistent config is required")
	}
	for _, currentTenant := range file.Tenants {
		if !currentTenant.Enabled {
			continue
		}
		for _, app := range currentTenant.Apps {
			if app.Enabled {
				if app.Model.Provider == "mock" {
					return fmt.Errorf("tenant %q app %q: mock model is test-only", currentTenant.ID, app.ID)
				}
				if _, err := secret.ResolveLocal(app.Model.APIKey); err != nil {
					return fmt.Errorf("tenant %q app %q: model credential is unavailable", currentTenant.ID, app.ID)
				}
				if err := storage.ValidatePostgresProfile(app.Storage); err != nil {
					return fmt.Errorf("tenant %q app %q: %w", currentTenant.ID, app.ID, err)
				}
			}
		}
	}
	return nil
}

// bootstrapConfig seeds the control plane from the startup file. The database
// is the source of truth after the first boot: tenants that already have
// published versions are left untouched so configurations published through
// the Admin API survive restarts without rebuilding the environment.
func bootstrapConfig(ctx context.Context, store repository.Store, file *config.File) error {
	service, err := admin.NewService(store)
	if err != nil {
		return err
	}
	for _, configured := range file.Tenants {
		payload, err := yaml.Marshal(&config.File{SchemaVersion: file.SchemaVersion, Tenants: []tenant.Tenant{configured}})
		if err != nil {
			return fmt.Errorf("encode tenant %q bootstrap: %w", configured.ID, err)
		}
		versions, err := service.Versions(ctx, configured.ID)
		if err != nil {
			return fmt.Errorf("read tenant %q versions: %w", configured.ID, err)
		}
		if len(versions) == 0 {
			if configured.ConfigVersion != 1 {
				return fmt.Errorf("tenant %q fresh bootstrap requires config_version 1", configured.ID)
			}
			bootCtx := admin.WithActor(ctx, "bootstrap")
			if _, err := service.Publish(bootCtx, configured.ID, 0, payload); err != nil {
				return fmt.Errorf("publish tenant %q bootstrap: %w", configured.ID, err)
			}
			continue
		}
		current := versions[0]
		if configured.ConfigVersion > current.Version {
			return fmt.Errorf("tenant %q file config_version %d is ahead of published head %d", configured.ID, configured.ConfigVersion, current.Version)
		}
	}
	return nil
}
