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
	"github.com/jhzhfree/ai-governance-gateway/internal/console"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

type config struct {
	database, issuer, audience, upstream, listen  string
	providerConfig                                string
	initSchema, mockOnly                          bool
	browserOrigin, clientID, clientSecret, webDir string
	allowLoopbackHTTP                             bool
}

func (c config) validate() error {
	if c.mockOnly == (c.providerConfig != "") {
		return errors.New("choose exactly one of --mock-only or --providers configuration")
	}
	if c.database == "" || c.issuer == "" || c.audience == "" || (c.mockOnly && c.upstream == "") {
		return errors.New("AIGW_DATABASE_URL, AIGW_OIDC_ISSUER, AIGW_OIDC_AUDIENCE and AIGW_UPSTREAM_URL are required")
	}
	u, err := url.Parse(c.issuer)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
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
	if c.browserOrigin != "" && c.clientID == "" {
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
	var providers *provider.Catalog
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

	client := &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	auth, err := identity.NewOIDC(oidc.ClientContext(setup, client), c.issuer, c.audience)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	var sessions *browser.Service
	if c.browserOrigin != "" {
		sessions, err = browser.New(setup, pool, auth, c.issuer, browser.Config{Origin: c.browserOrigin, ClientID: c.clientID, ClientSecret: c.clientSecret, AllowLoopbackHTTP: c.allowLoopbackHTTP, HTTPClient: client})
		if err != nil {
			return err
		}
		assets, e := console.New(c.webDir)
		if e != nil {
			return e
		}
		mux.Handle("/", assets)
		mux.Handle("/auth/", sessions.Handler())
		mux.Handle("/api/v1/", httpapi.NewWithSession(store, auth, sessions))
	} else {
		mux.Handle("/api/v1/", httpapi.New(store, auth))
	}
	mux.Handle("/v1/", &gateway.Handler{Admission: admission.New(pool), Attempts: execution.New(pool), Providers: providers, Ledger: ledger, Authorize: store.ResolveKey, Client: client, Upstream: c.upstream, Report: func(id, code string) { log.Printf("request_id=%s code=%s", id, code) }})
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	dbReady := readiness(pool)
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if providers != nil && !providers.Ready() {
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
	flag.StringVar(&c.listen, "listen", "127.0.0.1:8080", "development loopback listener")
	flag.BoolVar(&c.initSchema, "migrate", false, "apply embedded database migrations before development startup")
	flag.BoolVar(&c.initSchema, "init-schema", false, "deprecated alias for --migrate; includes legacy schema validation")
	flag.BoolVar(&c.mockOnly, "mock-only", false, "explicitly enable the mock-only development service")
	flag.StringVar(&c.providerConfig, "providers", "", "explicit mainland provider configuration file; mutually exclusive with --mock-only")
	flag.StringVar(&c.webDir, "web-dir", "web/dist", "built console assets directory")
	flag.BoolVar(&c.allowLoopbackHTTP, "allow-loopback-browser-http", false, "allow non-secure browser cookies only on an explicit loopback HTTP origin")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, c); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
