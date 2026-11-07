//go:build web_e2e

package browser_test

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/systemhost"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic agent is served over a real Unix socket; never touches host networking.
type browserHost struct {
	mu      sync.Mutex
	network systemhost.Network
	pending *systemhost.Result
	desired systemhost.IPv4
}

func (h *browserHost) Snapshot(context.Context) (systemhost.Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var pending *systemhost.Result
	if h.pending != nil {
		v := *h.pending
		pending = &v
	}
	cpu := 25.0
	return systemhost.Snapshot{Node: "browser-fixture", Metrics: systemhost.Metrics{Scope: "synthetic", SampledAt: time.Now(), CPUPercent: &cpu, CPUCount: 4, MemoryTotal: 1024 * 1024 * 1024, MemoryAvailable: 512 * 1024 * 1024, Interfaces: []systemhost.Link{{Name: "eth0", Up: true, MTU: 1500, Addresses: []string{"192.0.2.10/24"}}}, DNS: []string{"192.0.2.53"}}, NetworkEnabled: true, Networks: []systemhost.Network{h.network}, Pending: pending}, nil
}
func (h *browserHost) Apply(_ context.Context, c systemhost.Change) (systemhost.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := c.Validate(); e != nil {
		return systemhost.Result{}, e
	}
	if h.pending != nil || c.ExpectedRevision != h.network.Revision {
		return systemhost.Result{}, systemhost.ErrConflict
	}
	v := systemhost.Result{ID: c.ID, State: "pending", Interface: "eth0", Deadline: time.Now().Add(90 * time.Second)}
	h.pending = &v
	h.desired = c.IPv4
	return v, nil
}
func (h *browserHost) Finish(_ context.Context, id string, confirm bool) (systemhost.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == nil || h.pending.ID != id {
		return systemhost.Result{}, systemhost.ErrConflict
	}
	v := *h.pending
	v.State = "rolled_back"
	if confirm {
		v.State = "confirmed"
		h.network.IPv4 = h.desired
	}
	h.pending = nil
	return v, nil
}
func browserHostClient(t *testing.T) *systemhost.Client {
	t.Helper()
	dir, e := os.MkdirTemp("", "aigw-browser-host-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agent.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	agent := &browserHost{network: systemhost.Network{Interface: "eth0", Revision: strings.Repeat("a", 64), IPv4: systemhost.IPv4{Method: "auto", Addresses: []string{}, DNS: []string{}, AutomaticDNS: true}}}
	server := &http.Server{Handler: systemhost.Handler(agent)}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client, e := systemhost.NewClient(socket)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(client.Close)
	return client
}
