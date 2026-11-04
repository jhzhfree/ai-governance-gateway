package browser_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/browser"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestLocalBrowserForcedChangeCSRFAndAccountManagement(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	origin := "http://" + server.Listener.Addr().String()
	identity, err := localauth.New(pool, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	initial := "synthetic-initial-passphrase"
	updated := "synthetic-updated-passphrase"
	if _, err := identity.Initialize(ctx, "admin", initial); err != nil {
		t.Fatal(err)
	}
	sessions, err := browser.NewLocal(pool, identity, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/auth/", sessions.Handler())
	mux.Handle("/api/v1/", httpapi.NewWithSession(control.New(pool).WithLocalIdentity(identity), identity, sessions))
	server.Start()
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := server.Client()
	client.Jar = jar
	request := func(method, path string, body any, csrf, originHeader string, want int) map[string]any {
		t.Helper()
		data, _ := json.Marshal(body)
		r, _ := http.NewRequest(method, origin+path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		if originHeader != "" {
			r.Header.Set("Origin", originHeader)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s got %d want %d", method, path, resp.StatusCode, want)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("local identity response cacheable")
		}
		var out map[string]any
		if want != 204 {
			_ = json.NewDecoder(resp.Body).Decode(&out)
		}
		return out
	}
	request("POST", "/auth/local/login", map[string]string{"username": "admin", "password": initial}, "", "https://untrusted.example", 403)
	request("POST", "/auth/local/login", map[string]string{"username": "admin", "password": initial}, "", origin, 204)
	session := request("GET", "/auth/session", nil, "", "", 200)
	if session["must_change_password"] != true {
		t.Fatal("first password not marked for change")
	}
	csrf := session["csrf_token"].(string)
	request("GET", "/api/v1/organizations", nil, "", "", 403)
	request("POST", "/auth/local/password", map[string]string{"current_password": initial, "new_password": updated}, "", origin, 403)
	request("POST", "/auth/local/password", map[string]string{"current_password": initial, "new_password": updated}, csrf, origin, 204)
	request("GET", "/auth/session", nil, "", "", 401)
	request("POST", "/auth/local/login", map[string]string{"username": "admin", "password": updated}, "", origin, 204)
	session = request("GET", "/auth/session", nil, "", "", 200)
	csrf = session["csrf_token"].(string)
	request("GET", "/api/v1/organizations", nil, "", "", 200)
	user := request("POST", "/auth/local/users", map[string]any{"username": "worker", "password": initial, "administrator": false}, csrf, origin, 201)
	if _, ok := user["password_hash"]; ok {
		t.Fatal("password hash returned")
	}
	request("PUT", "/auth/local/users/"+user["id"].(string), map[string]any{"expected_revision": 1, "enabled": false, "administrator": false}, csrf, origin, 204)
	request("POST", "/auth/logout", nil, csrf, origin, 204)
	request("GET", "/auth/session", nil, "", "", 401)
}
