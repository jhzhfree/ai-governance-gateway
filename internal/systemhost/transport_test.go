package systemhost

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentUnixTransportAndUnsupportedChanges(t *testing.T) {
	dir, e := os.MkdirTemp("", "aigw-agent-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agent.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: Handler(&ReadOnly{Monitor: NewMonitor(), Node: "synthetic"})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client, e := NewClient(socket)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(client.Close)
	v, e := client.Snapshot(context.Background())
	if e != nil || v.Node != "synthetic" || v.NetworkEnabled || v.Metrics.MemoryTotal == 0 {
		t.Fatal("Unix snapshot failed", e)
	}
	if _, e := client.Apply(context.Background(), Change{}); e == nil {
		t.Fatal("read-only agent accepted mutation")
	}
	if _, e := client.Finish(context.Background(), "../../unsafe", true); e != ErrInvalid {
		t.Fatal("untrusted path accepted")
	}
}
