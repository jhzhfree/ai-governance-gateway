package systemhost

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// A local agent can fail midway through a write. Never accept a valid prefix
// of an incomplete/oversized response as evidence that the operation succeeded.
func TestAgentRejectsAmbiguousResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"valid", `{"id":"synthetic-operation","state":"confirmed","interface":"eth0"}`, 200, nil},
		{"apply_valid", `{"id":"synthetic-operation","state":"pending","interface":"eth0","deadline":"2026-11-07T01:00:00Z"}`, 200, nil},
		{"apply_wrong_interface", `{"id":"synthetic-operation","state":"pending","interface":"eth1","deadline":"2026-11-07T01:00:00Z"}`, 200, ErrUnavailable},
		{"apply_no_deadline", `{"id":"synthetic-operation","state":"pending","interface":"eth0"}`, 200, ErrUnavailable},
		{"null", `null`, 200, ErrUnavailable},
		{"empty", `{}`, 200, ErrUnavailable},
		{"wrong_operation", `{"id":"another-operation","state":"confirmed","interface":"eth0"}`, 200, ErrUnavailable},
		{"wrong_state", `{"id":"synthetic-operation","state":"pending","interface":"eth0"}`, 200, ErrUnavailable},
		{"trailing_json", `{"state":"confirmed"}{}`, 200, ErrUnavailable},
		{"trailing_garbage", `{"state":"confirmed"}broken`, 200, ErrUnavailable},
		{"truncated", `{"state":"confirmed"`, 200, ErrUnavailable},
		{"oversized_whitespace", `{"state":"confirmed"}` + strings.Repeat(" ", 1<<20), 200, ErrUnavailable},
		{"conflict", `{}`, 409, ErrConflict},
		{"redirect", `{}`, 307, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "aigw-response-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			socket := filepath.Join(dir, "agent.sock")
			l, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Accept-Encoding") != "" {
					t.Error("compression must remain disabled")
				}
				w.Header().Set("Location", "http://host-agent/changes")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})}
			go srv.Serve(l)
			t.Cleanup(func() { srv.Close() })
			c, err := NewClient(socket)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(c.Close)
			if strings.HasPrefix(tc.name, "apply_") {
				_, err = c.Apply(context.Background(), Change{ID: "synthetic-operation", Interface: "eth0"})
			} else {
				_, err = c.Finish(context.Background(), "synthetic-operation", true)
			}
			if err != tc.want {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if calls.Load() != 1 {
				t.Fatal("mutation was replayed")
			}
		})
	}
}
