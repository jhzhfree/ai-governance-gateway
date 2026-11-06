package gatewayapp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// requestGate prevents new requests from joining shutdown and retains database
// resources until all admitted handlers finish their accounting/audit cleanup.
type requestGate struct {
	next     http.Handler
	mu       sync.Mutex
	active   int
	stopping bool
	done     chan struct{}
}

func newRequestGate(next http.Handler) *requestGate {
	return &requestGate{next: next, done: make(chan struct{})}
}
func (g *requestGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	if g.stopping {
		g.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	g.active++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.active--
		if g.stopping && g.active == 0 {
			close(g.done)
		}
		g.mu.Unlock()
	}()
	g.next.ServeHTTP(w, r)
}
func (g *requestGate) stop() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.stopping {
		g.stopping = true
		if g.active == 0 {
			close(g.done)
		}
	}
	return g.done
}
func shutdownServer(server *http.Server, gate *requestGate, cancelResources context.CancelFunc, grace, cleanup time.Duration) error {
	done := gate.stop()
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	err := server.Shutdown(ctx)
	cancel()
	if err != nil {
		cancelResources()
		_ = server.Close()
	}
	finish, stop := context.WithTimeout(context.Background(), cleanup)
	defer stop()
	select {
	case <-done:
	case <-finish.Done():
		return errors.New("gateway handler cleanup exceeded deadline")
	}
	if err != nil {
		return errors.New("gateway shutdown exceeded graceful deadline; active requests canceled")
	}
	return nil
}
