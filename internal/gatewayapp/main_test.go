package gatewayapp

import (
	"context"
	"strings"
	"testing"
)

func TestEmbeddedEntrypointCannotSelectLegacyEngine(t *testing.T) {
	for _, args := range [][]string{nil, {"--execution-engine=bifrost"}} {
		cfg, err := commandConfig(args, true)
		if err != nil || cfg.executionEngine != "bifrost" || cfg.schemaMode != "auto" {
			t.Fatalf("embedded defaults changed: engine=%s schema=%s err=%v", cfg.executionEngine, cfg.schemaMode, err)
		}
	}
	// Deliberately omit the database: these must fail at the product boundary,
	// before any attempt to initialize schema, identity or external services.
	for _, engine := range []string{"native", "newapi", "", "unknown"} {
		err := RunEmbedded(context.Background(), []string{"--execution-engine=" + engine}, nil)
		if err == nil || !strings.Contains(err.Error(), "single-process product requires") {
			t.Fatalf("legacy selection not rejected before setup: %q: %v", engine, err)
		}
	}
	legacy, err := commandConfig(nil, false)
	if err != nil || legacy.executionEngine != "native" {
		t.Fatalf("historical entrypoint changed: %v", err)
	}
}

func TestDevelopmentConfigFailsClosed(t *testing.T) {
	valid := config{database: "postgres://unused", issuer: "https://idp.example/realm", audience: "aigw-management", upstream: "http://127.0.0.1:8091/v1/chat/completions", listen: "127.0.0.1:8080", mockOnly: true}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*config){
		func(c *config) { c.database = "" },
		func(c *config) { c.issuer = "http://idp.example" }, func(c *config) { c.issuer = "https://user:password@idp.example" },
		func(c *config) { c.listen = "0.0.0.0:8080" }, func(c *config) { c.upstream = "http://169.254.169.254/v1/chat/completions" },
		func(c *config) { c.upstream = "http://localhost:8091/v1/chat/completions" }, func(c *config) { c.upstream = "http://127.0.0.1:8091/other" },
	} {
		c := valid
		edit(&c)
		if c.validate() == nil {
			t.Fatal("unsafe development configuration accepted")
		}
	}
}

func TestMainlandModeRequiresExplicitMutuallyExclusiveConfiguration(t *testing.T) {
	c := config{database: "postgres://unused", issuer: "https://idp.example/realm", audience: "aigw-management", listen: "127.0.0.1:8080", providerConfig: "reviewed-mainland.json"}
	if err := c.validate(); err != nil {
		t.Fatal("explicit real provider mode was rejected")
	}
	c.mockOnly = true
	if c.validate() == nil {
		t.Fatal("mock/real ambiguous mode accepted")
	}
}

func TestLocalModeDoesNotRequireExternalOIDC(t *testing.T) {
	c := config{database: "postgres://unused", listen: "127.0.0.1:8080", authMode: "local", browserOrigin: "https://gateway.example"}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	c.browserOrigin = ""
	if c.validate() == nil {
		t.Fatal("local auth needs an explicit origin")
	}
}
