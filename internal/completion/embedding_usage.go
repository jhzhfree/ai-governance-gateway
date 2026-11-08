package completion

import "encoding/json"

// ParseUsage interprets the exact original supplier object according to the
// immutable execution binding. An embedding's output=0 is protocol semantics;
// it is never inserted into raw evidence that did not contain that field.
func ParseUsage(protocol string, raw []byte) (Counters, error) {
	switch protocol {
	case "chat_v1":
		return ChatUsage(raw)
	case "embeddings_v1":
		return EmbeddingUsage(raw)
	default:
		return Counters{}, ErrInvalid
	}
}
func EmbeddingUsage(raw []byte) (Counters, error) {
	var c Counters
	if len(raw) < 2 || len(raw) > 8192 {
		return c, ErrInvalid
	}
	v, err := fields(raw)
	if err != nil {
		return c, err
	}
	for k, value := range v {
		var n *int64
		if json.Unmarshal(value, &n) != nil || n == nil || *n < 0 || *n > 2000000 {
			return c, ErrInvalid
		}
		switch k {
		case "prompt_tokens":
			c.Input = *n
		case "total_tokens":
			c.Total = *n
		case "completion_tokens":
			if *n != 0 {
				return c, ErrInvalid
			}
		default:
			return c, ErrInvalid
		}
	}
	if c.Input < 1 || c.Total != c.Input {
		return c, ErrInvalid
	}
	return c, nil
}
