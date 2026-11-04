package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

type inferenceModelList struct {
	Object string                    `json:"object"`
	Data   []provider.InferenceModel `json:"data"`
}

func TestInferenceModelsHonorKeyProjectCatalogAndRevocation(t *testing.T) {
	f := providerSetup(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("model directory contacted supplier") }))
	o, p, _ := providerProject(f)
	key := parse[control.IssuedKey](t, f.request(f.token("alice"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "Subset key", "models": []string{"aliyun/fixture", "zhipu/fixture"}, "ttl_seconds": 3600}, 201))
	data := f.request(key.Secret, "GET", "/v1/models", nil, 200)
	v := parse[inferenceModelList](t, data)
	if v.Object != "list" || len(v.Data) != 2 || v.Data[0].ID != "aliyun/fixture" || v.Data[1].ID != "zhipu/fixture" || v.Data[0].Created != 0 || v.Data[0].Object != "model" {
		t.Fatal("OpenAI model intersection wrong")
	}
	for _, forbidden := range []string{"price_version", "evidence_ref", "synthetic-AIGW", "upstream_model", "volcengine/fixture"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("model directory leaked configuration or unauthorized model")
		}
	}
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": []string{"zhipu/fixture"}}, 204)
	v = parse[inferenceModelList](t, f.request(key.Secret, "GET", "/v1/models", nil, 200))
	if len(v.Data) != 1 || v.Data[0].ID != "zhipu/fixture" {
		t.Fatal("project authorization change not live")
	}
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": []string{}}, 204)
	v = parse[inferenceModelList](t, f.request(key.Secret, "GET", "/v1/models", nil, 200))
	if len(v.Data) != 0 {
		t.Fatal("empty authorization was broadened")
	}
	for _, table := range []string{"budget_reservations", "inference_attempts", "admission_events", "supplier_circuits"} {
		var count int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("model listing caused inference side effects")
		}
	}
	f.request(f.token("alice"), "DELETE", projectPath(o, p)+"/keys/"+key.ID, nil, 204)
	f.request(key.Secret, "GET", "/v1/models", nil, 401)
	f.request(f.token("alice"), "GET", "/v1/models", nil, 401)
	f.request("", "GET", "/v1/models", nil, 401)
	f.request("aigw_pk_unknown", "GET", "/v1/models", nil, 401)
}
func TestMockModelListExpiryAndDatabaseFailure(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	key := f.key("alice", o, p)
	v := parse[inferenceModelList](t, f.request(key.Secret, "GET", "/v1/models", nil, 200))
	if len(v.Data) != 1 || v.Data[0].ID != "mock-text" || v.Data[0].OwnedBy != "mock" {
		t.Fatal("mock discovery wrong")
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE project_keys SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, key.ID); err != nil {
		t.Fatal(err)
	}
	f.request(key.Secret, "GET", "/v1/models", nil, 401)
	f.request(key.Secret, "POST", "/v1/models", nil, 405)
	f.pool.Close()
	f.request(key.Secret, "GET", "/v1/models", nil, 503)
}
