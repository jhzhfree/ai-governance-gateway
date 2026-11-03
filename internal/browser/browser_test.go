package browser_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/browser"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestBrowserLoginCSRFAndLogout(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	store := control.New(pool)
	issuer := testutil.NewIssuer(t)
	api, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	origin := "http://" + server.Listener.Addr().String()
	sessions, err := browser.New(context.Background(), pool, api, issuer.URL, browser.Config{Origin: origin, ClientID: "web-client", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/auth/", sessions.Handler())
	mux.Handle("/api/v1/", httpapi.NewWithSession(store, api, sessions))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	server.Start()
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	client := server.Client()
	client.Jar = jar
	request := func(method, path, csrfToken, requestOrigin string, body []byte, want int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, origin+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		r.Header.Set("X-CSRF-Token", csrfToken)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s status=%d want %d", method, path, resp.StatusCode, want)
		}
		return b
	}
	request("GET", "/auth/session", "", "", nil, 401)
	request("GET", "/auth/login", "", "", nil, 200) // complete real redirect/code/PKCE/JWKS flow
	var session struct {
		User identity.Principal `json:"user"`
		CSRF string             `json:"csrf_token"`
	}
	if err := json.Unmarshal(request("GET", "/auth/session", "", "", nil, 200), &session); err != nil {
		t.Fatal(err)
	}
	if session.User.Subject != "alice" || session.CSRF == "" {
		t.Fatal("session not established")
	}
	u, _ := url.Parse(origin)
	var secret string
	for _, c := range jar.Cookies(u) {
		if c.Name == "aigw_session_dev" {
			secret = c.Value
		}
	}
	if secret == "" {
		t.Fatal("missing session cookie")
	}
	var stored []byte
	if err := pool.QueryRow(context.Background(), `SELECT digest FROM browser_sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(secret))
	if !bytes.Equal(stored, hash[:]) {
		t.Fatal("session is not stored as digest")
	}
	payload := []byte(`{"name":"Browser team","period":"2026-11","limit_amount":100}`)
	request("POST", "/api/v1/organizations", "", origin, payload, 403)
	request("POST", "/api/v1/organizations", session.CSRF, "https://attacker.example", payload, 403)
	request("POST", "/api/v1/organizations", session.CSRF, "", payload, 403)
	request("POST", "/api/v1/organizations", session.CSRF, origin, payload, 201)
	request("POST", "/auth/logout", "", origin, nil, 403)
	request("POST", "/auth/logout", session.CSRF, origin, nil, 204)
	request("GET", "/api/v1/organizations", "", "", nil, 401)
	// Replaying the old cookie after logout cannot resurrect the session.
	jar.SetCookies(u, []*http.Cookie{{Name: "aigw_session_dev", Value: secret, Path: "/"}})
	request("GET", "/auth/session", "", "", nil, 401)
}

func TestLoginBindingAndSingleUseState(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	issuer := testutil.NewIssuer(t)
	api, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	origin := "http://" + server.Listener.Addr().String()
	svc, err := browser.New(context.Background(), pool, api, issuer.URL, browser.Config{Origin: origin, ClientID: "web", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/auth/", svc.Handler())
	server.Start()
	t.Cleanup(server.Close)
	client := server.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	login, err := client.Get(origin + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	auth, err := client.Get(login.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	auth.Body.Close()
	callback := auth.Header.Get("Location")
	// Another browser lacks the binding cookie, even with the correct callback URL.
	stranger := &http.Client{Transport: client.Transport, CheckRedirect: client.CheckRedirect}
	denied, err := stranger.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	denied.Body.Close()
	if denied.StatusCode != 400 {
		t.Fatal("unbound login accepted")
	}
	accepted, err := client.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	accepted.Body.Close()
	if accepted.StatusCode != 303 {
		t.Fatal("valid callback rejected")
	}
	replay, err := client.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	replay.Body.Close()
	if replay.StatusCode != 400 {
		t.Fatal("callback replay accepted")
	}
	login, err = client.Get(origin + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	auth, err = client.Get(login.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	auth.Body.Close()
	if _, err := pool.Exec(context.Background(), `UPDATE browser_login_flows SET nonce='mismatched-nonce'`); err != nil {
		t.Fatal(err)
	}
	badNonce, err := client.Get(auth.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	badNonce.Body.Close()
	if badNonce.StatusCode != 401 {
		t.Fatal("nonce mismatch accepted")
	}
}

func TestSecureCookieConfiguration(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	issuer := testutil.NewIssuer(t)
	api, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.New(context.Background(), pool, api, issuer.URL, browser.Config{Origin: "http://public.example", ClientID: "web", AllowLoopbackHTTP: true}); err == nil {
		t.Fatal("non-loopback insecure origin accepted")
	}
	if _, err := browser.New(context.Background(), pool, api, issuer.URL, browser.Config{Origin: "http://127.0.0.1:8080", ClientID: "web"}); err == nil {
		t.Fatal("insecure origin accepted without explicit option")
	}
	svc, err := browser.New(context.Background(), pool, api, issuer.URL, browser.Config{Origin: "https://console.example", ClientID: "web"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://console.example/auth/login", nil)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing binding cookie")
	}
	c := cookies[0]
	if c.Name != "__Host-aigw_session_flow" || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
		t.Fatal("binding cookie security attributes invalid")
	}
}
