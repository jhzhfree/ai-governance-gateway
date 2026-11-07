package observe

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MetricsHandler enables deployment-wide statistics only with an independent
// scrape credential. It never exposes tenant identifiers or payloads.
func (r *Registry) MetricsHandler(token string) (http.Handler, error) {
	if token == "" {
		return http.NotFoundHandler(), nil
	}
	if len(token) < 32 || len(token) > 256 {
		return nil, errors.New("metrics token must contain 32..256 visible ASCII characters")
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return nil, errors.New("metrics token must contain 32..256 visible ASCII characters")
		}
	}
	expected := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		auth := req.Header.Values("Authorization")
		var value string
		if len(auth) == 1 {
			value = auth[0]
		}
		got := sha256.Sum256([]byte(value))
		if len(auth) != 1 || subtle.ConstantTimeCompare(got[:], expected[:]) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(r.prometheus()))
	}), nil
}

type ingressKey struct{ route, class string }
type ingressCounters struct {
	count   uint64
	seconds float64
	buckets [6]uint64
}

func routeFamily(path string) string {
	switch path {
	case "/v1/chat/completions":
		return "chat"
	case "/v1/embeddings":
		return "embeddings"
	case "/v1/models":
		return "models"
	default:
		return "other"
	}
}

// RecordIngress is independent of authenticated per-project measurements. Thus
// denied authentication is counted while project cardinality remains bounded.
func (r *Registry) RecordIngress(path string, status int, elapsed time.Duration) {
	if r == nil {
		return
	}
	class := "other"
	if status >= 200 && status <= 599 {
		class = strconv.Itoa(status/100) + "xx"
	}
	key := ingressKey{routeFamily(path), class}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ingress == nil {
		r.ingress = make(map[ingressKey]ingressCounters)
	}
	v := r.ingress[key]
	v.count++
	v.seconds += max(0, elapsed.Seconds())
	for i, b := range Bounds {
		if elapsed <= time.Duration(b)*time.Millisecond {
			v.buckets[i]++
		}
	}
	r.ingress[key] = v
}
func (r *Registry) prometheus() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var s strings.Builder
	s.WriteString("# HELP aigw_gateway_requests_total Gateway HTTP responses including denied authentication.\n# TYPE aigw_gateway_requests_total counter\n")
	for _, route := range []string{"chat", "embeddings", "models", "other"} {
		for _, class := range []string{"2xx", "3xx", "4xx", "5xx", "other"} {
			v, ok := r.ingress[ingressKey{route, class}]
			if !ok {
				continue
			}
			fmt.Fprintf(&s, "aigw_gateway_requests_total{route=%q,status_class=%q} %d\n", route, class, v.count)
		}
	}
	s.WriteString("# HELP aigw_gateway_request_duration_seconds Gateway handler duration, including full streaming lifetime.\n# TYPE aigw_gateway_request_duration_seconds histogram\n")
	for _, route := range []string{"chat", "embeddings", "models", "other"} {
		for _, class := range []string{"2xx", "3xx", "4xx", "5xx", "other"} {
			v, ok := r.ingress[ingressKey{route, class}]
			if !ok {
				continue
			}
			for i, b := range Bounds {
				fmt.Fprintf(&s, "aigw_gateway_request_duration_seconds_bucket{route=%q,status_class=%q,le=%q} %d\n", route, class, strconv.FormatFloat(float64(b)/1000, 'f', -1, 64), v.buckets[i])
			}
			fmt.Fprintf(&s, "aigw_gateway_request_duration_seconds_bucket{route=%q,status_class=%q,le=\"+Inf\"} %d\naigw_gateway_request_duration_seconds_sum{route=%q,status_class=%q} %s\naigw_gateway_request_duration_seconds_count{route=%q,status_class=%q} %d\n", route, class, v.count, route, class, strconv.FormatFloat(v.seconds, 'f', -1, 64), route, class, v.count)
		}
	}
	fmt.Fprintf(&s, "# HELP aigw_process_start_time_seconds Gateway process start time.\n# TYPE aigw_process_start_time_seconds gauge\naigw_process_start_time_seconds %d\n", r.started.Unix())
	return s.String()
}
