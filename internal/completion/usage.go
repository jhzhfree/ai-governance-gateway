// Package completion owns immutable embedded-execution evidence and atomic receipts.
// It never imports an SDK or retains model content, credentials or supplier errors.
package completion

import (
	"bytes"
	"encoding/json"
	"io"
)

// Counters is the validated quantity projection of an original usage object.
type Counters struct {
	Input, Output, Total int64
	Cache                *CacheCounts
}

// CacheCounts contains only categories explicitly reported by this protocol.
type CacheCounts struct{ Read, Uncached int64 }

// ChatUsage rejects duplicate keys, arbitrary nested content and non-integers.
// Optional provider details are retained exactly, but do not imply a cache tariff.
func ChatUsage(raw []byte) (Counters, error) {
	var c Counters
	if len(raw) < 2 || len(raw) > 8192 {
		return c, ErrInvalid
	}
	v, err := fields(raw)
	if err != nil {
		return c, err
	}
	read := func(k string) (int64, bool) {
		var n *int64
		err := json.Unmarshal(v[k], &n)
		if err != nil || n == nil || *n < 0 || *n > 3000000 {
			return 0, false
		}
		return *n, true
	}
	var a, b, d bool
	c.Input, a = read("prompt_tokens")
	c.Output, b = read("completion_tokens")
	c.Total, d = read("total_tokens")
	if !a || !b || !d || c.Input < 1 || c.Input > 2000000 || c.Output > 1000000 || c.Total != c.Input+c.Output {
		return c, ErrInvalid
	}
	for k, value := range v {
		switch k {
		case "prompt_tokens", "completion_tokens", "total_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens":
			if _, ok := read(k); !ok {
				return c, ErrInvalid
			}
		case "prompt_tokens_details", "completion_tokens_details":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				continue
			}
			details, err := fields(value)
			if err != nil {
				return c, err
			}
			for name, n := range details {
				bound := c.Output
				if k == "prompt_tokens_details" {
					bound = c.Input
					if name != "cached_tokens" && name != "audio_tokens" {
						return c, ErrInvalid
					}
				} else if name != "reasoning_tokens" && name != "audio_tokens" && name != "accepted_prediction_tokens" && name != "rejected_prediction_tokens" {
					return c, ErrInvalid
				}
				var count *int64
				if json.Unmarshal(n, &count) != nil || count == nil || *count < 0 || *count > bound {
					return c, ErrInvalid
				}
			}
		default:
			return c, ErrInvalid
		}
	}
	if _, hit := v["prompt_cache_hit_tokens"]; hit || v["prompt_cache_miss_tokens"] != nil {
		h, hOK := read("prompt_cache_hit_tokens")
		m, mOK := read("prompt_cache_miss_tokens")
		if !hOK || !mOK || h > c.Input || m != c.Input-h {
			return c, ErrInvalid
		}
		if detail := v["prompt_tokens_details"]; detail != nil && !bytes.Equal(bytes.TrimSpace(detail), []byte("null")) {
			values, _ := fields(detail)
			if cached := values["cached_tokens"]; cached != nil {
				var n int64
				if json.Unmarshal(cached, &n) != nil || n != h {
					return c, ErrInvalid
				}
			}
		}
	}
	return c, nil
}

func fields(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, ErrInvalid
	}
	v := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || v[key] != nil {
			return nil, ErrInvalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrInvalid
		}
		v[key] = value
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrInvalid
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return nil, ErrInvalid
	}
	return v, nil
}
