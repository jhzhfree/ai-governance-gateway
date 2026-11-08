package provider

import "errors"

// ValidateEmbeddedMessages limits the reviewed native conversion to ordered text
// conversation. System relocation and adjacent-role merging cannot change semantics.
func ValidateEmbeddedMessages(req Request) error {
	if req.IsEmbedding() || req.HasTools() || req.HasParts() || !ValidateRequest(req) || req.MaxTokens < 16 {
		return errors.New("unsupported native Messages request")
	}
	i := 0
	if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
		i = 1
	}
	if i == len(req.Messages) {
		return errors.New("native Messages requires user input")
	}
	next := "user"
	for ; i < len(req.Messages); i++ {
		if req.Messages[i].Role != next {
			return errors.New("native Messages requires alternating user and assistant text")
		}
		if next == "user" {
			next = "assistant"
		} else {
			next = "user"
		}
	}
	if next != "assistant" {
		return errors.New("native Messages requires final user input")
	}
	return nil
}
