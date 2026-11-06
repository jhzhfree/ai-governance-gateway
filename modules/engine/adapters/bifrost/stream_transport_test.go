package bifrost_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func TestStrictStreamTransportRefusesRedirectAndErrors(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, mode := range []string{"503", "redirect", "media", "encoding"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "503":
					w.WriteHeader(503)
				case "redirect":
					w.Header().Set("Location", target.URL)
					w.WriteHeader(307)
				case "media":
					w.Header().Set("Content-Type", "application/json")
				case "encoding":
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Content-Encoding", "gzip")
				}
				_, _ = io.WriteString(w, "synthetic-private-error")
			})
			err := e.Execute(context.Background(), request(strings.TrimSuffix(body, "}")+`,"stream":true}`), func(context.Context, execution.Event) error { t.Error("invalid transport emitted event"); return nil })
			if !errors.Is(err, execution.ErrUpstream) || calls.Load() != 1 || redirected.Load() != 0 {
				t.Fatalf("transport error policy failed: %v attempts=%d redirects=%d", err, calls.Load(), redirected.Load())
			}
		})
	}
}
func TestStrictStreamIdleDeadlineClosesUpstream(t *testing.T) {
	ended := make(chan struct{})
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(ended)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	err := e.Execute(ctx, request(strings.TrimSuffix(body, "}")+`,"stream":true}`), discard)
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatalf("idle deadline not enforced: %v", err)
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("idle stream leaked upstream connection")
	}
}
func TestStrictStreamTLSAndDestinationPolicy(t *testing.T) {
	for _, testLoopback := range []bool{false, true} {
		t.Run(fmt.Sprint(testLoopback), func(t *testing.T) {
			var connections, calls atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					connections.Add(1)
				}
			}
			server.StartTLS()
			defer server.Close()
			e, err := adapter.New(context.Background(), adapter.Config{BaseURL: server.URL, APIKey: "synthetic", Model: "gpt-4o-mini", TestLoopback: testLoopback})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			err = e.Execute(context.Background(), request(strings.TrimSuffix(body, "}")+`,"stream":true}`), discard)
			if !errors.Is(err, execution.ErrUpstream) || calls.Load() != 0 {
				t.Fatal("unsafe TLS/destination accepted")
			}
			if !testLoopback && connections.Load() != 0 {
				t.Fatal("production stream dialed loopback")
			}
			if testLoopback && connections.Load() == 0 {
				t.Fatal("TLS trust refusal not exercised")
			}
		})
	}
}
