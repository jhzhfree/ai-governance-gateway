package httpapi_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestSupplierIsolationRecoveryAndRuntimePermissions(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	healthy := testutil.SupplierFixture(t)
	f := providerSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		healthy.ServeHTTP(w, r)
	}))
	o, p, key := providerProject(f)
	path := projectPath(o, p) + "/runtime"
	initial := parse[control.Runtime](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if len(initial.Models) != 3 || initial.Models[0].State != "unobserved" || initial.Instance == nil || initial.Instance.Tracked {
		t.Fatal("invalid initial runtime")
	}
	for i := 0; i < 5; i++ {
		f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 502)
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), 503)
	if calls.Load() != 5 {
		t.Fatal("open circuit dispatched or replayed supplier call")
	}
	report := parse[control.Runtime](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if report.Models[0].State != "open" || report.OpenHolds != 5 || report.HeldAmount != "1440" || report.UnfinishedExecutions != 0 || report.Instance.Counters.Requests != 6 || report.Instance.Counters.Rejected != 1 {
		t.Fatal("runtime failed to distinguish billing and circuit rejection")
	}
	fail.Store(false)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("zhipu/fixture", false), 200)
	if _, err := f.pool.Exec(context.Background(), `UPDATE supplier_circuits SET retry_at=clock_timestamp()-interval '1 second' WHERE state='open'`); err != nil {
		t.Fatal(err)
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), 200)
	after := parse[control.Runtime](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if after.Models[0].State != "closed" || after.OpenHolds != 5 || calls.Load() != 7 {
		t.Fatal("recovery rewrote uncertain budget or failed probe")
	}
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", path, nil, 200)
	f.request(f.token("outsider"), "GET", path, nil, 404)
	f.request(key.Secret, "GET", path, nil, 401)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": []string{"zhipu/fixture"}}, 204)
	scoped := parse[control.Runtime](t, f.request(f.token("viewer"), "GET", path, nil, 200))
	if len(scoped.Models) != 1 || scoped.Models[0].Provider != "zhipu" {
		t.Fatal("runtime exposed unassigned route")
	}
}
func TestSupplierParameterErrorsDoNotTripCircuit(t *testing.T) {
	var calls atomic.Int32
	f := providerSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(400) }))
	_, _, key := providerProject(f)
	for i := 0; i < 7; i++ {
		f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 502)
	}
	if calls.Load() != 7 {
		t.Fatal("supplier parameter errors tripped circuit")
	}
}

func TestSupplierRateAndBrokenStreamTripWithoutReplay(t *testing.T) {
	for _, kind := range []string{"rate", "broken_stream"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			f := providerSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if kind == "rate" {
					w.WriteHeader(429)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"}}]}\n\n"))
			}))
			o, p, key := providerProject(f)
			for i := 0; i < 5; i++ {
				status := 502
				if kind == "broken_stream" {
					status = 200
				}
				f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), status)
			}
			f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), 503)
			report := parse[control.Runtime](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/runtime", nil, 200))
			if calls.Load() != 5 || report.OpenHolds != 5 || report.Models[0].State != "open" {
				t.Fatal("supplier errors bypassed circuit or replayed work")
			}
		})
	}
}

func TestRuntimeWithoutMonitorIsExplicitlyUnavailableAndReadOnly(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	v := parse[control.Runtime](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/runtime", nil, 200))
	if v.Instance != nil || len(v.Models) != 1 || v.Models[0].State != "not_applicable" {
		t.Fatal("missing monitor represented as live data")
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM supplier_circuits`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read-only runtime created a circuit")
	}
}
