package observe

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMetricsCredentialProtection(t *testing.T) {
	r := New()
	disabled, err := r.MetricsHandler("")
	if err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	disabled.ServeHTTP(out, httptest.NewRequest("GET", "/metrics", nil))
	if out.Code != 404 {
		t.Fatal("disabled endpoint exposed")
	}
	for _, value := range []string{"short", strings.Repeat("a", 257), strings.Repeat("a", 32) + "\n", strings.Repeat("密", 32)} {
		if _, err := r.MetricsHandler(value); err == nil {
			t.Fatal("invalid credential accepted")
		}
	}
	token := strings.Repeat("x", 32)
	handler, err := r.MetricsHandler(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, headers := range [][]string{nil, {"Bearer incorrect"}, {"Bearer " + token, "Bearer " + token}, {"Basic " + token}} {
		req := httptest.NewRequest("GET", "/metrics", nil)
		for _, v := range headers {
			req.Header.Add("Authorization", v)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 401 || strings.Contains(out.Body.String(), "aigw_") {
			t.Fatal("unauthorized metrics exposed")
		}
	}
	req := httptest.NewRequest("POST", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out = httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 405 || out.Header().Get("Allow") != "GET" {
		t.Fatal("write allowed")
	}
}

func TestPrometheusBoundedLabelsHistogramAndConcurrentScrapes(t *testing.T) {
	r := New()
	token := strings.Repeat("x", 32)
	handler, _ := r.MetricsHandler(token)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.RecordIngress("/v1/chat/completions", 403, 500*time.Millisecond)
			r.RecordIngress("/v1/embeddings", 200, time.Millisecond)
			r.RecordIngress(fmt.Sprintf("/v1/private-user-%d", i), 503, 121*time.Second)
			r.Record("secret-org", "secret-project", 200, time.Second)
			req := httptest.NewRequest("GET", "/metrics?secret=query", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if out.Code != 200 {
				t.Error("scrape failed")
			}
		}(i)
	}
	wg.Wait()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	body := out.Body.String()
	for _, expected := range []string{
		`aigw_gateway_requests_total{route="chat",status_class="4xx"} 100`,
		`aigw_gateway_requests_total{route="embeddings",status_class="2xx"} 100`,
		`aigw_gateway_request_duration_seconds_bucket{route="chat",status_class="4xx",le="0.5"} 100`,
		`aigw_gateway_request_duration_seconds_bucket{route="chat",status_class="4xx",le="0.1"} 0`,
		`aigw_gateway_request_duration_seconds_sum{route="chat",status_class="4xx"} 50`,
		`aigw_gateway_request_duration_seconds_bucket{route="other",status_class="5xx",le="120"} 0`,
		`aigw_gateway_request_duration_seconds_bucket{route="other",status_class="5xx",le="+Inf"} 100`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	for _, secret := range []string{"secret-org", "secret-project", "private-user", "query", token} {
		if strings.Contains(body, secret) {
			t.Fatal("sensitive label exposed")
		}
	}
	if len(r.ingress) != 3 || out.Header().Get("Cache-Control") != "no-store" || !strings.Contains(out.Header().Get("Content-Type"), "version=0.0.4") {
		t.Fatal("invalid exposition")
	}
}
