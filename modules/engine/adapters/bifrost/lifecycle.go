package bifrost

import (
	"context"
	"sync"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

type lifecycle struct {
	mu              sync.Mutex
	active          sync.WaitGroup
	once            sync.Once
	closing         bool
	inFlight, limit int
	ctx             context.Context
	cancel          context.CancelFunc
}

func newLifecycle(parent context.Context, limit int) *lifecycle {
	ctx, cancel := context.WithCancel(parent)
	return &lifecycle{ctx: ctx, cancel: cancel, limit: limit}
}

func (l *lifecycle) acquire(parent context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	if l.closing || l.ctx.Err() != nil {
		return nil, nil, execution.ErrClosed
	}
	if l.inFlight >= l.limit {
		return nil, nil, execution.ErrOverloaded
	}
	l.inFlight++
	l.active.Add(1)
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(l.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
		l.mu.Lock()
		l.inFlight--
		l.mu.Unlock()
		l.active.Done()
	}, nil
}

func (l *lifecycle) close(shutdown func()) {
	l.once.Do(func() {
		// Closing and Add share the lock: Wait cannot race a newly admitted
		// request. Consumers must honor their context as required by Emit.
		l.mu.Lock()
		l.closing = true
		l.cancel()
		l.mu.Unlock()
		l.active.Wait()
		shutdown()
	})
}
