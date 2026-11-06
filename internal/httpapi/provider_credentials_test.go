package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestProviderPageCredentialIsEncryptedAndUsedForRealProtocol(t *testing.T) {
	var expected atomic.Value
	expected.Store("synthetic-page-first")
	inner := testutil.SupplierFixture(t)
	supplier := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
			t.Error("stored credential not used for routing")
			w.WriteHeader(401)
			return
		}
		copy := r.Clone(r.Context())
		copy.Header = r.Header.Clone()
		copy.Header.Set("Authorization", "Bearer synthetic-AIGW_ALIYUN_API_KEY")
		inner.ServeHTTP(w, copy)
	})
	f, _ := managedSetup(t, supplier)
	ctx := context.Background()
	path := "/api/v1/providers/aliyun/credential"
	body := func(revision int64, value string) any {
		return map[string]any{"expected_revision": revision, "api_key": value}
	}
	f.request(f.token("bob"), "PUT", path, body(0, expected.Load().(string)), 403)
	f.request(f.token("alice"), "PUT", path, body(0, "invalid key with spaces"), 400)
	f.request(f.token("alice"), "PUT", path, body(0, expected.Load().(string)), 204)
	f.request(f.token("alice"), "PUT", path, body(0, "synthetic-stale"), 409)
	cfg := testutil.MainlandConfig()
	f.request(f.token("alice"), "PUT", "/api/v1/providers", map[string]any{"expected_revision": 0, "configuration": cfg}, 200)
	_, _, key := providerProject(f)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	expected.Store("synthetic-page-second")
	f.request(f.token("alice"), "PUT", path, body(1, expected.Load().(string)), 204)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), 200)
	metadata := string(f.request(f.token("alice"), "GET", "/api/v1/providers", nil, 200))
	if strings.Contains(metadata, "synthetic-page-") || strings.Contains(metadata, "api_key") {
		t.Fatal("credential returned to browser")
	}
	var cipher []byte
	if err := f.pool.QueryRow(ctx, `SELECT ciphertext FROM provider_credentials WHERE provider='aliyun'`).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipher), expected.Load().(string)) {
		t.Fatal("credential stored in plaintext")
	}
	_, err := f.pool.Exec(ctx, `ALTER TABLE provider_credential_events ADD CONSTRAINT reject_update CHECK(revision<3)`)
	if err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", path, body(2, "synthetic-failed-update"), 503)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	_, err = f.pool.Exec(ctx, `UPDATE provider_credentials SET ciphertext=set_byte(ciphertext,0,get_byte(ciphertext,0)#1) WHERE provider='aliyun'`)
	if err != nil {
		t.Fatal(err)
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 503)
}
func TestProviderCredentialRequiresEncryptionAndRestoresWithHistoricalKey(t *testing.T) {
	f, _ := managedSetup(t)
	ctx := context.Background()
	admin := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	unavailable := provider.NewManager(f.pool, func(string) string { return "" }, nil, admin)
	if unavailable.SetCredential(ctx, admin, "aliyun", 0, "synthetic-key") == nil {
		t.Fatal("plaintext storage enabled without encryption")
	}
	// Synthetic keys for rotation; never production values.
	versions := map[string]string{"v1": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}
	active := "v1"
	getter := func(name string) string {
		switch name {
		case "AIGW_PROVIDER_ENCRYPTION_KEYS":
			raw, _ := json.Marshal(versions)
			return string(raw)
		case "AIGW_PROVIDER_ENCRYPTION_ACTIVE_KEY":
			return active
		}
		return "synthetic-" + name
	}
	m := provider.NewManager(f.pool, getter, nil, admin)
	if err := m.SetCredential(ctx, admin, "aliyun", 0, "synthetic-key"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetCredential(ctx, admin, "zhipu", 0, "synthetic-retained-key"); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(ctx, admin, 0, testutil.MainlandConfig()); err != nil {
		t.Fatal(err)
	}
	if unavailable.Ready() {
		t.Fatal("lost master key fell back to another credential")
	}
	versions["v2"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	active = "v2"
	rotated := provider.NewManager(f.pool, getter, nil, admin)
	if !rotated.Ready() {
		t.Fatal("rotation could not read retained historical keys")
	}
	if err := rotated.SetCredential(ctx, admin, "aliyun", 1, "synthetic-new-key"); err != nil {
		t.Fatal(err)
	}
	var keyID string
	if err := f.pool.QueryRow(ctx, `SELECT key_id FROM provider_credentials WHERE provider='aliyun'`).Scan(&keyID); err != nil || keyID != "v2" {
		t.Fatal("active master key not used")
	}
	previous := versions["v1"]
	delete(versions, "v1")
	if rotated.Ready() {
		t.Fatal("missing historical key was ignored")
	}
	versions["v1"] = previous
	recovered := provider.NewManager(f.pool, getter, nil, admin)
	if !recovered.Ready() {
		t.Fatal("restoring historical key did not restore retained credential")
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM provider_credential_events`).Scan(&count); err != nil || count != 3 {
		t.Fatal("credential audit missing")
	}
}

func TestDeepSeekCredentialPublicationAndBillingImport(t *testing.T) {
	f, m := managedSetup(t, testutil.SupplierFixture(t))
	ctx := context.Background()
	admin := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	if err := m.SetCredential(ctx, admin, "deepseek", 0, "synthetic-AIGW_DEEPSEEK_API_KEY"); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", "/api/v1/providers", map[string]any{"expected_revision": 0, "configuration": testutil.MainlandConfig()}, 200)
	o, p, key := providerProject(f)
	for _, stream := range []bool{false, true} {
		f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("deepseek/fixture", stream), 200)
	}
	input := billImport("deepseek-invoice,,2026-11,CNY_MICRO,14,3,2")
	input.Provider = "deepseek"
	f.request(f.token("alice"), "POST", projectPath(o, p)+"/billing/imports", input, 200)
	var records int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM billing_records WHERE provider='deepseek'`).Scan(&records); err != nil || records != 1 {
		t.Fatal("DeepSeek invoice not persisted")
	}
	var cipher []byte
	if err := f.pool.QueryRow(ctx, `SELECT ciphertext FROM provider_credentials WHERE provider='deepseek'`).Scan(&cipher); err != nil || bytes.Contains(cipher, []byte("synthetic-AIGW")) {
		t.Fatal("credential storage failed")
	}
}
