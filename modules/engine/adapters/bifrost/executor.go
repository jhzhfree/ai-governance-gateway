// Package bifrost adapts the pinned upstream SDK. This M0 adapter supports
// OpenAI chat only and is not an authenticated, production gateway.
package bifrost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	core "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

const MaxRequestBytes = 1 << 20

type Config struct {
	// Supplier selects fixed server-owned mainland wire behavior. Empty keeps
	// generic OpenAI Chat. No client request field can change this selection.
	Supplier string
	// BaseURL is the origin, optionally with a proxy prefix, without /v1.
	// The OpenAI provider appends /v1/chat/completions itself.
	BaseURL string
	APIKey  string `json:"-"`
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
	client               *core.Bifrost
	streamHTTP           *http.Client
	streamURL, streamKey string
	model                string
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
	return &Executor{streamHTTP: streamClient(cfg.TestLoopback), profile: profile, streamURL: strings.TrimRight(cfg.BaseURL, "/") + profile.path, streamKey: cfg.APIKey, client: client, model: cfg.Model, maxOutputTokens: cfg.MaxOutputTokens, lifecycle: newLifecycle(ctx, cfg.MaxConcurrent)}, nil
}

func (e *Executor) Close() {
	e.lifecycle.close(func() { e.streamHTTP.CloseIdleConnections(); e.client.Shutdown() })
}

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
	if wire.Stream != nil && *wire.Stream {
		return e.stream(ctx, req, maxOutput, emit)
	}
	bctx, cancel := schemas.NewBifrostContextWithCancel(ctx)
	defer cancel()
	bctx.SetValue(schemas.BifrostContextKeyRequestID, req.AttemptID)
	// Capture raw provider frames solely for bounded evidence validation. They
	// are removed before events leave this adapter.
	converted := wire.ToBifrostChatRequest(bctx)
	converted.Provider = schemas.OpenAI
	converted.Model = e.model
	converted.Fallbacks = nil
	if e.profile.id != "" {
		body, err := e.profile.wireBody(req.Body, false)
		if err != nil {
			return execution.ErrInvalidRequest
		}
		converted.RawRequestBody = body
		bctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	}
	evidence := &usageEvidence{}
	resp, upstreamErr := e.client.ChatCompletionRequest(bctx, converted)
	if upstreamErr != nil {
		return classify(ctx)
	}
	if resp == nil {
		return execution.ErrUpstream
	}
	return deliver(ctx, req, resp, maxOutput, evidence, emit)
}

func classify(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Provider errors may contain echoed content or credentials. Do not expose them.
	return execution.ErrUpstream
}

func deliver(ctx context.Context, req execution.Request, resp *schemas.BifrostChatResponse, maxOutput int, evidence *usageEvidence, emit execution.Emit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := evidence.observe(resp.ExtraFields.RawResponse, maxOutput); err != nil {
		return err
	}
	// The SDK accepts Chat responses only with HTTP 200. Do not retain raw
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
	return emit(ctx, execution.Event{UpstreamStatus: 200, RequestID: req.RequestID, AttemptID: req.AttemptID, Payload: data, Usage: usage})
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
	var custom *schemas.CustomProviderConfig
	if profile, ok := supplier(a.cfg.Supplier); ok && profile.id != "" {
		custom = &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, RequestPathOverrides: map[schemas.RequestType]string{schemas.ChatCompletionRequest: profile.path}}
	}
	return &schemas.ProviderConfig{
		CustomProviderConfig:     custom,
		SendBackRawResponse:      true,
		NetworkConfig:            schemas.NetworkConfig{BaseURL: a.cfg.BaseURL, MaxRetries: 0, DefaultRequestTimeoutInSeconds: 10, StreamIdleTimeoutInSeconds: 3, RetryBackoffInitial: time.Millisecond, RetryBackoffMax: time.Millisecond, AllowPrivateNetwork: a.cfg.TestLoopback},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: a.cfg.MaxConcurrent, BufferSize: a.cfg.MaxConcurrent * 2},
	}, nil
}

func (*Executor) String() string   { return "[execution engine; credentials redacted]" }
func (*Executor) GoString() string { return "[execution engine; credentials redacted]" }
