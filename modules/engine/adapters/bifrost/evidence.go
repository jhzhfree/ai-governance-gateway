package bifrost

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

// usageEvidence validates the captured provider frames before the SDK's maximum
// aggregation can hide repeated/inconsistent counters. Raw content stays local;
// only the usage object is retained as evidence.
type usageEvidence struct {
	stream   bool
	finished bool
	usage    *execution.Usage
	emitted  bool
	bytes    int
}

func (e *usageEvidence) observe(raw any, maxOutput int) error {
	var data []byte
	switch v := raw.(type) {
	case nil:
		if e.stream && e.finished {
			return nil
		}
		return execution.ErrUpstream
	case string:
		data = []byte(v)
	case json.RawMessage:
		data = v
	case []byte:
		data = v
	default:
		return execution.ErrUpstream
	}
	if len(bytes.TrimSpace(data)) == 0 && e.stream && e.finished {
		return nil
	}
	if len(bytes.TrimSpace(data)) == 0 || len(data) > 1<<20 {
		return execution.ErrUpstream
	}
	e.bytes += len(data)
	if e.bytes > 16<<20 {
		return execution.ErrUpstream
	}
	d := json.NewDecoder(bytes.NewReader(data))
	frames := 0
	for {
		var frame json.RawMessage
		err := d.Decode(&frame)
		if err == io.EOF {
			break
		}
		if err != nil || (e.stream && len(frame) > 64<<10) || (!e.stream && frames != 0) {
			return execution.ErrUpstream
		}
		frames++
		// Decode top-level keys individually so duplicate usage cannot be hidden.
		fd := json.NewDecoder(bytes.NewReader(frame))
		token, err := fd.Token()
		if err != nil || token != json.Delim('{') {
			return execution.ErrUpstream
		}
		var rawUsage json.RawMessage
		var choices []struct {
			FinishReason *string `json:"finish_reason"`
		}
		seen := map[string]bool{}
		for fd.More() {
			token, err := fd.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return execution.ErrUpstream
			}
			seen[key] = true
			var value json.RawMessage
			if fd.Decode(&value) != nil {
				return execution.ErrUpstream
			}
			switch key {
			case "usage":
				rawUsage = value
			case "choices":
				if json.Unmarshal(value, &choices) != nil {
					return execution.ErrUpstream
				}
			}
		}
		if _, err := fd.Token(); err != nil {
			return execution.ErrUpstream
		}
		if !e.stream {
			if len(choices) != 1 || choices[0].FinishReason == nil {
				return execution.ErrUpstream
			}
			switch *choices[0].FinishReason {
			case "stop", "length", "tool_calls", "content_filter":
			default:
				return execution.ErrUpstream
			}
		}
		for _, c := range choices {
			if c.FinishReason != nil && *c.FinishReason != "" {
				e.finished = true
			}
		}
		if len(rawUsage) == 0 || strings.TrimSpace(string(rawUsage)) == "null" {
			continue
		}
		if e.usage != nil || (e.stream && !e.finished) {
			return execution.ErrUpstream
		}
		fields, ok := object(rawUsage, "prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details", "completion_tokens_details", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens")
		if !ok {
			return execution.ErrUpstream
		}
		read := func(name string) (int, bool) {
			var v *int
			err := json.Unmarshal(fields[name], &v)
			if err != nil || v == nil {
				return 0, false
			}
			return *v, true
		}
		in, a := read("prompt_tokens")
		out, b := read("completion_tokens")
		total, c := read("total_tokens")
		if !a || !b || !c || in < 1 || in > 2000000 || out < 0 || out > maxOutput || total != in+out {
			return execution.ErrUpstream
		}
		// DeepSeek reports a cache split in addition to total prompt tokens.
		// Preserve evidence, but do not infer discounted prices from these fields.
		if len(fields["prompt_cache_hit_tokens"]) > 0 || len(fields["prompt_cache_miss_tokens"]) > 0 {
			hit, h := read("prompt_cache_hit_tokens")
			miss, m := read("prompt_cache_miss_tokens")
			if !h || !m || hit < 0 || miss < 0 || hit > in || miss != in-hit {
				return execution.ErrUpstream
			}
		}
		e.usage = &execution.Usage{InputTokens: in, OutputTokens: out, TotalTokens: total, Evidence: append(json.RawMessage(nil), rawUsage...)}
	}
	if frames == 0 {
		return execution.ErrUpstream
	}
	return nil
}
