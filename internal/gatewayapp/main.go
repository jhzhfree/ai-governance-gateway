// gateway serves explicit mock or reviewed mainland-provider routes on loopback.
package gatewayapp

import (
	"context"
	"errors"
	"flag"
	"github.com/jackc/pgx/v5"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/browser"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/console"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/systemhost"
)

// RuntimeEngine must supply routing and physical execution as one owned object.
type RuntimeEngine interface {
	provider.Routing
	gateway.ProviderExecutor
	ModelsIn(context.Context, pgx.Tx) []provider.ModelInfo
	Close()
}
type EngineConfig struct{ MaxBindings, MaxConcurrent int }
type EngineFactory func(context.Context, *provider.Manager, EngineConfig) (RuntimeEngine, error)

type config struct {
	executionEngine                               string
	engineLimits                                  EngineConfig
	authMode                                      string
	database, issuer, audience, upstream, listen  string
	providerConfig                                string
	initSchema, mockOnly                          bool
	schemaMode                                    string
	schemaTimeout                                 time.Duration
	browserOrigin, clientID, clientSecret, webDir string
	allowLoopbackHTTP                             bool
}

func (c config) validate() error {
	if c.executionEngine != "" && c.executionEngine != "native" && c.executionEngine != "bifrost" && c.executionEngine != "newapi" {
		return errors.New("execution engine must be native, bifrost or newapi")
	}
	if c.executionEngine == "bifrost" && (c.mockOnly || c.providerConfig != "" || c.engineLimits.MaxBindings < 1 || c.engineLimits.MaxBindings > 256 || c.engineLimits.MaxConcurrent < 1 || c.engineLimits.MaxConcurrent > 1024) {
		return errors.New("bifrost requires managed providers and valid execution pool limits")
	}

	if c.executionEngine == "newapi" && (c.mockOnly || c.engineLimits.MaxConcurrent < 1 || c.engineLimits.MaxConcurrent > 1024) {
		return errors.New("newapi requires a valid concurrency limit and non-mock execution")
	}

	if c.schemaMode != "" && c.schemaMode != "auto" && c.schemaMode != "check" {
		return errors.New("schema mode must be auto or check")
	}
	if c.schemaTimeout < 0 || (c.schemaMode == "check" && c.initSchema) {
		return errors.New("invalid schema timeout or conflicting migration flags")
	}
	if c.mockOnly && c.providerConfig != "" {
		return errors.New("test mock mode and real provider configuration are mutually exclusive")
	}
	if c.authMode != "" && c.authMode != "oidc" && c.authMode != "local" {
		return errors.New("auth mode must be oidc or local")
	}
	if c.database == "" || (c.authMode == "local" && c.browserOrigin == "") || (c.authMode != "local" && (c.issuer == "" || c.audience == "")) || (c.mockOnly && c.upstream == "") {
		return errors.New("database, HTTPS OIDC issuer and management audience are required; mock tests additionally require a loopback upstream")
	}
	u, err := url.Parse(c.issuer)
	if c.authMode != "local" && (err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "") {
		return errors.New("OIDC issuer must be a credential-free HTTPS URL")
	}
	if c.mockOnly {
		u, err = url.Parse(c.upstream)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/chat/completions" || !net.ParseIP(u.Hostname()).IsLoopback() {
			return errors.New("mock upstream must be a loopback HTTP /v1/chat/completions URL")
		}
	}
	host, _, err := net.SplitHostPort(c.listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("development listener must use a loopback IP address")
	}
	if c.authMode != "local" && c.browserOrigin != "" && c.clientID == "" {
		return errors.New("AIGW_OIDC_CLIENT_ID required for browser login")
	}
	return nil
}
func run(ctx context.Context, c config, factory EngineFactory, listener net.Listener) error {
	if c.executionEngine == "bifrost" && factory == nil {
		return errors.New("bifrost is unavailable in this binary; build the engine gateway entrypoint")
	}
	ctx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	// Keep inference resources alive during the HTTP graceful shutdown window.
	resources, cancelResources := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelResources()
	var selected RuntimeEngine

	if err := c.validate(); err != nil {
		return err
	}
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(setup, c.database)
	if err != nil {
		return errors.New("database configuration failed")
	}
	defer pool.Close()
	defer cancelResources()
	if err := pool.Ping(setup); err != nil {
		return errors.New("database connection failed")
	}
	cancel()
	if err := prepareSchema(ctx, pool, c); err != nil {
		return err
	}
	// Identity discovery gets its own budget; a successful long migration must
	// not leave setup using the expired connection/migration deadline.
	setup, cancel = context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ledger := budget.New(pool)
	store := control.New(pool)
	var providers provider.Routing
	if c.executionEngine == "newapi" {
		var file *provider.Config
		if c.providerConfig != "" {
			cfg, err := provider.ReadConfig(c.providerConfig)
			if err != nil {
				return err
			}
			file = &cfg
		}
		adminToken := os.Getenv("AIGW_NEWAPI_ADMIN_TOKEN")
		if adminToken != "" && adminToken == os.Getenv("AIGW_NEWAPI_SERVICE_TOKEN") {
			return errors.New("new-api administration and execution require separate credentials")
		}
		manager, err := newapibridge.NewChannelManager(pool, os.Getenv("AIGW_NEWAPI_ORIGIN"), adminToken, identity.Principal{Issuer: os.Getenv("AIGW_PROVIDER_ADMIN_ISSUER"), Subject: os.Getenv("AIGW_PROVIDER_ADMIN_SUBJECT")}, os.Getenv("AIGW_NEWAPI_ALLOW_LOOPBACK_HTTP") == "true", file)
		if err != nil {
			return err
		}
		defer manager.Close()
		cfg := newapibridge.Config{Origin: os.Getenv("AIGW_NEWAPI_ORIGIN"), ServiceToken: os.Getenv("AIGW_NEWAPI_SERVICE_TOKEN"), MaxConcurrent: c.engineLimits.MaxConcurrent, AllowLoopbackHTTP: os.Getenv("AIGW_NEWAPI_ALLOW_LOOPBACK_HTTP") == "true", Journal: newapibridge.NewJournal(pool)}
		if file != nil {
			directory, err := provider.NewGoverned(*file)
			if err != nil {
				return err
			}
			selected, err = newapibridge.NewRuntime(resources, cfg, directory)
		} else {
			selected, err = newapibridge.NewManagedRuntime(resources, cfg, manager)
		}
		if err != nil {
			return err
		}
		defer func() { cancelResources(); selected.Close() }()
		providers = selected
		store = control.NewWithCatalog(pool, providers).WithProviderManager(manager).WithNewAPIChannels(manager)
	} else if c.providerConfig != "" {
		cfg, err := provider.ReadConfig(c.providerConfig)
		if err != nil {
			return err
		}
		providers, err = provider.New(cfg, os.Getenv, nil)
		if err != nil {
			return err
		}
		store = control.NewWithCatalog(pool, providers)
	}
	if c.executionEngine != "newapi" && !c.mockOnly && c.providerConfig == "" {
		manager := provider.NewManager(pool, os.Getenv, nil, identity.Principal{Issuer: os.Getenv("AIGW_PROVIDER_ADMIN_ISSUER"), Subject: os.Getenv("AIGW_PROVIDER_ADMIN_SUBJECT")})
		var management control.ProviderManagement = manager
		providers = manager
		if c.executionEngine == "bifrost" {
			selected, err = factory(resources, manager, c.engineLimits)
			if err != nil {
				if selected != nil {
					selected.Close()
				}
				return errors.New("execution engine initialization failed")
			}
			if selected == nil {
				return errors.New("execution engine initialization returned no engine")
			}
			defer func() { cancelResources(); selected.Close() }()
			providers = selected
			management = embeddedProviders{ProviderManagement: manager, engine: selected}
		}
		store = control.NewWithCatalog(pool, providers).WithProviderManager(management)
	}

	client := &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	var auth identity.Authenticator
	var oidcAuth *identity.OIDC
	var localIdentity *localauth.Service
	if c.authMode == "local" {
		localIdentity, err = localauth.New(pool, c.browserOrigin, c.allowLoopbackHTTP)
		auth = localIdentity
	} else {
		oidcAuth, err = identity.NewOIDC(oidc.ClientContext(setup, client), c.issuer, c.audience)
		auth = oidcAuth
	}
	if err != nil {
		return err
	}
	ring, err := contentaudit.ParseKeys(os.Getenv("AIGW_CONTENT_AUDIT_KEYS"), os.Getenv("AIGW_CONTENT_AUDIT_ACTIVE_KEY"))
	if err != nil {
		return errors.New("content audit key configuration invalid")
	}
	content := contentaudit.New(pool, ring)
	store.WithContentAudit(content)
	if localIdentity != nil {
		store.WithLocalIdentity(localIdentity)
	}

	systemAdmin := identity.Principal{Issuer: os.Getenv("AIGW_SYSTEM_ADMIN_ISSUER"), Subject: os.Getenv("AIGW_SYSTEM_ADMIN_SUBJECT")}
	if systemAdmin.Issuer != "" || systemAdmin.Subject != "" {
		node := os.Getenv("AIGW_SYSTEM_NODE")
		if systemAdmin.Issuer == "" || systemAdmin.Subject == "" || node == "" || len(node) > 128 {
			return errors.New("complete system administrator identity and node name required")
		}
		var host systemhost.Agent = &systemhost.ReadOnly{Monitor: systemhost.NewMonitor(), Node: node}
		if socket := os.Getenv("AIGW_HOST_AGENT_SOCKET"); socket != "" {
			agent, e := systemhost.NewClient(socket)
			if e != nil {
				return e
			}
			defer agent.Close()
			host = agent
		}
		store.WithSystem(host, systemAdmin, node)
	}

	monitor := observe.New()
	metrics, err := monitor.MetricsHandler(os.Getenv("AIGW_METRICS_TOKEN"))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics)
	var sessions *browser.Service
	if c.browserOrigin != "" {
		if localIdentity != nil {
			sessions, err = browser.NewLocal(pool, localIdentity, c.browserOrigin, c.allowLoopbackHTTP)
		} else {
			sessions, err = browser.New(setup, pool, oidcAuth, c.issuer, browser.Config{Origin: c.browserOrigin, ClientID: c.clientID, ClientSecret: c.clientSecret, AllowLoopbackHTTP: c.allowLoopbackHTTP, HTTPClient: client})
		}
		if err != nil {
			return err
		}
		assets, e := console.New(c.webDir)
		if e != nil {
			return e
		}
		mux.Handle("/", assets)
		mux.Handle("/auth/", sessions.Handler())
		mux.Handle("/api/v1/", httpapi.NewWithRuntime(store, auth, sessions, monitor))
	} else {
		mux.Handle("/api/v1/", httpapi.NewWithRuntime(store, auth, nil, monitor))
	}
	maintenanceDone := make(chan struct{})
	defer func() { stopWorkers(); <-maintenanceDone }()
	go func() {
		defer close(maintenanceDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := store.PruneDeviceProofs(jobCtx); err != nil && ctx.Err() == nil {
				log.Print("device proof maintenance unavailable")
			}
			if err := store.RotateDueBudgetPeriods(jobCtx); err != nil && ctx.Err() == nil {
				log.Print("budget cycle maintenance unavailable")
			}
			if localIdentity != nil {
				if err := localIdentity.Maintenance(jobCtx); err != nil && ctx.Err() == nil {
					log.Print("local identity maintenance unavailable")
				}
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	store.WithDeviceOrigin(c.browserOrigin)
	mux.Handle("/v1/", &gateway.Handler{Executor: selected, DeviceProof: store.VerifyDeviceRequest, Keywords: keyword.New(pool), Content: content, ListModels: store.KeyModels, Monitor: monitor, Circuits: circuit.New(pool), Admission: admission.New(pool), Attempts: execution.New(pool), Providers: providers, Ledger: ledger, Authorize: store.ResolveKey, Client: client, Upstream: c.upstream, Report: func(id, code string) { log.Printf("request_id=%s code=%s", id, code) }})
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	dbReady := readiness(pool)
	var stopping atomic.Bool
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if stopping.Load() {
			w.WriteHeader(503)
			return
		}
		if providers != nil && !providers.Ready() {
			w.WriteHeader(503)
			return
		}
		auditCtx, auditCancel := context.WithTimeout(r.Context(), 2*time.Second)
		auditReady := content.Ready(auditCtx)
		auditCancel()
		if !auditReady {
			w.WriteHeader(503)
			return
		}
		deviceCtx, deviceCancel := context.WithTimeout(r.Context(), 2*time.Second)
		deviceReady := store.DeviceAdmissionReady(deviceCtx)
		deviceCancel()
		if !deviceReady {
			w.WriteHeader(503)
			return
		}
		dbReady(w, r)
	})

	gate := newRequestGate(mux)
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return resources }, Addr: c.listen, Handler: gate, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	failed := make(chan error, 1)
	go func() {
		if listener != nil {
			failed <- server.Serve(listener)
		} else {
			failed <- server.ListenAndServe()
		}
	}()
	log.Printf("authenticated gateway listening on %s", c.listen)
	select {
	case err := <-failed:
		stopping.Store(true)
		cleanupErr := shutdownServer(server, gate, cancelResources, 10*time.Second, 15*time.Second)
		if cleanupErr != nil {
			return cleanupErr
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("gateway listener failed")
	case <-ctx.Done():
		stopping.Store(true)
		return shutdownServer(server, gate, cancelResources, 10*time.Second, 15*time.Second)
	}
}

// Run preserves the historical native/bridge entrypoint for migration tests.
func Run(ctx context.Context, args []string, factory EngineFactory) error {
	return runCommand(ctx, args, factory, false)
}

// RunEmbedded selects the in-process engine exclusively. An explicit legacy
// engine flag must fail rather than silently changing the product's data plane.
func RunEmbedded(ctx context.Context, args []string, factory EngineFactory) error {
	return runCommand(ctx, args, factory, true)
}

func runCommand(ctx context.Context, args []string, factory EngineFactory, embedded bool) error {
	c, err := commandConfig(args, embedded)
	if err != nil {
		return err
	}
	return run(ctx, c, factory, nil)
}

func commandConfig(args []string, embedded bool) (config, error) {
	flags := flag.NewFlagSet("gateway", flag.ContinueOnError)
	c := config{database: os.Getenv("AIGW_DATABASE_URL"), issuer: os.Getenv("AIGW_OIDC_ISSUER"), audience: os.Getenv("AIGW_OIDC_AUDIENCE"), upstream: os.Getenv("AIGW_UPSTREAM_URL")}
	c.browserOrigin = os.Getenv("AIGW_BROWSER_ORIGIN")
	c.clientID = os.Getenv("AIGW_OIDC_CLIENT_ID")
	c.clientSecret = os.Getenv("AIGW_OIDC_CLIENT_SECRET")
	flags.StringVar(&c.authMode, "auth-mode", os.Getenv("AIGW_AUTH_MODE"), "oidc (default) or explicit local offline identity")
	flags.StringVar(&c.listen, "listen", "127.0.0.1:8080", "development loopback listener")
	flags.StringVar(&c.schemaMode, "schema-mode", "auto", "auto: upgrade embedded schema before serving; check: require pre-migrated schema")
	flags.DurationVar(&c.schemaTimeout, "schema-timeout", 5*time.Minute, "database upgrade/check timeout including migration lock wait")
	flags.BoolVar(&c.initSchema, "migrate", false, "deprecated: auto migration is now the default; use --schema-mode=check to disable")
	flags.BoolVar(&c.initSchema, "init-schema", false, "deprecated alias for --migrate; includes legacy schema validation")
	flags.BoolVar(&c.mockOnly, "mock-only", false, "test-only mock service; additionally requires AIGW_TEST_MODE=1")
	flags.StringVar(&c.providerConfig, "providers", "", "explicit mainland provider configuration file; mutually exclusive with --mock-only")
	flags.StringVar(&c.webDir, "web-dir", "web/dist", "built console assets directory")
	flags.BoolVar(&c.allowLoopbackHTTP, "allow-loopback-browser-http", false, "allow non-secure browser cookies only on an explicit loopback HTTP origin")
	defaultEngine, engineHelp := "native", "native (default), newapi, or bifrost with an engine factory"
	if embedded {
		defaultEngine, engineHelp = "bifrost", "bifrost only: in-process execution, no legacy engine fallback"
	}
	flags.StringVar(&c.executionEngine, "execution-engine", defaultEngine, engineHelp)
	flags.IntVar(&c.engineLimits.MaxBindings, "engine-max-bindings", 64, "total current, draining and building execution instances")
	flags.IntVar(&c.engineLimits.MaxConcurrent, "engine-max-concurrent", 32, "global execution concurrency including response bodies")
	if err := flags.Parse(args); err != nil {
		return c, err
	}
	if flags.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	if embedded && c.executionEngine != "bifrost" {
		return c, errors.New("single-process product requires --execution-engine=bifrost; legacy engines are not supported by this entrypoint")
	}
	if c.mockOnly && os.Getenv("AIGW_TEST_MODE") != "1" {
		return c, errors.New("--mock-only requires explicit AIGW_TEST_MODE=1")
	}
	return c, nil
}

// Main owns process signals for the historical entrypoint.
func Main(factory EngineFactory) {
	mainCommand(factory, Run)
}

// MainEmbedded is the single-process product entrypoint.
func MainEmbedded(factory EngineFactory) {
	mainCommand(factory, RunEmbedded)
}

func mainCommand(factory EngineFactory, runCommand func(context.Context, []string, EngineFactory) error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runCommand(ctx, os.Args[1:], factory); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Print(err)
		os.Exit(1)
	}
}
