package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestOperationalCommandsWithoutOIDC(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx := context.Background()
	var out bytes.Buffer
	if err := execute(ctx, pool, "status", &out); err != nil {
		t.Fatal(err)
	}
	var report database.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Managed {
		t.Fatal("invalid unmanaged status output")
	}
	out.Reset()
	if err := execute(ctx, pool, "check", &out); !errors.Is(err, database.ErrUnmanaged) {
		t.Fatalf("check=%v", err)
	}
	if out.Len() != 0 {
		t.Fatal("failed check emitted successful status")
	}
	if err := execute(ctx, pool, "up", &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.Ready {
		t.Fatal("up did not output ready report")
	}
	out.Reset()
	if err := execute(ctx, pool, "check", &out); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidCLIArgumentsFailBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{nil, {"down"}, {"force"}, {"--timeout=0", "up"}, {"--timeout=invalid", "up"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid CLI arguments accepted")
		}
	}
}

func TestCLIEntrypointUsesOnlyDatabaseConfiguration(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	dsn := os.Getenv("TEST_DATABASE_URL")
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	if parsed, err := url.Parse(dsn); err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		// The fixture schema is generated from base32 and quoted as an identifier;
		// it contains no single quotes or backslashes. Preserve the original DSN,
		// including its TLS/auth settings, and override only the test search path.
		dsn += " search_path='" + schema + "'"
	}
	t.Setenv("AIGW_DATABASE_URL", dsn)
	t.Setenv("AIGW_OIDC_ISSUER", "")
	t.Setenv("AIGW_OIDC_AUDIENCE", "")
	var out bytes.Buffer
	for _, command := range []string{"status", "up", "check"} {
		out.Reset()
		if err := run(context.Background(), []string{"--timeout=15s", command}, &out); err != nil {
			t.Fatalf("CLI %s failed: %v", command, err)
		}
		var report database.Report
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal("CLI did not emit status JSON")
		}
		if command != "status" && !report.Ready {
			t.Fatal("CLI did not migrate isolated schema")
		}
	}
	if err := database.Check(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
}
