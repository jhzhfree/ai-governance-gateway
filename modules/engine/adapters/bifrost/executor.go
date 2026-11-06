// Package bifrost adapts the pinned upstream SDK. This M0 adapter supports
// OpenAI chat only and is not an authenticated, production gateway.
package bifrost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/internal/execution"
	core "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

const MaxRequestBytes = 1 << 20

type Config struct {
	// BaseURL is the origin, optionally with a proxy prefix, without /v1.
	// The OpenAI provider appends /v1/chat/completions itself.
	BaseURL string
	APIKey  string
	Model   string
	// MaxOutputTokens is a server-owned ceiling, never a client override.
	// Zero selects 4096. Values outside 16..1,000,000 are rejected.
	MaxOutputTokens int
	// MaxConcurrent bounds active executions, including streams and consumers.
	// Zero selects 4. There is no adapter-side waiting queue.
	MaxConcurrent int
	// TestLoopback permits only literal loopback IPs over HTTP for local fixtures.
	TestLoopback bool
}

type Executor struct {
	client          *core.Bifrost
	model           string
	maxOutputTokens int
	lifecycle       *lifecycle
}

var _ execution.Executor = (*Executor)(nil)

func New(ctx context.Context, cfg Config) (*Executor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid upstream endpoint")
	}
	loopback := net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(cfg.TestLoopback && loopback && u.Scheme == "http") {
		return nil, errors.New("upstream requires HTTPS; test HTTP requires a literal loopback address")
	}
	if cfg.Model == "" || strings.Contains(cfg.Model, "/") || cfg.APIKey == "" {
		return nil, errors.New("model and upstream credential required")
	}
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = 4096
	}
	if cfg.MaxOutputTokens < 16 || cfg.MaxOutputTokens > 1000000 {
		return nil, errors.New("invalid output token ceiling")
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 1024 {
		return nil, errors.New("invalid execution concurrency")
	}
	a := &account{cfg: cfg}
	client, err := core.Init(ctx, schemas.BifrostConfig{Account: a, Logger: core.NewNoOpLogger(), InitialPoolSize: 8, DropExcessRequests: true})
	if err != nil {
		return nil, errors.New("execution engine initialization failed")
	}
	return &Executor{client: client, model: cfg.Model, maxOutputTokens: cfg.MaxOutputTokens, lifecycle: newLifecycle(ctx, cfg.MaxConcurrent)}, nil
}

func (e *Executor) Close() { e.lifecycle.close(e.client.Shutdown) }

func (e *Executor) Execute(ctx context.Context, req execution.Request, emit execution.Emit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.RequestID == "" || req.AttemptID == "" || emit == nil || len(req.Body) == 0 || len(req.Body) > MaxRequestBytes {
		return execution.ErrInvalidRequest
	}
	maxOutput, valid := validateRequest(req.Body, e.model, e.maxOutputTokens)
	if !valid {
		return execution.ErrInvalidRequest
	}
	var wire openai.OpenAIChatRequest
	if err := json.Unmarshal(req.Body, &wire); err != nil || wire.Model != e.model || len(wire.Messages) == 0 {
		return execution.ErrInvalidRequest
	}
	ctx, release, err := e.lifecycle.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	bctx, cancel := schemas.NewBifrostContextWithCancel(ctx)
	defer cancel()
	bctx.SetValue(schemas.BifrostContextKeyRequestID, req.AttemptID)
	converted := wire.ToBifrostChatRequest(bctx)
	converted.Provider = schemas.OpenAI
	converted.Model = e.model
	converted.Fallbacks = nil
	stream := wire.Stream != nil && *wire.Stream
	if !stream {
		resp, upstreamErr := e.client.ChatCompletionRequest(bctx, converted)
		if upstreamErr != nil {
			return classify(ctx)
		}
		if resp == nil {
			return execution.ErrUpstream
		}
		return deliver(ctx, req, resp, maxOutput, emit)
	}
	chunks, upstreamErr := e.client.ChatCompletionStreamRequest(bctx, converted)
	if upstreamErr != nil {
		return classify(ctx)
	}
	if chunks == nil {
		return execution.ErrUpstream
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				return ctx.Err()
			}
			if chunk == nil {
				continue
			}
			if chunk.BifrostError != nil {
				return classify(ctx)
			}
			if chunk.BifrostChatResponse != nil {
				if err := deliver(ctx, req, chunk.BifrostChatResponse, maxOutput, emit); err != nil {
					return err
				}
			}
		}
	}
}

func classify(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Provider errors may contain echoed content or credentials. Do not expose them.
	return execution.ErrUpstream
}

func deliver(ctx context.Context, req execution.Request, resp *schemas.BifrostChatResponse, maxOutput int, emit execution.Emit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return execution.ErrUpstream
	}
	var usage *execution.Usage
	if u := resp.Usage; u != nil {
		if u.PromptTokens < 0 || u.PromptTokens > 2000000 || u.CompletionTokens < 0 || u.CompletionTokens > maxOutput || u.TotalTokens < 0 {
			return execution.ErrUpstream
		}
		// The pinned SDK synthesizes an all-zero usage object when an upstream
		// stream omits usage. Without provenance, even a genuine all-zero usage
		// must remain unknown rather than authorize a zero-cost settlement.
		if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 {
			if u.PromptTokens == 0 || u.TotalTokens != u.PromptTokens+u.CompletionTokens {
				return execution.ErrUpstream
			}
			evidence, err := json.Marshal(u)
			if err != nil {
				return execution.ErrUpstream
			}
			usage = &execution.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens, Evidence: evidence}
		}
	}
	return emit(ctx, execution.Event{RequestID: req.RequestID, AttemptID: req.AttemptID, Payload: data, Usage: usage})
}

type account struct{ cfg Config }

func (a *account) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}
func (a *account) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if p != schemas.OpenAI {
		return nil, fmt.Errorf("provider not configured")
	}
	return []schemas.Key{{ID: "m0-provider", Name: "m0-provider", Value: *schemas.NewSecretVar(a.cfg.APIKey), Models: schemas.WhiteList{a.cfg.Model}, Weight: 1}}, nil
}
func (a *account) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.OpenAI {
		return nil, fmt.Errorf("provider not configured")
	}
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.NetworkConfig{BaseURL: a.cfg.BaseURL, MaxRetries: 0, DefaultRequestTimeoutInSeconds: 10, StreamIdleTimeoutInSeconds: 3, RetryBackoffInitial: time.Millisecond, RetryBackoffMax: time.Millisecond, AllowPrivateNetwork: a.cfg.TestLoopback},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: a.cfg.MaxConcurrent, BufferSize: a.cfg.MaxConcurrent * 2},
	}, nil
}
