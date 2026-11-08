package gatewaybridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestCompletedEvidenceSurvivesKilledSettlementProcess(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			db, ledger, _ := testutil.Database(t)
			cfg := testutil.MainlandConfig()
			cfg.Models = cfg.Models[3:]
			cfg.Models[0].MaxOutputTokens = 32
			actor := identity.Principal{Issuer: "synthetic", Subject: "owner"}
			source := provider.NewManager(db, func(string) string { return "synthetic-cluster-key" }, nil, actor)
			if err := source.Publish(ctx, actor, 0, cfg); err != nil {
				t.Fatal(err)
			}
			store := control.NewWithCatalog(db, source)
			org, err := store.CreateOrganizationWithCurrency(ctx, actor, "completion crash", "2026-11", provider.Currency, 10000)
			if err != nil {
				t.Fatal(err)
			}
			project, err := store.CreateProject(ctx, actor, org.ID, "crash", 5000)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SetModels(ctx, actor, org.ID, project.ID, []string{"deepseek/fixture"}); err != nil {
				t.Fatal(err)
			}
			key, err := store.CreateKey(ctx, actor, org.ID, project.ID, "synthetic", []string{"deepseek/fixture"}, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-cluster-key" {
					t.Error("unexpected provider request")
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
				}
			}))
			t.Cleanup(upstream.Close)
			schema := db.Config().ConnConfig.RuntimeParams["search_path"]
			node := startClusterNode(t, ctx, schema, upstream.URL)

			// Hold only this disposable schema's receipt trigger, after ledger UPDATE but
			// before COMMIT. SIGKILL must roll the entire settlement transaction back.
			lock, err := db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()
			lockName := "completion-crash-" + org.ID
			if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, lockName); err != nil {
				t.Fatal(err)
			}
			defer lock.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lockName)
			if _, err := db.Exec(ctx, `CREATE FUNCTION hold_completion_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended(TG_ARGV[0],0)); RETURN NEW; END $$`); err != nil {
				t.Fatal(err)
			}
			// IDs are server-generated base32, but quote trigger arguments as SQL literals.
			quoted := strings.ReplaceAll(lockName, "'", "''")
			if _, err := db.Exec(ctx, `CREATE TRIGGER hold_receipt BEFORE INSERT ON engine_completion_receipts FOR EACH ROW EXECUTE FUNCTION hold_completion_receipt('`+quoted+`')`); err != nil {
				t.Fatal(err)
			}
			finished := make(chan struct{})
			var body []byte
			go func() {
				defer close(finished)
				raw := fmt.Sprintf(`{"model":"deepseek/fixture","stream":%t,"max_tokens":32,"messages":[{"role":"user","content":"synthetic"}]}`, stream)
				r, _ := http.NewRequestWithContext(ctx, "POST", node.url+"/v1/chat/completions", strings.NewReader(raw))
				r.Header.Set("Authorization", "Bearer "+key.Secret)
				response, err := http.DefaultClient.Do(r)
				if err == nil {
					body, _ = io.ReadAll(response.Body)
					response.Body.Close()
				}
			}()
			var id string
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				var blocked bool
				_ = db.QueryRow(ctx, `SELECT b.request_id FROM engine_execution_bindings b JOIN engine_completion_events e USING(attempt_id)`).Scan(&id)
				_ = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND objid=((hashtextextended($1,0) & 4294967295)::bigint)::oid)`, lockName).Scan(&blocked)
				if id != "" && blocked {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("completion receipt did not block")
				case <-tick.C:
				}
			}
			if err := node.stop(true); err == nil {
				t.Fatal("child was not killed")
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("client not reclaimed")
			}
			if strings.Contains(string(body), "[DONE]") || (!stream && len(body) > 0) {
				t.Fatal("completion exposed before committed receipt")
			}
			r, err := ledger.Get(ctx, id)
			if err != nil || r.State != "dispatched" || r.Actual != nil {
				t.Fatal("killed settlement did not roll back")
			}
			if _, err := lock.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lockName); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `UPDATE engine_completion_events SET created_at=clock_timestamp()-interval '4 minutes'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `DROP TRIGGER hold_receipt ON engine_completion_receipts; DROP FUNCTION hold_completion_receipt()`); err != nil {
				t.Fatal(err)
			}
			restarted := startClusterNode(t, ctx, schema, upstream.URL)
			for {
				r, err = ledger.Get(ctx, id)
				if err == nil && r.State == "settled" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("new process did not reconcile completion")
				case <-tick.C:
				}
			}
			if r.Actual == nil || *r.Actual != 14 || calls.Load() != 1 {
				t.Fatal("recovery mischarged or replayed upstream")
			}
			var receipts int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM engine_completion_receipts`).Scan(&receipts); err != nil || receipts != 1 {
				t.Fatal("recovery receipt missing")
			}
			if err := restarted.stop(false); err != nil {
				t.Fatal("restart did not close cleanly")
			}
		})
	}
}
