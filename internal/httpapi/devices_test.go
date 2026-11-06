package httpapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/deviceproof"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func deviceRequest(f fixture, token, scheme, method, path string, body any, proof string, want int) ([]byte, string) {
	f.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", scheme+" "+token)
	if proof != "" {
		r.Header.Set("DPoP", proof)
	}
	r.Header.Set("X-Device-ID", "untrusted-spoof")
	r.Header.Set("X-Project-ID", "untrusted-spoof")
	resp, err := f.server.Client().Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		f.t.Fatal(err)
	}
	if resp.StatusCode != want {
		f.t.Fatalf("device request %s %s got %d want %d (credentials/body suppressed)", method, path, resp.StatusCode, want)
	}
	return data, resp.Header.Get("X-Request-ID")
}
func registeredDevice(f fixture, o control.Organization, subject string) (control.Device, ed25519.PrivateKey) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	d := parse[control.Device](f.t, f.request(f.token(subject), "POST", orgPath(o)+"/devices", map[string]string{"name": "Ubuntu synthetic workstation", "public_key": deviceproof.Public(pub)}, 201))
	return d, key
}
func activateDevice(f fixture, o control.Organization, d control.Device) control.Device {
	return parse[control.Device](f.t, f.request(f.token("alice"), "PUT", orgPath(o)+"/devices/"+d.ID, map[string]any{"state": "active", "expected_revision": d.Revision, "reason": "administrator confirms synthetic enrollment"}, 200))
}
func issueDevice(f fixture, o control.Organization, p control.Project, d control.Device, key ed25519.PrivateKey, subject string) control.IssuedDeviceSession {
	path := projectPath(o, p) + "/devices/" + d.ID + "/sessions"
	proof, err := deviceproof.Sign(key, "POST", f.server.URL+path, "")
	if err != nil {
		f.t.Fatal(err)
	}
	data, _ := deviceRequest(f, f.token(subject), "Bearer", "POST", path, map[string]any{"models": []string{"aliyun/fixture"}}, proof, 201)
	return parse[control.IssuedDeviceSession](f.t, data)
}
func resourceProof(f fixture, key ed25519.PrivateKey, token, method, path string) string {
	proof, err := deviceproof.Sign(key, method, f.server.URL+path, token)
	if err != nil {
		f.t.Fatal(err)
	}
	return proof
}
func TestDeviceAdmissionRequiresPossessionAndBindsRealInference(t *testing.T) {
	f := readyManaged(t)
	f.store.WithDeviceOrigin(f.server.URL)
	o, p, pk := providerProject(f)
	f.request(pk.Secret, "GET", "/v1/models", nil, 200)
	d, key := registeredDevice(f, o, "alice")
	path := orgPath(o) + "/devices/" + d.ID
	sessionPath := projectPath(o, p) + "/devices/" + d.ID + "/sessions"
	proof := resourceProof(f, key, "", "POST", sessionPath)
	deviceRequest(f, f.token("alice"), "Bearer", "POST", sessionPath, map[string]any{"models": []string{"aliyun/fixture"}}, proof, 403)
	d = activateDevice(f, o, d)
	session := issueDevice(f, o, p, d, key, "alice")
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/device-policy", map[string]any{"required": true, "expected_revision": 0, "reason": "require approved endpoint"}, 200)
	before := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	f.request(pk.Secret, "GET", "/v1/models", nil, 403)
	f.request(pk.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 403)
	deviceRequest(f, session.Token, "Bearer", "GET", "/v1/models", nil, resourceProof(f, key, session.Token, "GET", "/v1/models"), 401)
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, "", 401)
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, resourceProof(f, wrong, session.Token, "GET", "/v1/models"), 401)
	proof = resourceProof(f, key, session.Token, "GET", "/v1/models")
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, proof, 200)
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, proof, 401)
	deviceRequest(f, session.Token, "DPoP", "POST", "/v1/chat/completions", realPayload("volcengine/fixture", false), resourceProof(f, key, session.Token, "POST", "/v1/chat/completions"), 403)
	if now := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200)); now != before {
		t.Fatal("admission denial consumed budget")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE device_request_bindings ADD CONSTRAINT reject_device_binding CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	deviceRequest(f, session.Token, "DPoP", "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), resourceProof(f, key, session.Token, "POST", "/v1/chat/completions"), 503)
	if now := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200)); now != before {
		t.Fatal("device attribution failure did not roll back budget")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE device_request_bindings DROP CONSTRAINT reject_device_binding`); err != nil {
		t.Fatal(err)
	}
	_, requestID := deviceRequest(f, session.Token, "DPoP", "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), resourceProof(f, key, session.Token, "POST", "/v1/chat/completions"), 200)
	bound := parse[struct {
		Data []control.DeviceRequest `json:"data"`
	}](t, f.request(f.token("alice"), "GET", path+"/requests", nil, 200))
	if len(bound.Data) != 1 || bound.Data[0].RequestID != requestID || bound.Data[0].SessionID != session.ID || bound.Data[0].ProjectID != p.ID {
		t.Fatal("real inference device attribution missing")
	}
	sessions := f.request(f.token("alice"), "GET", path+"/sessions", nil, 200)
	if bytes.Contains(sessions, []byte(session.Token)) {
		t.Fatal("session listing exposed token")
	}
	f.request(f.token("alice"), "POST", path+"/sessions/"+session.ID+"/revoke", map[string]string{"reason": "revoke stolen token"}, 204)
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, resourceProof(f, key, session.Token, "GET", "/v1/models"), 401)
	next := issueDevice(f, o, p, d, key, "alice")
	f.request(f.token("alice"), "PUT", path, map[string]any{"state": "revoked", "expected_revision": d.Revision, "reason": "retire endpoint"}, 200)
	deviceRequest(f, next.Token, "DPoP", "GET", "/v1/models", nil, resourceProof(f, key, next.Token, "GET", "/v1/models"), 401)
	f.request(f.token("alice"), "PUT", path, map[string]any{"state": "active", "expected_revision": d.Revision + 1, "reason": "must not revive"}, 409)
}
func TestDeviceOwnershipScopeAndMemberRestoreCannotRevive(t *testing.T) {
	f := readyManaged(t)
	f.store.WithDeviceOrigin(f.server.URL)
	o, p, _ := providerProject(f)
	f.member(o, "bob", "developer")
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "role": "developer"}, 204)
	d, key := registeredDevice(f, o, "bob")
	path := orgPath(o) + "/devices/" + d.ID
	f.request(f.token("bob"), "PUT", path, map[string]any{"state": "active", "expected_revision": 0, "reason": "cannot self approve as developer"}, 403)
	d = activateDevice(f, o, d)
	session := issueDevice(f, o, p, d, key, "bob")
	sp := projectPath(o, p) + "/devices/" + d.ID + "/sessions"
	deviceRequest(f, f.token("alice"), "Bearer", "POST", sp, map[string]any{"models": []string{"aliyun/fixture"}}, resourceProof(f, key, "", "POST", sp), 403)
	other, otherP, _ := providerProject(f)
	cross := projectPath(other, otherP) + "/devices/" + d.ID + "/sessions"
	deviceRequest(f, f.token("alice"), "Bearer", "POST", cross, map[string]any{"models": []string{"aliyun/fixture"}}, resourceProof(f, key, "", "POST", cross), 404)
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", path+"/sessions", nil, 404)
	f.request(f.token("alice"), "DELETE", projectPath(o, p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "expected_role": "developer"}, 204)
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, resourceProof(f, key, session.Token, "GET", "/v1/models"), 403)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "role": "developer"}, 204)
	event := parse[control.MemberLifecycleEvent](t, f.request(f.token("alice"), "PUT", orgPath(o)+"/members/enabled", lifecycleBody(f, "bob", 0, false, ""), 200))
	if event.RevokedDevices != 1 {
		t.Fatal("member stop did not revoke device")
	}
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members/enabled", lifecycleBody(f, "bob", 1, true, "developer"), 200)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "role": "developer"}, 204)
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, resourceProof(f, key, session.Token, "GET", "/v1/models"), 401)
	f.request(f.token("bob"), "POST", orgPath(o)+"/devices", map[string]string{"name": "copied old device key", "public_key": d.PublicKey}, 409)
}
func TestDeviceProofExpiryPolicyAndAuditRollback(t *testing.T) {
	f := readyManaged(t)
	f.store.WithDeviceOrigin(f.server.URL)
	o, p, pk := providerProject(f)
	d, key := registeredDevice(f, o, "alice")
	d = activateDevice(f, o, d)
	session := issueDevice(f, o, p, d, key, "alice")
	path := orgPath(o) + "/devices/" + d.ID
	for _, offset := range []time.Duration{-120 * time.Second, 60 * time.Second} {
		proof, err := deviceproof.SignAt(key, "GET", f.server.URL+"/v1/models", session.Token, time.Now().Add(offset), rand.Text())
		if err != nil {
			t.Fatal(err)
		}
		deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, proof, 401)
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_device_revoke CHECK(action<>'device.revoked') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", path, map[string]any{"state": "revoked", "expected_revision": 1, "reason": "must roll back"}, 503)
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, resourceProof(f, key, session.Token, "GET", "/v1/models"), 200)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_device_policy CHECK(action<>'device.policy_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/device-policy", map[string]any{"required": true, "expected_revision": 0, "reason": "policy rollback"}, 503)
	f.request(pk.Secret, "GET", "/v1/models", nil, 200)
	if _, err := f.pool.Exec(context.Background(), `UPDATE device_sessions SET created_at=clock_timestamp()-interval '10 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, session.ID); err != nil {
		t.Fatal(err)
	}
	deviceRequest(f, session.Token, "DPoP", "GET", "/v1/models", nil, resourceProof(f, key, session.Token, "GET", "/v1/models"), 401)
}

func TestDeviceConcurrentProofCannotReplayAcrossInstances(t *testing.T) {
	f := readyManaged(t)
	f.store.WithDeviceOrigin(f.server.URL)
	o, p, _ := providerProject(f)
	d, key := registeredDevice(f, o, "alice")
	d = activateDevice(f, o, d)
	session := issueDevice(f, o, p, d, key, "alice")
	other := control.New(f.pool).WithDeviceOrigin(f.server.URL)
	proof := resourceProof(f, key, session.Token, "GET", "/v1/models")
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*control.Store{f.store, other} {
		wg.Add(1)
		go func(store *control.Store) {
			defer wg.Done()
			r, _ := http.NewRequest("GET", f.server.URL+"/v1/models", nil)
			r.Header.Set("DPoP", proof)
			_, err := store.VerifyDeviceRequest(context.Background(), r, session.Token)
			results <- err
		}(store)
	}
	wg.Wait()
	close(results)
	success, denied := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, identity.ErrUnauthenticated) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatal("shared replay state admitted duplicate proof")
	}
}

func TestDeviceStateCASFencesConcurrentRevocation(t *testing.T) {
	f := readyManaged(t)
	o, _, _ := providerProject(f)
	d, _ := registeredDevice(f, o, "alice")
	d = activateDevice(f, o, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// A global local-account stop is outside the organization member advisory lock.
	// Model its already-in-flight device update while a stale organization write waits.
	if _, err := tx.Exec(ctx, `UPDATE terminal_devices SET state='revoked',revision=revision+1 WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.store.ChangeDevice(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}, o.ID, d.ID, d.Revision, "revoked", "concurrent organization revocation")
		done <- err
	}()
	waitForMemberBlocker(t, f, tx.Conn().PgConn().PID())
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, control.ErrMemberConflict) {
		t.Fatal("stale device update overwrote newer global revision")
	}
	var revision int64
	if err := f.pool.QueryRow(ctx, `SELECT revision FROM terminal_devices WHERE id=$1`, d.ID).Scan(&revision); err != nil || revision != d.Revision+1 {
		t.Fatal("newer revocation revision lost")
	}
}
