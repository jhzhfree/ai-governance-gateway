package gatewaybridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/gatewaybridge"
)

type executorFunc func(context.Context, execution.Request, execution.Emit) error

func (f executorFunc) Execute(c context.Context, r execution.Request, e execution.Emit) error {
	return f(c, r, e)
}
func (executorFunc) Close() {}
func bridgeFixture(t *testing.T, f executorFunc) (*gatewaybridge.Binding, *provider.Route, provider.Request) {
	t.Helper()
	cfg := testutil.MainlandConfig()
	cfg.Models[0].MaxOutputTokens = 32
	catalog, err := provider.New(cfg, func(string) string { return "synthetic" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := catalog.Find("aliyun/fixture")
	b, err := gatewaybridge.New(route, f)
	if err != nil {
		t.Fatal(err)
	}
	return b, route, provider.Request{Model: "aliyun/fixture", MaxTokens: 32, Messages: []provider.Message{{Role: "user", Content: "synthetic"}}}
}
func event(r execution.Request) execution.Event {
	return execution.Event{UpstreamStatus: 200, RequestID: r.RequestID, AttemptID: r.AttemptID, Payload: json.RawMessage(`{"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)}
}
func TestBodyCloseReclaimsBlockedProducer(t *testing.T) {
	returned := make(chan struct{})
	b, route, req := bridgeFixture(t, func(ctx context.Context, r execution.Request, emit execution.Emit) error {
		defer close(returned)
		return emit(ctx, event(r))
	})
	resp, err := b.Open(context.Background(), route, req, "request", "attempt")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _ = resp.Body.Close(); _ = resp.Body.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("body close did not reclaim producer")
	}
	select {
	case <-returned:
	default:
		t.Fatal("producer survived close")
	}
}
func TestBridgeDoesNotPromotePayloadUsageOrHideLateErrors(t *testing.T) {
	for _, late := range []bool{false, true} {
		b, route, req := bridgeFixture(t, func(ctx context.Context, r execution.Request, emit execution.Emit) error {
			if err := emit(ctx, event(r)); err != nil {
				return err
			}
			if late {
				return errors.New("synthetic-private-error")
			}
			return nil
		})
		resp, err := b.Open(context.Background(), route, req, "request", "attempt")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if late && !errors.Is(err, execution.ErrUpstream) {
			t.Fatal("late failure became successful EOF")
		}
		if !late && err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "usage") || (err != nil && strings.Contains(err.Error(), "private")) {
			t.Fatal("untrusted usage or error leaked")
		}
	}
}
func TestBridgeRejectsUncorrelatedAndDuplicateEvents(t *testing.T) {
	for _, mode := range []string{"identity", "status", "second response", "invalid usage", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			b, route, req := bridgeFixture(t, func(ctx context.Context, r execution.Request, emit execution.Emit) error {
				e := event(r)
				switch mode {
				case "identity":
					e.AttemptID = "different"
				case "status":
					e.UpstreamStatus = 0
				case "invalid usage":
					e.Usage = &execution.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 3}
				case "oversized":
					e.Payload = json.RawMessage(strings.Repeat("x", (1<<20)+1))
				}
				if err := emit(ctx, e); err != nil {
					return err
				}
				return emit(ctx, e)
			})
			resp, err := b.Open(context.Background(), route, req, "request", "attempt")
			if err == nil {
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			if err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
}
