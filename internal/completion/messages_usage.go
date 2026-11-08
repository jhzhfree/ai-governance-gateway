package completion

import "encoding/json"

// MessagesUsage interprets original native counters, never a synthesized Chat usage.
// DeepSeek currently reports explicit zero creation. Any other billing category
// needs a separate review/tariff, rather than quietly losing supplier costs.
func MessagesUsage(raw []byte) (Counters, error) {
	var c Counters
	if len(raw) < 2 || len(raw) > 8192 {
		return c, ErrInvalid
	}
	v, err := fields(raw)
	if err != nil {
		return c, err
	}
	read := func(key string) (int64, bool) {
		var n *int64
		err := json.Unmarshal(v[key], &n)
		if err != nil || n == nil || *n < 0 || *n > 2000000 {
			return 0, false
		}
		return *n, true
	}
	input, a := read("input_tokens")
	output, b := read("output_tokens")
	hit, d := read("cache_read_input_tokens")
	write, e := read("cache_creation_input_tokens")
	if !a || !b || !d || !e || write != 0 || input+hit < 1 || input+hit > 2000000 || output > 1000000 {
		return c, ErrInvalid
	}
	for k, value := range v {
		switch k {
		case "input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens":
		case "service_tier":
			var tier string
			if json.Unmarshal(value, &tier) != nil || tier != "standard" {
				return c, ErrInvalid
			}
		default:
			return c, ErrInvalid
		}
	}
	c.Input, c.Output, c.Total = input+hit, output, input+hit+output
	c.Cache = &CacheCounts{Read: hit, Uncached: input, Creation: &write}
	return c, nil
}
