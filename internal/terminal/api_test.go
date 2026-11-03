package terminal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientNeverForwardsAuthorizationOnRedirect(t *testing.T) {
	var received atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer destination.Close()
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("missing API authentication")
		}
		http.Redirect(w, r, destination.URL, 302)
	}))
	defer original.Close()
	client, err := NewClient(original.URL, "synthetic-token", true, original.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Organizations(context.Background(), "", 1); err == nil {
		t.Fatal("redirect treated as successful API response")
	}
	if received.Load() != 0 {
		t.Fatal("credential sent to redirect target")
	}
}
func TestClientRejectsInvalidResponsesAndSanitizesErrors(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"html", "text/html", "<html>not-api</html>", 200},
		{"null", "application/json", "null", 200},
		{"missing_fields", "application/json", `{"data":[]}`, 200},
		{"oversized", "application/json", strings.Repeat("x", (1<<20)+1), 200},
		{"private_error", "application/json", `{"error":"synthetic-private-token"}`, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Request-ID", "synthetic-private-token")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "synthetic-private-token", true, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Organizations(context.Background(), "", 10)
			if err == nil {
				t.Fatal("invalid response accepted")
			}
			if strings.Contains(err.Error(), "synthetic-private-token") {
				t.Fatal("error exposed credential/body/header")
			}
		})
	}
}
func TestClientCancellationAndTerminalControlFiltering(t *testing.T) {
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done(); close(canceled) }))
	defer server.Close()
	client, err := NewClient(server.URL, "synthetic-token", true, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := client.Organizations(ctx, "", 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request not canceled: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("server did not receive cancellation")
	}
	if value := SafeText("name\x1b[2J\n\t\u202e"); strings.ContainsAny(value, "\x1b\n\t\u202e") {
		t.Fatal("terminal control characters survived filtering")
	}
}

func TestLocalQueryValidationUsesUsageExitCode(t *testing.T) {
	client, err := NewClient("https://gateway.example", "synthetic-token", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, invalidLimit := client.Organizations(ctx, "", 101)
	_, missingOrg := client.Projects(ctx, "", "", 1)
	_, missingProject := client.Usage(ctx, "org", "")
	_, invalidCursor := client.Audit(ctx, "org", "invalid", 1)
	for _, err := range []error{invalidLimit, missingOrg, missingProject, invalidCursor} {
		if ExitCode(err) != 2 {
			t.Fatal("local query argument error must exit 2")
		}
	}
}
