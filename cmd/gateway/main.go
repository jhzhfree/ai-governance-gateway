// gateway serves the authenticated mock-model development slice on loopback.
package main

import (
	"context"
	"errors"
	"flag"
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
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type config struct {
	database, issuer, audience, upstream, listen string
	initSchema, mockOnly                         bool
}

func (c config) validate() error {
	if !c.mockOnly {
		return errors.New("only --mock-only is supported in this development stage")
	}
	if c.database == "" || c.issuer == "" || c.audience == "" || c.upstream == "" {
		return errors.New("AIGW_DATABASE_URL, AIGW_OIDC_ISSUER, AIGW_OIDC_AUDIENCE and AIGW_UPSTREAM_URL are required")
	}
	u, err := url.Parse(c.issuer)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("OIDC issuer must be a credential-free HTTPS URL")
	}
	u, err = url.Parse(c.upstream)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/chat/completions" || !net.ParseIP(u.Hostname()).IsLoopback() {
		return errors.New("mock upstream must be a loopback HTTP /v1/chat/completions URL")
	}
	host, _, err := net.SplitHostPort(c.listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("development listener must use a loopback IP address")
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
	if c.initSchema {
		if err := ledger.Init(setup); err != nil {
			return errors.New("budget schema initialization failed")
		}
		if err := store.Init(setup); err != nil {
			return errors.New("management schema initialization failed")
		}
	}
	client := &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	auth, err := identity.NewOIDC(oidc.ClientContext(setup, client), c.issuer, c.audience)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", httpapi.New(store, auth))
	mux.Handle("/v1/", &gateway.Handler{Ledger: ledger, Authorize: store.ResolveKey, Client: client, Upstream: c.upstream, Report: func(id, code string) { log.Printf("request_id=%s code=%s", id, code) }})
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		check, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		var ready bool
		err := pool.QueryRow(check, `SELECT to_regclass('budget_reservations') IS NOT NULL AND to_regclass('project_keys') IS NOT NULL`).Scan(&ready)
		if err != nil || !ready {
			http.Error(w, "not ready", 503)
			return
		}
		w.WriteHeader(204)
	})
	server := &http.Server{Addr: c.listen, Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	log.Printf("authenticated mock gateway listening on %s", c.listen)
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
	flag.StringVar(&c.listen, "listen", "127.0.0.1:8080", "development loopback listener")
	flag.BoolVar(&c.initSchema, "init-schema", false, "initialize prototype schema in a dedicated development database")
	flag.BoolVar(&c.mockOnly, "mock-only", false, "explicitly enable the mock-only development service")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, c); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
