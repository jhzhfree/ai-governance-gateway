package gateway

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
)

type attemptTrace struct {
	sink      execution.Recorder
	id        string
	result    execution.Result
	done      bool
	ctx       context.Context
	report    func(string, string)
	requestID string
}

func (h *Handler) beginAttempt(ctx context.Context, id string, req Request, provider, region string) (*attemptTrace, error) {
	if h.Attempts == nil {
		return nil, errors.New("execution recorder unavailable")
	}
	start := execution.Start{AttemptID: rand.Text(), RequestID: id, Model: req.Model, Provider: provider, Region: region, Stream: req.Stream}
	if err := h.Attempts.Begin(ctx, start); err != nil {
		return nil, err
	}
	return &attemptTrace{sink: h.Attempts, id: start.AttemptID, result: execution.Result{Outcome: "failed", ErrorCode: "execution_incomplete"}, ctx: ctx, report: h.report, requestID: id}, nil
}
func (t *attemptTrace) failed(code string) { t.result.Outcome = "failed"; t.result.ErrorCode = code }
func (t *attemptTrace) status(status int) {
	if status >= 100 && status <= 599 {
		t.result.UpstreamStatus = &status
	}
}
func (t *attemptTrace) usage(input, output int64) {
	t.result.InputTokens = &input
	t.result.OutputTokens = &output
}
func (t *attemptTrace) persist(v execution.Result) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return t.sink.Finish(ctx, t.id, v)
}
func (t *attemptTrace) complete() error {
	v := t.result
	v.Outcome = "completed"
	v.ErrorCode = ""
	if err := t.persist(v); err != nil {
		t.failed("metadata_finalize_failed")
		return err
	}
	t.done = true
	return nil
}
func (t *attemptTrace) abort() {
	if t.done {
		return
	}
	if errors.Is(t.ctx.Err(), context.Canceled) {
		t.result.Outcome = "canceled"
		t.result.ErrorCode = "client_canceled"
	} else if errors.Is(t.ctx.Err(), context.DeadlineExceeded) {
		t.failed("request_timeout")
	}
	if err := t.persist(t.result); err != nil {
		t.report(t.requestID, "metadata_finalize_failed")
	}
}
