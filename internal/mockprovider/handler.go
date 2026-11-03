// Package mockprovider is an explicitly local, deterministic supplier fixture.
package mockprovider

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
)

func Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var req gateway.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(&req); err != nil || req.Model != "mock-text" || !req.Stream || req.MaxTokens < 1 || req.MaxTokens > 8 {
		http.Error(w, "unsupported mock request", 400)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	n := min(req.MaxTokens, 2)
	for i := 0; i < n; i++ {
		if _, err := fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"mock\"}}]}\n\n"); err != nil {
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
	}
	_, _ = fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\ndata: [DONE]\n\n", n, n+2)
}
