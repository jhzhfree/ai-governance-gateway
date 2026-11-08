package completion

import "testing"

func TestEmbeddingOriginalUsage(t *testing.T) {
	for _, raw := range []string{`{"prompt_tokens":3,"total_tokens":3}`, `{ "total_tokens":3, "prompt_tokens":3,"completion_tokens":0 }`} {
		got, err := ParseUsage("embeddings_v1", []byte(raw))
		if err != nil || got != (Counters{3, 0, 3}) {
			t.Fatal(got, err)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"prompt_tokens":3}`, `{"prompt_tokens":3,"total_tokens":4}`, `{"prompt_tokens":0,"total_tokens":0}`, `{"prompt_tokens":3.0,"total_tokens":3}`, `{"prompt_tokens":3,"total_tokens":3,"completion_tokens":1}`, `{"prompt_tokens":3,"total_tokens":3,"completion_tokens":null}`, `{"prompt_tokens":3,"prompt_tokens":3,"total_tokens":3}`, `{"prompt_tokens":3,"total_tokens":3,"note":"content"}`, `{"prompt_tokens":2000001,"total_tokens":2000001}`} {
		if _, err := EmbeddingUsage([]byte(raw)); err == nil {
			t.Fatal("accepted invalid original usage", raw)
		}
	}
	if _, err := ParseUsage("unreviewed", []byte(`{"prompt_tokens":3,"total_tokens":3}`)); err == nil {
		t.Fatal("unknown format")
	}
	if _, err := ChatUsage([]byte(`{"prompt_tokens":3,"total_tokens":3}`)); err == nil {
		t.Fatal("Chat borrowed input-only evidence")
	}
}
