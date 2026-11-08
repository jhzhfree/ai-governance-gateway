package completion

import (
	"fmt"
	"strings"
	"testing"
)

func TestDeepSeekCacheOriginalCategories(t *testing.T) {
	for _, hit := range []int{0, 1, 3} {
		raw := fmt.Sprintf(`{ "prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":%d,"prompt_cache_miss_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}`, hit, 3-hit, hit)
		c, err := ParseUsage("deepseek_cache_v1", []byte(raw))
		if err != nil || c.Cache == nil || c.Cache.Read != int64(hit) || c.Cache.Uncached != int64(3-hit) {
			t.Fatal("explicit zero/all/mixed evidence rejected", err)
		}
	}
	base := `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":1}`
	bad := []string{
		strings.Replace(base, `,"prompt_cache_hit_tokens":2`, "", 1), strings.Replace(base, `,"prompt_cache_miss_tokens":1`, "", 1),
		strings.Replace(base, `"prompt_cache_hit_tokens":2`, `"prompt_cache_hit_tokens":null`, 1),
		strings.Replace(base, `"prompt_cache_hit_tokens":2`, `"prompt_cache_hit_tokens":2.0`, 1),
		strings.Replace(base, `"prompt_cache_hit_tokens":2`, `"prompt_cache_hit_tokens":-1`, 1),
		strings.Replace(base, `"prompt_cache_hit_tokens":2`, `"prompt_cache_hit_tokens":3`, 1),
		strings.Replace(base, `"prompt_cache_hit_tokens":2`, `"prompt_cache_hit_tokens":2,"prompt_cache_hit_tokens":2`, 1),
	}
	for _, extra := range []string{`"prompt_tokens_details":{"cached_tokens":1}`, `"prompt_tokens_details":{"audio_tokens":0}`, `"completion_tokens_details":{"audio_tokens":0}`, `"server_tool_use":{"web_search_requests":1}`, `"cached_creation_tokens":0`} {
		bad = append(bad, strings.TrimSuffix(base, "}")+","+extra+"}")
	}
	for _, raw := range bad {
		if _, err := DeepSeekCacheUsage([]byte(raw)); err == nil {
			t.Fatal("unproven cache or unreviewed categories accepted", raw)
		}
	}
}
