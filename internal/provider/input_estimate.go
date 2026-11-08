package provider

import "errors"

var ErrEstimatedInputLimit = errors.New("estimated input exceeds reviewed limit")

// EstimatedInputTokens is deliberately conservative for the reviewed text
// subset: UTF-8 bytes plus fixed framing allowances, never a billing counter or
// a claim to reproduce a supplier's proprietary tokenizer/template. It avoids
// network calls and cannot authorize tools or media by treating them as text.
func EstimatedInputTokens(req Request) (int64, error) {
	if !ValidateRequest(req) || req.HasTools() || req.HasParts() {
		return 0, ErrEstimatedInputLimit
	}
	estimate := int64(32)
	if req.IsEmbedding() {
		for _, input := range req.Input {
			estimate += int64(len(input)) + 16
		}
	} else {
		for _, message := range req.Messages {
			estimate += int64(len(message.Role)+len(message.Content)) + 32
		}
	}
	return estimate, nil
}

func (r *Route) CheckEstimatedInput(req Request) error {
	if r == nil {
		return ErrEstimatedInputLimit
	}
	estimate, err := EstimatedInputTokens(req)
	if err != nil || estimate > r.model.MaxInputTokens {
		return ErrEstimatedInputLimit
	}
	return nil
}
