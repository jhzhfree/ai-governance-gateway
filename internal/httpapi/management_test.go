package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/api/openapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/mockprovider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type fixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	store    *control.Store
	issuer   *testutil.Issuer
	server   *httptest.Server
	contract routers.Router
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool, ledger, _ := testutil.Database(t)
	store := control.New(pool)
	issuer := testutil.NewIssuer(t)
	auth, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("project secret leaked to supplier")
		}
		mockprovider.Handler(w, r)
	}))
	t.Cleanup(upstream.Close)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", httpapi.New(store, auth))
	mux.Handle("/v1/", &gateway.Handler{DeviceProof: store.VerifyDeviceRequest, ListModels: store.KeyModels, Admission: admission.New(pool), Attempts: execution.New(pool), Ledger: ledger, Authorize: store.ResolveKey, Client: upstream.Client(), Upstream: upstream.URL + "/v1/chat/completions"})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	doc, err := openapi3.NewLoader().LoadFromData(openapi.Document)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{t: t, pool: pool, store: store, issuer: issuer, server: s, contract: contract}
}
func (f fixture) token(subject string) string { return f.issuer.Token(f.t, subject, nil) }
func (f fixture) request(token, method, path string, body any, want int) []byte {
	f.t.Helper()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(payload))
	if err != nil {
		f.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	// Untrusted scope hints must never influence key-derived authorization.
	r.Header.Set("X-Organization-ID", "spoofed-org")
	r.Header.Set("X-Project-ID", "spoofed-project")
	resp, err := f.server.Client().Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if resp.StatusCode != want {
		f.t.Fatalf("%s %s: got %d want %d", method, path, resp.StatusCode, want)
	}
	if (strings.HasPrefix(path, "/api/") || (method == "GET" && path == "/v1/models")) && resp.Header.Get("Cache-Control") != "no-store" {
		f.t.Error("management response may be cached")
	}
	if strings.HasPrefix(path, "/api/") || (method == "GET" && path == "/v1/models") {
		route, params, err := f.contract.FindRoute(r)
		if err != nil {
			f.t.Fatal("operation missing from OpenAPI")
		}
		input := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route, Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
		if want < 400 {
			r.Body = io.NopCloser(bytes.NewReader(payload))
			if err := openapi3filter.ValidateRequest(context.Background(), input); err != nil {
				f.t.Fatalf("%s %s request violates OpenAPI", method, path)
			}
		}
		validation := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: resp.StatusCode, Header: resp.Header}
		validation.SetBodyBytes(b)
		if err := openapi3filter.ValidateResponse(context.Background(), validation); err != nil {
			f.t.Fatalf("%s %s response violates OpenAPI (body suppressed)", method, path)
		}
	}
	return b
}
func parse[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal("invalid response JSON")
	}
	return v
}
func (f fixture) org(owner string) control.Organization {
	return parse[control.Organization](f.t, f.request(f.token(owner), "POST", "/api/v1/organizations", map[string]any{"name": "Team", "period": "2026-11", "limit_amount": 100}, 201))
}
func orgPath(o control.Organization) string { return "/api/v1/organizations/" + o.ID }
func projectPath(o control.Organization, p control.Project) string {
	return orgPath(o) + "/projects/" + p.ID
}
func (f fixture) project(owner string, o control.Organization) control.Project {
	return parse[control.Project](f.t, f.request(f.token(owner), "POST", orgPath(o)+"/projects", map[string]any{"name": "App", "limit_amount": 50}, 201))
}
func (f fixture) key(subject string, o control.Organization, p control.Project) control.IssuedKey {
	return parse[control.IssuedKey](f.t, f.request(f.token(subject), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "SDK key", "models": []string{"mock-text"}, "ttl_seconds": 3600}, 201))
}
func inference() map[string]any {
	return map[string]any{"model": "mock-text", "stream": true, "max_tokens": 8, "messages": []any{map[string]string{"role": "user", "content": "hello"}}}
}
func (f fixture) member(o control.Organization, subject, role string) {
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": subject, "role": role}, 204)
}

func TestManagedKeyInferenceAndRevocation(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	k := f.key("alice", o, p)
	if !strings.HasPrefix(k.Secret, "aigw_pk_") || len(k.Secret) < 60 {
		t.Fatal("key entropy/format invalid")
	}
	var digest []byte
	var row string
	if err := f.pool.QueryRow(context.Background(), `SELECT digest,to_jsonb(k)::text FROM project_keys k WHERE id=$1`, k.ID).Scan(&digest, &row); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(k.Secret))
	if !bytes.Equal(digest, wantDigest[:]) || strings.Contains(row, k.Secret) {
		t.Fatal("key not stored as digest only")
	}
	metadata := f.request(f.token("alice"), "GET", projectPath(o, p)+"/keys", nil, 200)
	if strings.Contains(string(metadata), k.Secret) || strings.Contains(string(metadata), `"secret"`) || strings.Contains(string(metadata), `"digest"`) {
		t.Fatal("key material exposed in metadata")
	}
	result := f.request(k.Secret, "POST", "/v1/chat/completions", inference(), 200)
	if !strings.Contains(string(result), "data: [DONE]") {
		t.Fatal("inference did not complete")
	}
	u := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if u.Spent != 4 || u.Held != 0 {
		t.Fatalf("unexpected ledger %d/%d", u.Spent, u.Held)
	}
	f.request(k.Secret, "GET", "/api/v1/organizations", nil, 401)
	f.request(f.token("alice"), "POST", "/v1/chat/completions", inference(), 401)
	f.request("", "POST", "/v1/chat/completions", inference(), 401)
	f.request(f.token("alice"), "DELETE", projectPath(o, p)+"/keys/"+k.ID, nil, 204)
	f.request(f.token("alice"), "DELETE", projectPath(o, p)+"/keys/"+k.ID, nil, 204)
	f.request(k.Secret, "POST", "/v1/chat/completions", inference(), 401)
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='project.key_revoked'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("revocation audit duplicated")
	}
}

func TestRolesTenantIsolationAndOwnKeyRule(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	otherProject := f.project("alice", o)
	f.member(o, "viewer", "viewer")
	f.member(o, "dev", "developer")
	f.member(o, "admin", "admin")
	member := func(subject, role string) map[string]string {
		return map[string]string{"issuer": f.issuer.URL, "subject": subject, "role": role}
	}
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/members", member("dev", "developer"), 204)
	f.request(f.token("viewer"), "GET", projectPath(o, p)+"/usage", nil, 200)
	f.request(f.token("viewer"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "bad", "models": []string{"mock-text"}, "ttl_seconds": 3600}, 403)
	f.request(f.token("dev"), "GET", projectPath(o, otherProject)+"/usage", nil, 404)
	projects := parse[struct {
		Data []control.Project `json:"data"`
	}](t, f.request(f.token("dev"), "GET", orgPath(o)+"/projects", nil, 200))
	if len(projects.Data) != 1 || projects.Data[0].ID != p.ID {
		t.Fatal("unassigned project leaked")
	}
	detail := parse[control.ProjectDetails](t, f.request(f.token("dev"), "GET", projectPath(o, p), nil, 200))
	if detail.Role != "developer" || len(detail.Models) != 1 {
		t.Fatal("effective role/model metadata missing")
	}
	ownerKey := f.key("alice", o, p)
	devKey := f.key("dev", o, p)
	f.request(f.token("dev"), "DELETE", projectPath(o, p)+"/keys/"+ownerKey.ID, nil, 403)
	f.request(f.token("dev"), "DELETE", projectPath(o, p)+"/keys/"+devKey.ID, nil, 204)
	f.request(f.token("admin"), "PUT", orgPath(o)+"/members", member("viewer", "admin"), 403)
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members", member("alice", "viewer"), 403)
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members", member("viewer", "owner"), 400)
	f.request(f.token("dev"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": []string{}}, 403)
	f.request(f.token("dev"), "PUT", projectPath(o, p)+"/members", member("dev", "admin"), 403)
	f.request(f.token("dev"), "GET", orgPath(o)+"/audit", nil, 403)
	// A different owner cannot use path IDs to cross organization boundaries.
	bobOrg := f.org("bob")
	bobProject := f.project("bob", bobOrg)
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/keys", nil, 404)
	f.request(f.token("alice"), "GET", projectPath(o, bobProject)+"/usage", nil, 404)
	f.request(f.token("alice"), "DELETE", projectPath(bobOrg, bobProject)+"/keys/"+ownerKey.ID, nil, 404)
	// Org role changes affect subsequent management requests without token refresh.
	f.member(o, "admin", "viewer")
	f.request(f.token("admin"), "POST", orgPath(o)+"/projects", map[string]any{"name": "No", "limit_amount": 1}, 403)
}

func TestModelAndExpiryChecksAreLive(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	k := f.key("alice", o, p)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": []string{}}, 204)
	f.request(k.Secret, "POST", "/v1/chat/completions", inference(), 403)
	f.request(f.token("alice"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "blocked", "models": []string{"mock-text"}, "ttl_seconds": 3600}, 403)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": []string{"mock-text"}}, 204)
	f.request(k.Secret, "POST", "/v1/chat/completions", inference(), 200)
	if _, err := f.pool.Exec(context.Background(), `UPDATE project_keys SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, k.ID); err != nil {
		t.Fatal(err)
	}
	f.request(k.Secret, "POST", "/v1/chat/completions", inference(), 401)
}

func TestPaginationAndInputContract(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.key("alice", o, p)
	f.key("alice", o, p)
	var all []string
	after := ""
	for page := 0; page < 3; page++ {
		b := f.request(f.token("alice"), "GET", projectPath(o, p)+"/keys?limit=1&after="+after, nil, 200)
		v := parse[struct {
			Data []control.Key `json:"data"`
			Next string        `json:"next_cursor"`
		}](t, b)
		for _, k := range v.Data {
			all = append(all, k.ID)
		}
		if v.Next == "" {
			break
		}
		after = v.Next
	}
	if len(all) != 2 || all[0] == all[1] {
		t.Fatal("pagination duplicated or lost keys")
	}
	f.request(f.token("alice"), "GET", orgPath(o)+"/audit?limit=0", nil, 400)
	f.request(f.token("alice"), "GET", orgPath(o)+"/audit?after=invalid", nil, 400)
	f.request(f.token("alice"), "POST", orgPath(o)+"/projects", map[string]any{"name": "Missing budget"}, 400)
	f.request(f.token("alice"), "POST", orgPath(o)+"/projects", map[string]any{"name": "Unknown", "limit_amount": 10, "role": "owner"}, 400)
	f.request(f.token("alice"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "overflow", "models": []string{"mock-text"}, "ttl_seconds": int64(1 << 62)}, 400)
	f.request(f.issuer.Token(t, "alice", map[string]any{"scope": "openid"}), "GET", "/api/v1/organizations", nil, 403)
	f.request("", "GET", "/api/v1/organizations", nil, 401)
	member := map[string]string{"issuer": f.issuer.URL, "subject": "viewer", "role": "viewer"}
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members", member, 204)
	isolated := parse[struct {
		Data []control.Organization `json:"data"`
	}](t, f.request(f.token("outsider"), "GET", "/api/v1/organizations", nil, 200))
	if len(isolated.Data) != 0 {
		t.Fatal("organization leaked")
	}
}

func TestAuditFailureRollsBackKeyIssuance(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_key CHECK(action <> 'project.key_created')`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "rollback", "models": []string{"mock-text"}, "ttl_seconds": 3600}, 503)
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM project_keys`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("key survived failed audit transaction")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events DROP CONSTRAINT reject_key`); err != nil {
		t.Fatal(err)
	}
	k := f.key("alice", o, p)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_revoke CHECK(action <> 'project.key_revoked')`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "DELETE", projectPath(o, p)+"/keys/"+k.ID, nil, 503)
	f.request(k.Secret, "POST", "/v1/chat/completions", inference(), 200)
}

func TestUnavailableAuthorizationDatabaseFailsClosed(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	k := f.key("alice", o, p)
	f.pool.Close()
	f.request(k.Secret, "POST", "/v1/chat/completions", inference(), 503)
	f.request(f.token("alice"), "GET", projectPath(o, p)+"/keys", nil, 503)
}
