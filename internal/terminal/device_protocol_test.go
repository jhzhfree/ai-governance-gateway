package terminal

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/jhzhfree/ai-governance-gateway/internal/deviceproof"
)

func TestDeviceProxyReviewedProtocolsAndProofBinding(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var origin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "DPoP synthetic-device-session" || r.Header.Get("X-Aigw-Channel-Id") != "" {
			t.Error("proxy forwarded local credentials or untrusted governance headers")
		}
		if _, err := deviceproof.Verify(r.Header.Get("DPoP"), public, r.Method, origin+r.URL.Path, "synthetic-device-session", time.Now()); err != nil {
			t.Error("proof does not bind actual path")
		}
		if _, err := deviceproof.Verify(r.Header.Get("DPoP"), public, r.Method, origin+"/v1/other", "synthetic-device-session", time.Now()); err == nil {
			t.Error("proof accepted on another path")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) == 0 {
			t.Error("missing forwarded payload")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"synthetic":true}`)
	}))
	defer upstream.Close()
	origin = upstream.URL
	client, err := NewClient(origin, "synthetic-management-token", true, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	proxy := &deviceProxy{client: client, host: "localhost", localToken: "synthetic-local-token", key: private, permits: make(chan struct{}, 1), upstream: upstream.Client(), session: types.IssuedDeviceSession{Token: "synthetic-device-session", TokenType: "DPoP", ExpiresAt: time.Now().Add(time.Hour)}}
	for _, tc := range []struct {
		path       string
		size, want int
	}{
		{"/v1/embeddings", 64 << 10, 200},
		{"/v1/embeddings", (64 << 10) + 1, 413},
		{"/v1/chat/completions", 1 << 20, 200},
		{"/v1/chat/completions", (1 << 20) + 1, 413},
		{"/v1/embeddings?query=unsupported", 1, 404},
		{"/v1/messages", 1, 404},
	} {
		t.Run(tc.path+http.StatusText(tc.want), func(t *testing.T) {
			before := calls.Load()
			req := httptest.NewRequest("POST", "http://localhost"+tc.path, strings.NewReader(strings.Repeat("x", tc.size)))
			req.Header.Set("Authorization", "Bearer synthetic-local-token")
			req.Header.Set("X-Aigw-Channel-Id", "untrusted")
			out := httptest.NewRecorder()
			proxy.ServeHTTP(out, req)
			if out.Code != tc.want {
				t.Fatalf("status %d want %d", out.Code, tc.want)
			}
			expected := before
			if tc.want == 200 {
				expected++
			}
			if calls.Load() != expected {
				t.Fatal("rejected local request reached gateway")
			}
		})
	}
}
