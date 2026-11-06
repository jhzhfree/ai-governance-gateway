package bifrost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/internal/execution"
)

func liveStream(w http.ResponseWriter, r *http.Request, closed chan<- struct{}) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
	w.(http.Flusher).Flush()
	select {
	case <-r.Context().Done():
		if closed != nil {
			closed <- struct{}{}
		}
	case <-time.After(6 * time.Second):
	}
}

func TestOverloadRejectsBeforeDispatchAndReleasesCapacity(t *testing.T) {
	var calls atomic.Int32
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var wire struct {
			Stream bool `json:"stream"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil {
			t.Error("invalid upstream payload")
		}
		if !wire.Stream {
			respond(w, true)
			return
		}
		liveStream(w, r, nil)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{}, 4)
	finished := make(chan error, 4)
	for i := 0; i < 4; i++ {
		req := request(strings.TrimSuffix(body, "}") + `,"stream":true}`)
		req.RequestID = fmt.Sprintf("request-%d", i)
		req.AttemptID = fmt.Sprintf("attempt-%d", i)
		go func() {
			finished <- e.Execute(ctx, req, func(context.Context, execution.Event) error { ready <- struct{}{}; return nil })
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("streams did not start")
		}
	}
	if err := e.Execute(context.Background(), request(body), discard); !errors.Is(err, execution.ErrOverloaded) {
		t.Fatalf("overload outcome: %v", err)
	}
	if calls.Load() != 4 {
		t.Fatalf("overloaded request dispatched: %d", calls.Load())
	}
	cancel()
	for i := 0; i < 4; i++ {
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("stream did not stop")
		}
	}
	if err := e.Execute(context.Background(), request(body), discard); err != nil {
		t.Fatalf("capacity not released: %v", err)
	}
	if calls.Load() != 5 {
		t.Fatalf("unexpected physical attempts: %d", calls.Load())
	}
}

func TestConcurrentCloseCancelsStreamAndRejectsNewRequests(t *testing.T) {
	closed := make(chan struct{}, 1)
	var calls atomic.Int32
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); liveStream(w, r, closed) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{}, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- e.Execute(ctx, request(strings.TrimSuffix(body, "}")+`,"stream":true}`), func(context.Context, execution.Event) error { ready <- struct{}{}; return nil })
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("stream did not start")
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.Close() }()
	}
	allClosed := make(chan struct{})
	go func() { wg.Wait(); close(allClosed) }()
	select {
	case <-allClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not complete")
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight outcome: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("execution not released")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("upstream stayed open")
	}
	if err := e.Execute(context.Background(), request(body), discard); !errors.Is(err, execution.ErrClosed) {
		t.Fatalf("closed executor admitted request: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("request dispatched after shutdown")
	}
}

func TestInvalidExecutionConcurrencyRejected(t *testing.T) {
	for _, limit := range []int{-1, 1025} {
		e, err := adapter.New(context.Background(), adapter.Config{BaseURL: "https://example.invalid", APIKey: "synthetic", Model: model, MaxConcurrent: limit})
		if err == nil {
			e.Close()
			t.Fatalf("invalid concurrency accepted: %d", limit)
		}
	}
}
