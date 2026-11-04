package terminal

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/browser"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestLocalCLILoginBindingAndServerLogout(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	origin := "http://" + server.Listener.Addr().String()
	service, err := localauth.New(pool, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	first := "synthetic-initial-passphrase"
	password := "synthetic-working-passphrase"
	user, err := service.Initialize(ctx, "admin", first)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(ctx, service.Principal(user), first, password); err != nil {
		t.Fatal(err)
	}
	sessions, err := browser.NewLocal(pool, service, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/auth/", sessions.Handler())
	mux.Handle("/api/v1/", httpapi.NewWithSession(control.New(pool).WithLocalIdentity(service), service, sessions))
	server.Start()
	defer server.Close()
	state := localState(t)
	var out, stderr bytes.Buffer
	options := Options{In: strings.NewReader(password + "\n"), Out: &out, Err: &stderr, Getenv: func(string) string { return "" }, HTTPClient: server.Client(), Keyring: state.Keyring}
	code := Execute(ctx, options, []string{"--config", state.Path, "--server", origin, "--allow-loopback-http", "login", "--local", "--username", "admin", "--password-stdin", "--credential-store=file"})
	if code != 0 {
		t.Fatalf("local CLI login failed: %s", stderr.String())
	}
	config, _, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	credential, err := state.Credential(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), password) || strings.Contains(stderr.String(), password) || strings.Contains(out.String(), credential.AccessToken) {
		t.Fatal("CLI printed password or token")
	}
	if _, err := service.Authenticate(ctx, credential.AccessToken); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	options.In = strings.NewReader("")
	if code := Execute(ctx, options, []string{"--config", state.Path, "logout"}); code != 0 {
		t.Fatal("logout failed")
	}
	if _, err := service.Authenticate(ctx, credential.AccessToken); err == nil {
		t.Fatal("CLI logout left token active")
	}
}
