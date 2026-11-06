package gatewayapp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForcedShutdownWaitsForHandlerCleanup(t *testing.T) {
	resources, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, cleaned := make(chan struct{}), make(chan struct{})
	gate := newRequestGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cleaned) }))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: gate, BaseContext: func(net.Listener) context.Context { return resources }}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		c := &http.Client{Timeout: time.Second}
		r, e := c.Get("http://" + listener.Addr().String())
		if e == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler not admitted")
	}
	if err := shutdownServer(server, gate, cancel, 20*time.Millisecond, time.Second); err == nil {
		t.Fatal("forced shutdown not reported")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("shutdown returned before handler cleanup")
	}
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal("shutdown admitted new work")
	}
	<-requestDone
	<-finished
	if err := shutdownServer(server, gate, cancel, time.Second, time.Second); err != nil {
		t.Fatal("repeated shutdown failed")
	}
}
