// gateway serves explicit mock or reviewed mainland-provider routes on loopback.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
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
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

type config struct {
	authMode                                      string
	database, issuer, audience, upstream, listen  string
	providerConfig                                string
	initSchema, mockOnly                          bool
	browserOrigin, clientID, clientSecret, webDir string
	allowLoopbackHTTP                             bool
}

func (c config) validate() error {
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
func run(ctx context.Context, c config) error {
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
	if err := pool.Ping(setup); err != nil {
		return errors.New("database connection failed")
	}
	ledger := budget.New(pool)
	store := control.New(pool)
	var providers provider.Routing
	if c.providerConfig != "" {
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
	if c.initSchema {
		if err := database.Migrate(setup, pool); err != nil {
			return fmt.Errorf("database migration failed: %w", err)
		}
	}
	if err := database.Check(setup, pool); err != nil {
		return fmt.Errorf("database schema is not ready: %w", err)
	}

	if !c.mockOnly && c.providerConfig == "" {
		manager := provider.NewManager(pool, os.Getenv, nil, identity.Principal{Issuer: os.Getenv("AIGW_PROVIDER_ADMIN_ISSUER"), Subject: os.Getenv("AIGW_PROVIDER_ADMIN_SUBJECT")})
		providers = manager
		store = control.NewWithCatalog(pool, manager).WithProviderManager(manager)
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
	go func() {
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
	mux.Handle("/v1/", &gateway.Handler{DeviceProof: store.VerifyDeviceRequest, Keywords: keyword.New(pool), Content: content, ListModels: store.KeyModels, Monitor: monitor, Circuits: circuit.New(pool), Admission: admission.New(pool), Attempts: execution.New(pool), Providers: providers, Ledger: ledger, Authorize: store.ResolveKey, Client: client, Upstream: c.upstream, Report: func(id, code string) { log.Printf("request_id=%s code=%s", id, code) }})
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	dbReady := readiness(pool)
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
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

	server := &http.Server{Addr: c.listen, Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	log.Printf("authenticated gateway listening on %s", c.listen)
	select {
	case err := <-failed:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("gateway listener failed")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("gateway shutdown exceeded deadline")
		}
		return nil
	}
}
func main() {
	c := config{database: os.Getenv("AIGW_DATABASE_URL"), issuer: os.Getenv("AIGW_OIDC_ISSUER"), audience: os.Getenv("AIGW_OIDC_AUDIENCE"), upstream: os.Getenv("AIGW_UPSTREAM_URL")}
	c.browserOrigin = os.Getenv("AIGW_BROWSER_ORIGIN")
	c.clientID = os.Getenv("AIGW_OIDC_CLIENT_ID")
	c.clientSecret = os.Getenv("AIGW_OIDC_CLIENT_SECRET")
	flag.StringVar(&c.authMode, "auth-mode", os.Getenv("AIGW_AUTH_MODE"), "oidc (default) or explicit local offline identity")
	flag.StringVar(&c.listen, "listen", "127.0.0.1:8080", "development loopback listener")
	flag.BoolVar(&c.initSchema, "migrate", false, "apply embedded database migrations before development startup")
	flag.BoolVar(&c.initSchema, "init-schema", false, "deprecated alias for --migrate; includes legacy schema validation")
	flag.BoolVar(&c.mockOnly, "mock-only", false, "test-only mock service; additionally requires AIGW_TEST_MODE=1")
	flag.StringVar(&c.providerConfig, "providers", "", "explicit mainland provider configuration file; mutually exclusive with --mock-only")
	flag.StringVar(&c.webDir, "web-dir", "web/dist", "built console assets directory")
	flag.BoolVar(&c.allowLoopbackHTTP, "allow-loopback-browser-http", false, "allow non-secure browser cookies only on an explicit loopback HTTP origin")
	flag.Parse()
	if c.mockOnly && os.Getenv("AIGW_TEST_MODE") != "1" {
		log.Print("--mock-only is restricted to explicit tests (AIGW_TEST_MODE=1); normal startup uses managed real providers")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, c); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
