// Package bifrost adapts the pinned upstream SDK. This M0 adapter supports
// reviewed OpenAI-compatible Chat and text Embeddings only and is not an authenticated, production gateway.
package bifrost

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

const MaxRequestBytes = 1 << 20

type Config struct {
	// API selects a server-owned operation; empty is legacy Chat.
	API string
	// Supplier selects fixed server-owned mainland wire behavior. Empty keeps
	// generic OpenAI Chat. No client request field can change this selection.
	Supplier string
	// BaseURL is the origin, optionally with a proxy prefix, without /v1.
	// The reviewed profile appends its fixed Chat/Embeddings path.
	BaseURL string
	APIKey  string `json:"-"`
	Model   string
	// MaxOutputTokens is a server-owned ceiling, never a client override.
	// Chat zero selects 4096; other values must be 16..1,000,000.
	// Embeddings requires zero.
	MaxOutputTokens int
	// MaxConcurrent bounds active executions, including streams and consumers.
	// Zero selects 4. There is no adapter-side waiting queue.
	MaxConcurrent int
	// TestLoopback permits only literal loopback IPs over HTTP for local fixtures.
	TestLoopback bool
}

type Executor struct {
	streamHTTP           *http.Client
	streamURL, streamKey string
	api, model           string
	maxOutputTokens      int
	lifecycle            *lifecycle
	profile              supplierProfile
}

var _ execution.Executor = (*Executor)(nil)

func New(ctx context.Context, cfg Config) (*Executor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, profile, err := resolveSupplier(cfg)
	if err != nil {
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
	if cfg.TestLoopback && !loopback {
		return nil, errors.New("test transport requires literal loopback")
	}
	if cfg.Model == "" || strings.Contains(cfg.Model, "/") || cfg.APIKey == "" {
		return nil, errors.New("model and upstream credential required")
	}
	if cfg.API != "embeddings" && cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = 4096
	}
	if (cfg.API == "embeddings" && cfg.MaxOutputTokens != 0) || (cfg.API != "embeddings" && (cfg.MaxOutputTokens < 16 || cfg.MaxOutputTokens > 1000000)) {
		return nil, errors.New("invalid output token ceiling")
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 1024 {
		return nil, errors.New("invalid execution concurrency")
	}
	e := &Executor{api: cfg.API, streamHTTP: streamClient(cfg.TestLoopback), profile: profile, streamURL: strings.TrimRight(cfg.BaseURL, "/") + profile.path, streamKey: cfg.APIKey, model: cfg.Model, maxOutputTokens: cfg.MaxOutputTokens, lifecycle: newLifecycle(ctx, cfg.MaxConcurrent)}
	return e, nil
}
func (e *Executor) Close() {
	e.lifecycle.close(func() {
		e.streamHTTP.CloseIdleConnections()
	})
}

func (e *Executor) Execute(ctx context.Context, req execution.Request, emit execution.Emit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.RequestID == "" || req.AttemptID == "" || emit == nil || len(req.Body) == 0 || len(req.Body) > MaxRequestBytes {
		return execution.ErrInvalidRequest
	}
	if e.api == "messages" {
		return e.messagesExecute(ctx, req, emit)
	}
	if e.api == "embeddings" {
		return e.embedding(ctx, req, emit)
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
	if wire.Stream != nil && *wire.Stream {
		return e.stream(ctx, req, maxOutput, emit)
	}
	return e.chatJSON(ctx, req, maxOutput, emit)
}

func deliver(ctx context.Context, req execution.Request, resp *schemas.BifrostChatResponse, maxOutput int, evidence *usageEvidence, emit execution.Emit) error {
	if err := evidence.observe(resp.ExtraFields.RawResponse, maxOutput); err != nil {
		return err
	}
	// The receiver accepts only HTTP 200. Do not retain raw
	// request/response content in event payloads or logs.
	resp.ExtraFields.RawResponse = nil
	resp.ExtraFields.RawRequest = nil
	var usage *execution.Usage
	if u := resp.Usage; u != nil && (u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0) {
		raw := evidence.usage
		if raw == nil || evidence.emitted || raw.InputTokens != u.PromptTokens || raw.OutputTokens != u.CompletionTokens || raw.TotalTokens != u.TotalTokens {
			return execution.ErrUpstream
		}
		evidence.emitted = true
		usage = raw
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return execution.ErrUpstream
	}
	return emit(ctx, execution.Event{Completed: true, UpstreamStatus: 200, RequestID: req.RequestID, AttemptID: req.AttemptID, Payload: data, Usage: usage})
}

func (*Executor) String() string   { return "[execution engine; credentials redacted]" }
func (*Executor) GoString() string { return "[execution engine; credentials redacted]" }
