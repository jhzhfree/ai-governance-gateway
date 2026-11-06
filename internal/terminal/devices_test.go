package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/mockprovider"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeviceCLIProxySignsRealGatewayRequestsAndRejectsLocalBypass(t *testing.T) {
	f := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "private")
	keyPath, tokenPath := filepath.Join(dir, "key.pem"), filepath.Join(dir, "local-token")
	code, out, _ := f.execute(t, f.token, "device", "keygen", "--private-key-file", keyPath, "--local-token-file", tokenPath)
	if code != 0 {
		t.Fatal("device key generation failed")
	}
	var generated map[string]string
	if json.Unmarshal([]byte(out), &generated) != nil || generated["public_key"] == "" {
		t.Fatal("public enrollment descriptor missing")
	}
	keyBefore, _ := os.ReadFile(keyPath)
	if code, _, _ := f.execute(t, f.token, "device", "keygen", "--private-key-file", keyPath, "--local-token-file", tokenPath); code == 0 {
		t.Fatal("device private key overwritten")
	}
	keyAfter, _ := os.ReadFile(keyPath)
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("private key changed on failed overwrite")
	}
	code, out, _ = f.execute(t, f.token, "device", "register", "--org", f.org.ID, "--name", "Ubuntu CLI fixture", "--private-key-file", keyPath)
	var d control.Device
	if code != 0 || json.Unmarshal([]byte(out), &d) != nil || d.State != "pending" {
		t.Fatal("CLI registration failed")
	}
	owner := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	var err error
	d, err = f.store.ChangeDevice(ctx, owner, f.org.ID, d.ID, 0, "active", "administrator approved workstation")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := identity.NewOIDC(ctx, f.issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	upstream := httptest.NewServer(http.HandlerFunc(mockprovider.Handler))
	defer upstream.Close()
	mux.Handle("/api/v1/", httpapi.New(f.store, auth))
	mux.Handle("/v1/", &gateway.Handler{Admission: admission.New(f.pool), Attempts: f.recorder, DeviceProof: f.store.VerifyDeviceRequest, Authorize: f.store.ResolveKey, ListModels: f.store.KeyModels, Ledger: f.ledger, Client: upstream.Client(), Upstream: upstream.URL + "/v1/chat/completions"})
	server := httptest.NewServer(mux)
	defer server.Close()
	f.store.WithDeviceOrigin(server.URL)
	if _, err := f.store.SetDevicePolicy(ctx, owner, f.org.ID, f.project.ID, 0, true, "enforce device"); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(server.URL, f.token, true, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	done := make(chan error, 1)
	proxyCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		done <- runDeviceProxy(proxyCtx, client, f.org.ID, f.project.ID, d.ID, keyPath, tokenPath, "127.0.0.1:0", []string{"mock-text"}, func(endpoint string) { started <- endpoint })
	}()
	var endpoint string
	select {
	case endpoint = <-started:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("proxy startup timed out")
	}
	localRaw, _ := os.ReadFile(tokenPath)
	localToken := strings.TrimSpace(string(localRaw))
	request := func(method, path, credential, origin, body string, want int) ([]byte, string) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(endpoint, "/v1")+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("proxy status %d want %d (payload suppressed)", resp.StatusCode, want)
		}
		if bytes.Contains(data, []byte(f.token)) {
			t.Fatal("management token leaked to client")
		}
		return data, resp.Header.Get("X-Request-ID")
	}
	request("GET", "/v1/models", "", "", "", 401)
	request("GET", "/v1/models", localToken, "https://untrusted.synthetic.invalid", "", 403)
	request("GET", "/api/v1/organizations", localToken, "", "", 404)
	request("GET", "/v1/models", localToken, "", "", 200)
	data, id := request("POST", "/v1/chat/completions", localToken, "", `{"model":"mock-text","stream":true,"max_tokens":4,"messages":[{"role":"user","content":"synthetic device proxy fixture"}]}`, 200)
	if !strings.HasSuffix(string(data), "data: [DONE]\n\n") {
		t.Fatal("proxy broke streaming framing")
	}
	reservation, err := f.ledger.Get(ctx, id)
	if err != nil || reservation.State != "settled" {
		t.Fatal("proxy inference did not settle real ledger")
	}
	bindings, err := f.store.DeviceRequests(ctx, owner, f.org.ID, d.ID, "")
	if err != nil || len(bindings) != 1 || bindings[0].RequestID != id {
		t.Fatal("proxy request device binding absent")
	}
	if _, err := f.store.ChangeDevice(ctx, owner, f.org.ID, d.ID, d.Revision, "revoked", "lost workstation"); err != nil {
		t.Fatal(err)
	}
	request("GET", "/v1/models", localToken, "", "", 401)
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("proxy did not stop")
	}
	if err := runDeviceProxy(ctx, client, f.org.ID, f.project.ID, d.ID, keyPath, tokenPath, "0.0.0.0:16680", nil, nil); err == nil {
		t.Fatal("proxy accepted public bind")
	}
}
