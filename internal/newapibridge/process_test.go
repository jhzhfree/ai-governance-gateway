package newapibridge_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

// Helper is a distinct OS process running the real enterprise handler, journal
// and recovery worker, with synthetic authorization and an isolated test schema.
func TestNewAPIControlHelper(t *testing.T) {
	if os.Getenv("AIGW_INTEGRATION_CHILD") != "1" {
		t.Skip("subprocess fixture only")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("AIGW_INTEGRATION_DSN"))
	if err != nil {
		t.Fatal("fixture database unavailable")
	}
	defer pool.Close()
	var schema string
	if pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema) != nil || !strings.HasPrefix(schema, "poc_") {
		t.Fatal("isolated test schema required")
	}

	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].UpstreamModel = "governance-model"
		if os.Getenv("AIGW_INTEGRATION_PROTOCOL") == "anthropic" && cfg.Models[i].ID == "zhipu/fixture" {
			cfg.Models[i].ID = "reviewed-compatible/fixture"
			cfg.Models[i].Provider = "reviewed-compatible"
		}
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = os.Getenv("AIGW_INTEGRATION_REVISION")
		cfg.Models[i].AllowedTools = []string{"lookup"}
		if os.Getenv("AIGW_INTEGRATION_RESPONSES") == "1" {
			cfg.Models[i].MaxOutputTokens = 32
		}
		if os.Getenv("AIGW_INTEGRATION_CACHE") == "1" {
			cfg.Models[i].CachePricing = &provider.CachePricing{ReadPerMillion: 500000, WritePerMillion: 3000000}
			if os.Getenv("AIGW_INTEGRATION_TTL") == "1" {
				cfg.Models[i].CachePricing.WriteTTL = &provider.CacheWriteTTL{FiveMinutePerMillion: 3000000, OneHourPerMillion: 6000000}
			}
		}
		if os.Getenv("AIGW_INTEGRATION_PROTOCOL") == "embedding" {
			cfg.Models[i].API = "embeddings"
			cfg.Models[i].MaxOutputTokens = 0
			cfg.Models[i].OutputPerMillion = 0
			cfg.Models[i].AllowedTools = nil
		}
		cfg.Models[i].AllowImages = os.Getenv("AIGW_INTEGRATION_IMAGES") == "1"
		if os.Getenv("AIGW_INTEGRATION_RICH") == "1" {
			cfg.Models[i].MaxInputTokens = 1024
		}
	}
	directory, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newapibridge.NewRuntime(ctx, newapibridge.Config{Origin: os.Getenv("AIGW_INTEGRATION_ORIGIN"), ServiceToken: "sk-syntheticgovernancefixture", MaxConcurrent: 4, AllowLoopbackHTTP: true, Journal: newapibridge.NewJournal(pool)}, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	scope := budget.Scope{Organization: "org-test", Project: "project-test", Period: "2026-11", Currency: provider.Currency}
	handler := &gateway.Handler{Executor: runtime, Providers: runtime, Ledger: budget.New(pool), Attempts: execution.New(pool), Admission: testutil.PassAdmission{}, Circuits: testutil.PassCircuit{}, Authorize: func(context.Context, string, string) (budget.Scope, error) { return scope, nil }}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(os.Getenv("AIGW_INTEGRATION_READY"), []byte("http://"+listener.Addr().String()), 0600) != nil {
		t.Fatal("fixture address unavailable")
	}
	_ = http.Serve(listener, handler)
}

func TestNewAPITwoProcessRecovery(t *testing.T) {
	for _, protocol := range []string{"openai", "deepseek", "anthropic"} {
		t.Run(protocol, func(t *testing.T) { testNewAPITwoProcessRecovery(t, protocol, false, false) })
	}
}
func TestNewAPITwoProcessClientTools(t *testing.T) {
	for _, protocol := range []string{"openai", "deepseek", "anthropic"} {
		t.Run(protocol, func(t *testing.T) { testNewAPITwoProcessRecovery(t, protocol, true, false) })
	}
}
func TestNewAPITwoProcessImages(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) { testNewAPITwoProcessRecovery(t, protocol, false, true) })
	}
}
func TestNewAPITwoProcessEmbeddings(t *testing.T) {
	testNewAPITwoProcessRecovery(t, "embedding", false, false)
}
func TestNewAPITwoProcessCacheTTL(t *testing.T) {
	for _, protocol := range []string{"openai", "deepseek", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			testNewAPITwoProcessRecovery(t, protocol, false, false, processOptions{cacheTTL: true, replicated: true, multiKey: "polling"})
		})
	}
}

func TestNewAPITwoProcessCacheTariffs(t *testing.T) {
	for _, protocol := range []string{"openai", "deepseek", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			testNewAPITwoProcessRecovery(t, protocol, false, false, processOptions{cacheTariff: true})
		})
	}
	t.Run("openai-tools", func(t *testing.T) {
		testNewAPITwoProcessRecovery(t, "openai", true, false, processOptions{cacheTariff: true})
	})
}

type processOptions struct {
	cacheTTL, cacheTariff, replicated, responses, messages bool
	multiKey                                               string
}

func TestNewAPIReplicatedControlRecovery(t *testing.T) {
	for _, protocol := range []string{"anthropic", "embedding"} {
		t.Run(protocol, func(t *testing.T) {
			testNewAPITwoProcessRecovery(t, protocol, false, false, processOptions{cacheTariff: protocol == "anthropic", replicated: true})
		})
	}
}

func TestNewAPIMultiKeyRecovery(t *testing.T) {
	for i, protocol := range []string{"openai", "anthropic", "deepseek", "embedding"} {
		mode := "polling"
		if i%2 == 1 {
			mode = "random"
		}
		t.Run(protocol+"/"+mode, func(t *testing.T) {
			testNewAPITwoProcessRecovery(t, protocol, false, false, processOptions{cacheTariff: protocol != "embedding", replicated: true, multiKey: mode})
		})
	}
}

func TestNewAPIResponsesRecovery(t *testing.T) {
	for _, protocol := range []string{"openai", "deepseek", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			testNewAPITwoProcessRecovery(t, protocol, false, false, processOptions{cacheTariff: true, replicated: true, responses: true, multiKey: "polling"})
		})
	}
}

func TestNewAPIMessagesRecovery(t *testing.T) {
	for _, protocol := range []string{"openai", "deepseek", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			testNewAPITwoProcessRecovery(t, protocol, false, false, processOptions{cacheTariff: true, replicated: true, messages: true, multiKey: "polling"})
		})
	}
}

func testNewAPITwoProcessRecovery(t *testing.T, protocol string, tools, images bool, options ...processOptions) {
	var option processOptions
	if len(options) > 0 {
		option = options[0]
	}
	cache := option.cacheTariff || option.cacheTTL
	embedding := protocol == "embedding"
	expectedHeld := int64(288)
	if tools || images {
		expectedHeld = 2080
	}
	expectedSpent := int64(48)
	if images {
		expectedSpent = 108
	}
	if embedding {
		expectedHeld = 256
		expectedSpent = 12
	}
	if cache {
		expectedHeld = 416
		if option.cacheTTL {
			expectedHeld = 800
		}
		if tools || images {
			expectedHeld = 3104
		}
		expectedSpent = 45
		if protocol == "anthropic" {
			expectedSpent = 99
			if option.cacheTTL {
				expectedSpent = 117
			}
		}
	}
	if option.responses {
		expectedHeld += 32
	}
	binary := os.Getenv("TEST_NEWAPI_SERVER_BINARY")
	if binary == "" {
		t.Skip("pinned new-api integration fixture binary required")
	}
	pool, ledger, scope := testutil.Database(t)
	scope.Currency = provider.Currency
	testutil.Accounts(t, ledger, scope, 100000, 100000)
	var calls atomic.Int32
	var block atomic.Bool
	arrived := make(chan struct{})
	blockedRelease := make(chan struct{})
	streamRelease := make(chan struct{})
	var streamOnce sync.Once
	releaseStream := func() { streamOnce.Do(func() { close(streamRelease) }) }
	defer releaseStream()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if option.multiKey != "" {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if protocol == "anthropic" {
				key = r.Header.Get("x-api-key")
			}
			if key != "syntheticvendor-one" && key != "syntheticvendor-two" {
				t.Error("unapproved or disabled credential dispatched")
				w.WriteHeader(401)
				return
			}
		}
		write := func(data string) (int, error) {
			if cache {
				if protocol == "anthropic" {
					data = strings.ReplaceAll(data, `"input_tokens":2`, `"input_tokens":2,"cache_read_input_tokens":4,"cache_creation_input_tokens":5`)
					if option.cacheTTL {
						data = strings.ReplaceAll(data, `"cache_creation_input_tokens":5`, `"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":2}`)
					}
				} else {
					extra := `"prompt_tokens":2,"prompt_tokens_details":{"cached_tokens":1,"cached_creation_tokens":0}`
					if protocol == "deepseek" {
						extra += `,"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":1`
					}
					data = strings.ReplaceAll(data, `"prompt_tokens":2`, extra)
				}
			}

			if images {
				data = strings.ReplaceAll(data, `"input_tokens":2`, `"input_tokens":12`)
				data = strings.ReplaceAll(data, `"prompt_tokens":2`, `"prompt_tokens":12`)
				data = strings.ReplaceAll(data, `"total_tokens":5`, `"total_tokens":15`)
			}
			return io.WriteString(w, data)
		}
		if block.Load() {
			close(arrived)
			select {
			case <-r.Context().Done():
			case <-blockedRelease:
			}
			return
		}
		var req struct {
			Input     []string          `json:"input"`
			Tools     []json.RawMessage `json:"tools"`
			Messages  json.RawMessage   `json:"messages"`
			Stream    bool              `json:"stream"`
			MaxTokens uint              `json:"max_tokens"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		if embedding {
			if r.URL.Path != "/v1/embeddings" || len(req.Input) != 2 || req.MaxTokens != 0 || req.Stream {
				t.Error("invalid embedding wire request")
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			write(`{"object":"list","model":"governance-model","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]},{"object":"embedding","index":1,"embedding":[0.3,0.4]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
			return
		}
		if tools && (len(req.Tools) != 1 || !strings.Contains(string(req.Tools[0]), "lookup") || !strings.Contains(string(req.Messages), "synthetic result")) {
			t.Error("tool declaration/history lost during conversion")
			w.WriteHeader(400)
			return
		}
		if images && !strings.Contains(string(req.Messages), strings.TrimPrefix(testutil.InlinePNG, "data:image/png;base64,")) {
			t.Error("image bytes lost during conversion")
			w.WriteHeader(400)
			return
		}
		wantOutput := uint(8)
		if option.responses {
			wantOutput = 16
		}
		if req.MaxTokens != wantOutput {
			t.Error("converted request exceeded or lost output bound")
			w.WriteHeader(400)
			return
		}
		if protocol == "anthropic" {
			if r.URL.Path != "/v1/messages" {
				t.Error("wrong Anthropic upstream path")
				w.WriteHeader(400)
				return
			}
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range []string{
					`{"type":"message_start","message":{"id":"msg_synthetic","type":"message","role":"assistant","model":"governance-model","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"synthetic"}}`,
					`{"type":"content_block_stop","index":0}`,
				} {
					write("data: " + frame + "\n\n")
				}
				w.(http.Flusher).Flush()
				select {
				case <-streamRelease:
				case <-r.Context().Done():
					return
				}
				reason := "end_turn"
				if tools {
					reason = "tool_use"
					for _, frame := range []string{
						`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_new","name":"lookup","input":{}}}`,
						`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
						`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"synthetic\"}"}}`,
						`{"type":"content_block_stop","index":1}`,
					} {
						write("data: " + frame + "\n\n")
					}
				}
				write("data: " + `{"type":"message_delta","delta":{"stop_reason":"` + reason + `"},"usage":{"output_tokens":3}}` + "\n\ndata: " + `{"type":"message_stop"}` + "\n\n")
			} else {
				w.Header().Set("Content-Type", "application/json")
				if tools {
					write(`{"id":"msg_synthetic","type":"message","role":"assistant","model":"governance-model","content":[{"type":"text","text":"synthetic"},{"type":"tool_use","id":"call_new","name":"lookup","input":{"q":"synthetic"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`)
				} else {
					write(`{"id":"msg_synthetic","type":"message","role":"assistant","model":"governance-model","content":[{"type":"text","text":"synthetic"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
				}
			}
			return
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			write("data: {\"id\":\"synthetic\",\"model\":\"governance-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\n")
			if protocol == "deepseek" || tools {
				write("data: " + `{"id":"synthetic","model":"governance-model","choices":[{"index":0,"delta":{},"finish_reason":null}]}` + "\n\n")
			} else {
				write("data: {\"id\":\"synthetic\",\"model\":\"governance-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			}
			w.(http.Flusher).Flush()
			select {
			case <-streamRelease:
			case <-r.Context().Done():
				return
			}
			if tools {
				for _, frame := range []string{
					`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_new","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
					`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"synthetic\"}"}}]},"finish_reason":null}]}`,
					`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
				} {
					write("data: " + frame + "\n\n")
				}
				write("data: [DONE]\n\n")
			} else if protocol == "deepseek" {
				write("data: " + `{"id":"synthetic","model":"governance-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}` + "\n\ndata: [DONE]\n\n")
			} else {
				write("data: {\"id\":\"synthetic\",\"model\":\"governance-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
			}
		} else {
			w.Header().Set("Content-Type", "application/json")
			if tools {
				write(`{"id":"synthetic","object":"chat.completion","model":"governance-model","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_new","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"synthetic\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
			} else {
				write(reply)

			}
		}
	}))
	defer func() { releaseStream(); upstream.Close() }()
	defer close(blockedRelease)
	start := func(command *exec.Cmd, ready string) (string, func()) {
		t.Helper()
		_ = os.Remove(ready)
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Start(); err != nil {
			t.Fatal("fixture process did not start")
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				_ = command.Process.Kill()
				_ = command.Wait()
				if dir := os.Getenv("TEST_NEWAPI_PROCESS_LOG_DIR"); dir != "" {
					_ = os.WriteFile(filepath.Join(dir, filepath.Base(command.Path)+".log"), output.Bytes(), 0600)
				}
			})
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(ready)
			if err == nil && len(data) > 0 {
				return string(data), stop
			}
			time.Sleep(20 * time.Millisecond)
		}
		stop()
		if dir := os.Getenv("TEST_NEWAPI_PROCESS_LOG_DIR"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, "process-failure.log"), output.Bytes(), 0600)
		}
		t.Fatalf("fixture readiness timeout (exit=%d; process output withheld)", command.ProcessState.ExitCode())
		return "", stop
	}
	root := t.TempDir()
	native := exec.Command(binary)
	native.Dir = root
	native.Env = append(os.Environ(), "GOVERNANCE_FIXTURE_DIR="+root, "GOVERNANCE_FIXTURE_UPSTREAM="+upstream.URL, "GOVERNANCE_FIXTURE_PROTOCOL="+protocol, "GOVERNANCE_FIXTURE_MULTIKEY="+option.multiKey)
	nativeURL, stopNative := start(native, filepath.Join(root, "address"))
	defer stopNative()
	// Only synthetic fixtures automatically copy a revision. Operators must review production configuration.
	manifestRequest, _ := http.NewRequest("GET", nativeURL+"/internal/governance/v1/channels/1/revision", nil)
	manifestRequest.Header.Set("Authorization", "Bearer sk-syntheticgovernancefixture")
	manifestResponse, err := http.DefaultClient.Do(manifestRequest)
	if err != nil {
		t.Fatal("fixture manifest unavailable")
	}
	var manifest struct {
		Revision string `json:"revision"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(manifestResponse.Body, 4096)).Decode(&manifest)
	manifestResponse.Body.Close()
	if decodeErr != nil || manifestResponse.StatusCode != 200 || len(manifest.Revision) != 64 {
		t.Fatal("invalid fixture manifest")
	}

	target, _ := url.Parse(nativeURL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var pause atomic.Bool
	captured := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	proxy.ModifyResponse = func(res *http.Response) error {
		if res.StatusCode != 200 && (strings.HasSuffix(res.Request.URL.Path, "/chat/completions") || strings.HasSuffix(res.Request.URL.Path, "/embeddings")) {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			res.Body.Close()
			res.Body = io.NopCloser(bytes.NewReader(body))
			t.Logf("synthetic native status=%d body=%s", res.StatusCode, body)
		}

		if pause.Load() && (strings.HasSuffix(res.Request.URL.Path, "/chat/completions") || strings.HasSuffix(res.Request.URL.Path, "/embeddings")) && res.StatusCode == 200 {
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				return err
			}
			res.Body = io.NopCloser(bytes.NewReader(body))
			close(captured)
			<-release
		}
		return nil
	}
	faultProxy := httptest.NewServer(proxy)
	defer faultProxy.Close()

	isolatedDSN, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal("invalid isolated fixture DSN")
	}
	query := isolatedDSN.Query()
	query.Set("search_path", pool.Config().ConnConfig.RuntimeParams["search_path"])
	isolatedDSN.RawQuery = query.Encode()
	child := func() (string, func()) {
		ready := filepath.Join(t.TempDir(), "ready")
		command := exec.Command(os.Args[0], "-test.run=^TestNewAPIControlHelper$")
		command.Env = append(os.Environ(), "AIGW_INTEGRATION_CHILD=1", "AIGW_INTEGRATION_TTL="+map[bool]string{true: "1", false: "0"}[option.cacheTTL], "AIGW_INTEGRATION_RESPONSES="+map[bool]string{true: "1", false: "0"}[option.responses], "AIGW_INTEGRATION_CACHE="+map[bool]string{true: "1", false: "0"}[cache], "AIGW_INTEGRATION_REVISION="+manifest.Revision, "AIGW_INTEGRATION_PROTOCOL="+protocol, "AIGW_INTEGRATION_IMAGES="+map[bool]string{true: "1", false: "0"}[images], "AIGW_INTEGRATION_RICH="+map[bool]string{true: "1", false: "0"}[tools || images], "AIGW_INTEGRATION_DSN="+isolatedDSN.String(), "AIGW_INTEGRATION_ORIGIN="+faultProxy.URL, "AIGW_INTEGRATION_READY="+ready)
		return start(command, ready)
	}
	controlURL, stopControl := child()
	replicaURL := ""
	if option.replicated {
		var stopReplica func()
		replicaURL, stopReplica = child()
		defer stopReplica()
	}

	call := func(origin string, stream bool) (int, []byte) {
		if embedding || option.messages {
			stream = false
		}
		req := request()
		if protocol == "anthropic" {
			req.Model = "reviewed-compatible/fixture"
		}
		req.Stream = stream
		if images {
			req.Messages = []provider.Message{{Role: "user", Parts: []provider.ContentPart{{Type: "text", Text: "synthetic image query"}, {Type: "image_url", ImageURL: &provider.ImageURL{URL: testutil.InlinePNG, Detail: "auto"}}}}}
		}
		if tools {
			req.Tools = []provider.Tool{{Type: "function", Function: provider.FunctionDefinition{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}}}
			req.ToolChoice = "auto"
			req.Messages = append(req.Messages, provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call_old", Type: "function", Function: provider.FunctionCall{Name: "lookup", Arguments: `{"q":"prior"}`}}}}, provider.Message{Role: "tool", ToolCallID: "call_old", Content: "synthetic result"})
		}
		data, _ := json.Marshal(req)
		path := "/v1/chat/completions"
		if embedding {
			path = "/v1/embeddings"
			data, _ = json.Marshal(map[string]any{"model": req.Model, "input": []string{"synthetic one", "synthetic two"}, "dimensions": 2, "encoding_format": "float"})
		}
		if option.responses {
			path = "/v1/responses"
			data, _ = json.Marshal(map[string]any{"model": req.Model, "input": "synthetic responses input", "max_output_tokens": 16, "store": false, "stream": stream})
		}
		if option.messages {
			path = "/v1/messages"
			data, _ = json.Marshal(map[string]any{"model": req.Model, "max_tokens": 8, "messages": []map[string]string{{"role": "user", "content": "synthetic messages input"}}})
		}
		r, _ := http.NewRequest("POST", origin+path, bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer synthetic-project-key")
		if option.messages {
			r.Header.Del("Authorization")
			r.Header.Set("X-Api-Key", "synthetic-project-key")
			r.Header.Set("Anthropic-Version", "2023-06-01")
		}
		r.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 20 * time.Second}
		res, err := client.Do(r)
		if err != nil {
			return 0, nil
		}
		defer res.Body.Close()
		if stream {
			reader := bufio.NewReader(res.Body)
			var prefix strings.Builder
			for i := 0; i < 32; i++ {
				line, readErr := reader.ReadString('\n')
				if readErr != nil {
					releaseStream()
					t.Fatal("stream content did not precede upstream completion")
				}
				prefix.WriteString(line)
				if strings.Contains(line, "synthetic") {
					break
				}
			}
			first := prefix.String()
			if !strings.Contains(first, "synthetic") {
				releaseStream()
				t.Fatal("early content missing")
			}

			var settledBefore int
			if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_reservations WHERE state='settled'`).Scan(&settledBefore); err != nil || settledBefore != 1 {
				releaseStream()
				t.Fatal("stream settled before completion")
			}
			releaseStream()
			tail, readErr := io.ReadAll(reader)
			terminalMarker := "[DONE]"
			if option.responses {
				terminalMarker = `"type":"response.completed"`
			}
			if readErr != nil || !strings.Contains(string(tail), terminalMarker) {
				t.Fatalf("verified stream did not finish: %v, synthetic response: %s%s", readErr, first, tail)
			}
			return res.StatusCode, append([]byte(first), tail...)
		}
		body, _ := io.ReadAll(res.Body)
		if option.responses && res.StatusCode == 200 && (!bytes.Contains(body, []byte(`"object":"response"`)) || !bytes.Contains(body, []byte(`"cache_write_tokens"`))) {
			t.Error("responses contract or cache facts lost")
		}
		if option.messages && res.StatusCode == 200 {
			var message provider.MessagesResult
			if json.Unmarshal(body, &message) != nil || message.Type != "message" || message.Usage.InputTokens != map[bool]int64{true: 2, false: 1}[protocol == "anthropic"] || message.Usage.CacheCreationInputTokens != map[bool]int64{true: 5, false: 0}[protocol == "anthropic"] {
				t.Error("messages raw cache counters or protocol lost")
			}
		}
		return res.StatusCode, body
	}
	for _, stream := range []bool{false, true} {
		origin := controlURL
		if option.replicated && stream {
			origin = replicaURL
		}
		status, body := call(origin, stream)
		if status != 200 {
			t.Fatalf("real process integration rejected request: %d %s physical=%d", status, body, calls.Load())
		}
		if tools && (!strings.Contains(string(body), "tool_calls") || !strings.Contains(string(body), "call_new") || !strings.Contains(string(body), "lookup")) {
			t.Fatal("validated tool call missing from client response")
		}
	}
	pause.Store(true)
	requestDone := make(chan struct{})
	go func() { call(controlURL, false); close(requestDone) }()
	select {
	case <-captured:
	case <-time.After(20 * time.Second):
		t.Fatal("did not reach durable result fault point")
	}
	stopControl() // SIGKILL while the successful native response is withheld.
	releaseOnce.Do(func() { close(release) })
	pause.Store(false)
	<-requestDone
	if calls.Load() != 3 {
		t.Fatal("unexpected physical attempt count before recovery")
	}
	if option.replicated {
		// No replacement process is started until the surviving worker proves
		// automatic settlement of the killed owner's durable completion.
		deadline := time.Now().Add(12 * time.Second)
		recovered := false
		for time.Now().Before(deadline) {
			var settled, receipts int
			if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_reservations WHERE state='settled'`).Scan(&settled); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM newapi_execution_receipts`).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if settled == 3 && receipts == 3 {
				recovered = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !recovered || calls.Load() != 3 {
			t.Fatal("surviving replica did not recover exactly once without replacement startup")
		}
	}
	restartedURL, stopRestarted := child()
	defer stopRestarted()
	_ = restartedURL
	var settled, receipts int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_reservations WHERE state='settled'`).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM newapi_execution_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if settled != 3 || receipts != 3 || calls.Load() != 3 {
		t.Fatalf("restart recovery mismatch: settled=%d receipts=%d physical=%d", settled, receipts, calls.Load())
	}
	var held, spent int64
	if err := pool.QueryRow(context.Background(), `SELECT held,spent FROM budget_accounts WHERE project_id=$1`, scope.Project).Scan(&held, &spent); err != nil || held != 0 || spent != expectedSpent {
		t.Fatalf("incorrect recovered ledger: held=%d spent=%d", held, spent)
	}
	// Kill the data plane after the provider observes a request, before any
	// completion evidence exists. Restart must never replay or refund it.
	block.Store(true)
	uncertainDone := make(chan struct{})
	go func() { call(restartedURL, false); close(uncertainDone) }()
	select {
	case <-arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("unknown-outcome fault point not reached")
	}
	stopNative()
	select {
	case <-uncertainDone:
	case <-time.After(20 * time.Second):
		t.Fatal("data-plane termination did not return")
	}
	restartedNative := exec.Command(binary)
	restartedNative.Dir = root
	restartedNative.Env = append(os.Environ(), "GOVERNANCE_FIXTURE_DIR="+root, "GOVERNANCE_FIXTURE_UPSTREAM="+upstream.URL, "GOVERNANCE_FIXTURE_PROTOCOL="+protocol, "GOVERNANCE_FIXTURE_LISTEN="+target.Host, "GOVERNANCE_FIXTURE_MULTIKEY="+option.multiKey)
	_, stopNativeAgain := start(restartedNative, filepath.Join(root, "address"))
	defer stopNativeAgain()
	// A recovery poll may observe this restart; it is not authority to replay.
	time.Sleep(6 * time.Second)
	var uncertain int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_reservations WHERE state='uncertain'`).Scan(&uncertain); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT held,spent FROM budget_accounts WHERE project_id=$1`, scope.Project).Scan(&held, &spent); err != nil {
		t.Fatal(err)
	}
	if uncertain != 1 || held != expectedHeld || spent != expectedSpent || calls.Load() != 4 {
		t.Fatalf("unknown outcome was replayed/refunded: uncertain=%d held=%d spent=%d physical=%d", uncertain, held, spent, calls.Load())
	}

	// An expired local binding with no data-plane dispatch can be cancelled
	// atomically; the resulting durable tombstone is the only refund authority.
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = manifest.Revision
		cfg.Models[i].UpstreamModel = "governance-model"
	}
	directory, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := directory.Find("zhipu/fixture")
	const orphanRequest = "OrphanRequest123456"
	const orphanAttempt = "OrphanAttempt123456"
	if _, err := ledger.Reserve(context.Background(), budget.Quote{Scope: scope, RequestID: orphanRequest, PriceVersion: route.PriceVersion(), Amount: 288}); err != nil {
		t.Fatal(err)
	}
	if err := execution.New(pool).Begin(context.Background(), execution.Start{AttemptID: orphanAttempt, RequestID: orphanRequest, Model: "zhipu/fixture", Provider: "zhipu", Region: "cn-mainland"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(context.Background(), orphanRequest); err != nil {
		t.Fatal(err)
	}
	if err := newapibridge.NewJournal(pool).Bind(context.Background(), faultProxy.URL, "governance-model", orphanRequest, orphanAttempt, strings.Repeat("a", 64), route, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE newapi_execution_bindings SET created_at=clock_timestamp()-interval '4 minutes' WHERE attempt_id=$1`, orphanAttempt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		r, err := ledger.Get(context.Background(), orphanRequest)
		if err == nil && r.State == "settled" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	orphan, err := ledger.Get(context.Background(), orphanRequest)
	if err != nil || orphan.State != "settled" || orphan.Actual == nil || *orphan.Actual != 0 {
		t.Fatal("absent attempt not reconciled from cancellation proof")
	}
	late, _ := http.NewRequest("POST", nativeURL+"/internal/governance/v1/chat/completions", strings.NewReader(`{"model":"governance-model","messages":[],"max_tokens":8}`))
	late.Header.Set("Authorization", "Bearer sk-syntheticgovernancefixture")
	late.Header.Set("X-Aigw-Request-Id", orphanRequest)
	late.Header.Set("X-Aigw-Attempt-Id", orphanAttempt)
	late.Header.Set("X-Aigw-Channel-Id", "1")
	rejected, err := http.DefaultClient.Do(late)
	if err != nil {
		t.Fatal(err)
	}
	rejected.Body.Close()
	if rejected.StatusCode != 409 || calls.Load() != 4 {
		t.Fatal("late request bypassed cancelled tombstone")
	}
	if err := pool.QueryRow(context.Background(), `SELECT held,spent FROM budget_accounts WHERE project_id=$1`, scope.Project).Scan(&held, &spent); err != nil || held != expectedHeld || spent != expectedSpent {
		t.Fatal("cancellation released an uncertain physical dispatch")
	}

}
