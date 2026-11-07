package httpapi_test

import (
	"context"
	"errors"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/systemhost"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu    sync.Mutex
	node  string
	calls int
	fail  bool
}

func (h *fakeHost) Snapshot(context.Context) (systemhost.Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return systemhost.Snapshot{Node: h.node, Metrics: systemhost.Metrics{Scope: "synthetic", SampledAt: time.Now(), CPUCount: 2, Interfaces: []systemhost.Link{}, DNS: []string{}}, Networks: []systemhost.Network{}, NetworkEnabled: true}, nil
}
func (h *fakeHost) Apply(_ context.Context, c systemhost.Change) (systemhost.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if h.fail {
		return systemhost.Result{}, errors.New("private host failure")
	}
	return systemhost.Result{ID: c.ID, Interface: c.Interface, Deadline: time.Now().Add(time.Minute), State: "pending"}, nil
}
func (h *fakeHost) Finish(_ context.Context, id string, confirm bool) (systemhost.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	state := "rolled_back"
	if confirm {
		state = "confirmed"
	}
	return systemhost.Result{ID: id, Interface: "eth0", Deadline: time.Now(), State: state}, nil
}
func (h *fakeHost) count() int { h.mu.Lock(); defer h.mu.Unlock(); return h.calls }
func TestSystemAdministrationAuthorizationAuditAndNodeBinding(t *testing.T) {
	f := setup(t)
	alice := f.token("alice")
	bob := f.token("bob")
	f.request(alice, "GET", "/api/v1/system", nil, 403)
	host := &fakeHost{node: "node-a"}
	f.store.WithSystem(host, identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}, "node-a")
	f.request("", "GET", "/api/v1/system", nil, 401)
	f.request(bob, "GET", "/api/v1/system", nil, 403)
	f.request(alice, "GET", "/api/v1/system", nil, 200)
	f.request(bob, "POST", "/api/v1/organizations", map[string]any{"name": "owner is not system admin", "period": "2026-11", "limit_amount": 10000}, 201)
	change := systemhost.Change{Interface: "eth0", ExpectedRevision: strings.Repeat("a", 64), IPv4: systemhost.IPv4{Method: "auto", Addresses: []string{}, DNS: []string{}, AutomaticDNS: true}}
	body := map[string]any{"node": "node-a", "reason": "synthetic network change", "change": change}
	f.request(bob, "POST", "/api/v1/system/network", body, 403)
	body["node"] = "node-b"
	f.request(alice, "POST", "/api/v1/system/network", body, 409)
	body["node"] = "node-a"
	host.mu.Lock()
	host.node = "wrong-agent"
	host.mu.Unlock()
	f.request(alice, "POST", "/api/v1/system/network", body, 409)
	host.mu.Lock()
	host.node = "node-a"
	host.mu.Unlock()
	if host.count() != 0 {
		t.Fatal("unauthorized or wrong-node change dispatched")
	}
	result := parse[systemhost.Result](t, f.request(alice, "POST", "/api/v1/system/network", body, 200))
	if result.State != "pending" || host.count() != 1 {
		t.Fatal("missing apply")
	}
	f.request(alice, "POST", "/api/v1/system/network/"+result.ID+"/confirm", map[string]any{"node": "node-a", "reason": "verified connectivity"}, 200)
	events := parse[struct {
		Data []control.SystemEvent `json:"data"`
	}](t, f.request(alice, "GET", "/api/v1/system/events", nil, 200))
	if len(events.Data) != 6 {
		t.Fatal("missing durable intent/outcome", len(events.Data))
	}
	f.request(bob, "GET", "/api/v1/system/events", nil, 403)
	if _, e := f.pool.Exec(context.Background(), `ALTER TABLE system_management_events ADD CONSTRAINT reject_intent CHECK(phase<>'requested') NOT VALID`); e != nil {
		t.Fatal(e)
	}
	f.request(alice, "POST", "/api/v1/system/network", body, 503)
	if host.count() != 2 {
		t.Fatal("audit failure did not stop host operation")
	}
}
func TestSystemUnavailableOutcomeIsNotSuccess(t *testing.T) {
	f := setup(t)
	host := &fakeHost{node: "node-a", fail: true}
	f.store.WithSystem(host, identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}, "node-a")
	body := map[string]any{"node": "node-a", "reason": "synthetic failure", "change": systemhost.Change{Interface: "eth0", ExpectedRevision: strings.Repeat("a", 64), IPv4: systemhost.IPv4{Method: "auto", Addresses: []string{}, DNS: []string{}, AutomaticDNS: true}}}
	response := f.request(f.token("alice"), "POST", "/api/v1/system/network", body, 503)
	if strings.Contains(string(response), "private") {
		t.Fatal("agent error leaked")
	}
	var count int
	if e := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM system_management_events WHERE phase='unknown'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("unknown dispatch not recorded", e)
	}
}
