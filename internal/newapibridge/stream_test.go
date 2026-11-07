package newapibridge

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVerifiedStreamEarlyContentAndCompletion(t *testing.T) {
	for _, mode := range []string{"complete", "missing", "duplicate", "incomplete", "trailing", "body-forgery", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Trailer", "X-Aigw-Completion")
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[]}\n\n")
				w.(http.Flusher).Flush()
				<-release
				if mode != "truncated" {
					io.WriteString(w, "data: [DONE]\n\n")
				}
				if mode == "trailing" {
					io.WriteString(w, "data: {}\n\n")
				}
				if mode == "body-forgery" {
					io.WriteString(w, "X-Aigw-Completion: completed\n")
				}
				switch mode {
				case "complete", "trailing":
					w.Header().Set("X-Aigw-Completion", "completed")
				case "duplicate":
					w.Header().Add("X-Aigw-Completion", "completed")
					w.Header().Add("X-Aigw-Completion", "completed")
				case "incomplete":
					w.Header().Set("X-Aigw-Completion", "failed")
				}
			}))
			defer func() { unblock(); server.Close() }()
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			stream := newVerifiedStream(response)
			defer stream.Close()
			reader := bufio.NewReader(stream)
			first, err := reader.ReadString('\n')
			if err != nil || !strings.Contains(first, "choices") {
				t.Fatal("content did not arrive before completion")
			}
			unblock()
			rest, err := io.ReadAll(reader)
			if mode == "complete" {
				if err != nil || !strings.Contains(string(rest), "[DONE]") {
					t.Fatalf("valid completion rejected: %v", err)
				}
			} else if err == nil || strings.Contains(string(rest), "[DONE]") {
				t.Fatal("unproven completion escaped")
			}
		})
	}
}
