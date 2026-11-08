package completion

import (
	"bytes"
	"encoding/json"
)

// DeepSeekCacheUsage accepts only the reviewed text Chat hit/miss categories.
// Missing counts are unknown; audio/prediction/server-tool billing is not inferred.
func DeepSeekCacheUsage(raw []byte) (Counters, error) {
	c, err := ChatUsage(raw)
	if err != nil {
		return c, err
	}
	v, _ := fields(raw)
	var hit, miss *int64
	if json.Unmarshal(v["prompt_cache_hit_tokens"], &hit) != nil || hit == nil ||
		json.Unmarshal(v["prompt_cache_miss_tokens"], &miss) != nil || miss == nil {
		return c, ErrInvalid
	}
	for _, k := range []string{"prompt_tokens_details", "completion_tokens_details"} {
		if v[k] == nil || bytes.Equal(bytes.TrimSpace(v[k]), []byte("null")) {
			continue
		}
		values, err := fields(v[k])
		if err != nil {
			return c, err
		}
		allowed := "cached_tokens"
		if k == "completion_tokens_details" {
			allowed = "reasoning_tokens"
		}
		for name := range values {
			if name != allowed {
				return c, ErrInvalid
			}
		}
	}
	c.Cache = &CacheCounts{Read: *hit, Uncached: *miss}
	return c, nil
}
