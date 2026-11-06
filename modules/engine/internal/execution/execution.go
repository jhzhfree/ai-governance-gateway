// Package execution defines the engine-independent M0 execution boundary.
// Authorization and durable accounting must be added before exposing inference.
package execution

import (
	"context"
	"encoding/json"
	"errors"
)

var (
	ErrInvalidRequest = errors.New("invalid execution request")
	ErrUpstream       = errors.New("upstream execution failed")
)

type Request struct {
	RequestID string
	AttemptID string
	Body      json.RawMessage
}

// Usage is evidence, never a monetary charge. Missing evidence remains nil.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	Evidence     json.RawMessage
}

type Event struct {
	RequestID string
	AttemptID string
	Payload   json.RawMessage
	Usage     *Usage
}

// Emit is synchronous: callers own backpressure and must honor ctx cancellation.
// A returned error stops execution; implementations must not silently discard it.
type Emit func(ctx context.Context, event Event) error

type Executor interface {
	Execute(context.Context, Request, Emit) error
	Close()
}
