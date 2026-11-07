// Package newapibridge implements the explicit newapi execution mode.
// Missing authenticated execution evidence always retains uncertain funds.
package newapibridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

var ErrUnavailable = errors.New("new-api execution unavailable")
var ErrRecoveryPending = errors.New("new-api recovery requires reconciliation")
var ErrInvalid = errors.New("unsupported new-api execution request")
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./:-]{0,127}$`)

type Config struct {
	Origin            string
	ServiceToken      string `json:"-"`
	Models            map[string]string
	Channels          map[string]int
	MaxConcurrent     int
	Journal           *Journal `json:"-"`
	AllowLoopbackHTTP bool
}

func (Config) String() string   { return "[new-api configuration; credentials redacted]" }
func (Config) GoString() string { return "[new-api configuration; credentials redacted]" }

type Executor struct {
	origin, token       string
	recoveryMu          sync.Mutex
	recoveryCursor      string
	recoveryCycleFailed bool
	journal             *Journal
	models              map[string]string
	channels            map[string]int
	client              *http.Client
	transport           *http.Transport
	root                context.Context
	cancel              context.CancelFunc
	mu                  sync.Mutex
	closed              bool
	active              sync.WaitGroup
	slots               chan struct{}
}

var _ gateway.ProviderExecutor = (*Executor)(nil)

func (*Executor) String() string   { return "[new-api executor; credentials redacted]" }
func (*Executor) GoString() string { return "[new-api executor; credentials redacted]" }

func New(cfg Config) (*Executor, error) {
	u, err := url.Parse(cfg.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || strings.Contains(cfg.Origin, "#") || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return nil, ErrInvalid
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !cfg.AllowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
			return nil, ErrInvalid
		}
	}
	if len(cfg.ServiceToken) < 16 || len(cfg.ServiceToken) > 256 || cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 1024 || len(cfg.Models) < 1 || len(cfg.Models) > 32 {
		return nil, ErrInvalid
	}
	for _, r := range cfg.ServiceToken {
		if r < 33 || r > 126 {
			return nil, ErrInvalid
		}
	}
	models := map[string]string{}
	channels := map[string]int{}
	for alias, model := range cfg.Models {
		if !modelPattern.MatchString(alias) || !modelPattern.MatchString(model) {
			return nil, ErrInvalid
		}
		models[alias] = model
		if cfg.Channels[alias] > 0 {
			channels[alias] = cfg.Channels[alias]
		}
	}
	root, cancel := context.WithCancel(context.Background())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.MaxResponseHeaderBytes = 16 << 10
	transport.MaxConnsPerHost = cfg.MaxConcurrent
	transport.ResponseHeaderTimeout = 120 * time.Second
	return &Executor{journal: cfg.Journal, origin: strings.TrimSuffix(cfg.Origin, "/"), token: cfg.ServiceToken, models: models, channels: channels, root: root, cancel: cancel, slots: make(chan struct{}, cfg.MaxConcurrent), transport: transport, client: &http.Client{Transport: transport, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (e *Executor) Validate(route *provider.Route, req gateway.Request) error {
	if route == nil || !route.Ready() || e.models[req.Model] == "" || !provider.ValidateRequest(req) {
		return ErrInvalid
	}
	alias, _ := route.ExecutionModel()
	if req.Model != alias {
		return ErrInvalid
	}
	if _, err := route.Bound(req.MaxTokens); err != nil {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrUnavailable
	}
	return nil
}
func (e *Executor) Open(ctx context.Context, route *provider.Route, req gateway.Request, requestID, attemptID string) (*http.Response, error) {
	if err := e.Validate(route, req); err != nil {
		return nil, err
	}
	if !idPattern.MatchString(requestID) || !idPattern.MatchString(attemptID) {
		return nil, ErrInvalid
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, ErrUnavailable
	}
	select {
	case e.slots <- struct{}{}:
	default:
		e.mu.Unlock()
		return nil, ErrUnavailable
	}
	e.active.Add(1)
	e.mu.Unlock()
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.root, cancel)
	var once sync.Once
	release := func() { once.Do(func() { stop(); cancel(); <-e.slots; e.active.Done() }) }
	payload := map[string]any{"model": e.models[req.Model], "messages": req.Messages, "max_tokens": req.MaxTokens, "stream": req.Stream}
	if req.Stream {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		release()
		return nil, ErrInvalid
	}
	r, err := http.NewRequestWithContext(call, http.MethodPost, e.origin+"/internal/governance/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		release()
		return nil, ErrInvalid
	}
	r.Header.Set("Authorization", "Bearer "+e.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if req.Stream {
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("X-Aigw-Stream-Version", "2")
	}
	if channelID := e.channels[req.Model]; channelID > 0 {
		r.Header.Set("X-Aigw-Channel-Id", strconv.Itoa(channelID))
	}
	r.Header.Set("X-Aigw-Request-Id", requestID)
	r.Header.Set("X-Aigw-Attempt-Id", attemptID)
	// Only the dedicated internal service token is sent; route vendor credentials
	// and caller Authorization/Cookie/forwarded headers are never copied.
	if e.journal != nil {
		digest := sha256.Sum256(append([]byte(strconv.Itoa(e.channels[req.Model])+"\x00"), data...))
		if err := e.journal.Bind(call, e.origin, e.models[req.Model], requestID, attemptID, hex.EncodeToString(digest[:]), route, req.MaxTokens); err != nil {
			release()
			return nil, ErrUnavailable
		}
	}
	res, err := e.client.Do(r)
	if err != nil {
		release()
		return nil, ErrUnavailable
	}
	version := res.Header.Get("X-Aigw-Evidence-Version")
	live := req.Stream && version == "2"
	valid := res.StatusCode == http.StatusOK
	if live {
		valid = valid && strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream")
	} else {
		version = "1"
	}
	for key, want := range map[string]string{"X-Aigw-Evidence-Version": version, "X-Aigw-Request-Id": requestID, "X-Aigw-Attempt-Id": attemptID, "X-Aigw-Usage-Source": "upstream", "X-Aigw-Physical-Attempts": "1"} {
		values := res.Header.Values(key)
		valid = valid && len(values) == 1 && values[0] == want
	}
	// This is a required integration contract, not a claim stock new-api emits it.
	// Missing evidence fails closed; Handler retains uncertain funds after dispatch.
	if !valid {
		res.Body.Close()
		release()
		return nil, ErrUnavailable
	}
	var responseBody io.ReadCloser = res.Body
	if live {
		responseBody = newVerifiedStream(res)
	}
	body := &ownedBody{ReadCloser: responseBody, release: release}
	context.AfterFunc(call, func() { body.Close() })
	res.Body = body
	return res, nil
}
func (e *Executor) Close() {
	e.mu.Lock()
	e.closed = true
	e.cancel()
	e.mu.Unlock()
	e.active.Wait()
	e.transport.CloseIdleConnections()
}

type ownedBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *ownedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.Close()
	}
	return n, err
}
func (b *ownedBody) Close() error {
	var err error
	b.once.Do(func() { err = b.ReadCloser.Close(); b.release() })
	return err
}
