package completion

import (
	"strings"
	"testing"
)

func TestMessagesOriginalUsageRequiresExplicitNativeCounters(t *testing.T) {
	const valid = `{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}`
	for _, raw := range []string{valid, strings.Replace(valid, `"input_tokens":1`, `"input_tokens":0`, 1), strings.Replace(valid, `"cache_read_input_tokens":2`, `"cache_read_input_tokens":0`, 1), strings.TrimSuffix(valid, "}") + `,"service_tier":"standard"}`} {
		c, err := ParseUsage("deepseek_messages_v1", []byte(raw))
		if err != nil || c.Cache == nil || c.Cache.Creation == nil || *c.Cache.Creation != 0 || c.Total != c.Input+c.Output {
			t.Fatal("explicit native proof rejected", err)
		}
	}
	bad := []string{`{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`, strings.Replace(valid, `,"cache_creation_input_tokens":0`, "", 1), strings.Replace(valid, `"input_tokens":1`, `"input_tokens":1.0`, 1), strings.Replace(valid, `"output_tokens":2`, `"output_tokens":null`, 1), strings.Replace(valid, `"cache_creation_input_tokens":0`, `"cache_creation_input_tokens":1`, 1), strings.Replace(valid, `"input_tokens":1`, `"input_tokens":1,"input_tokens":1`, 1)}
	for _, extra := range []string{`"service_tier":"priority"`, `"server_tool_use":{"web_search_requests":1}`, `"cache_creation":{"ephemeral_5m_input_tokens":0}`} {
		bad = append(bad, strings.TrimSuffix(valid, "}")+","+extra+"}")
	}
	for _, raw := range bad {
		if _, err := MessagesUsage([]byte(raw)); err == nil {
			t.Fatal("unknown or synthetic native evidence accepted", raw)
		}
	}
}
