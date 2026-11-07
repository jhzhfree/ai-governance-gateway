package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// messagesStreamWriter translates normalized text only. Input counters come from
// the authenticated initial wire snapshot; final events require the caller's
// durable completion proof, fee settlement and metadata finalization.
type messagesStreamWriter struct {
	http.ResponseWriter
	request       provider.Request
	id            string
	initial       *provider.MessagesUsage
	started, done bool
	textBytes     int
	reason        *string
	usage         *provider.Usage
}

func (w *messagesStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *messagesStreamWriter) frame(kind string, fields map[string]any) ([]byte, error) {
	fields["type"] = kind
	data, err := json.Marshal(fields)
	if err != nil || len(data) > 128<<10 {
		return nil, errors.New("Messages event exceeds boundary")
	}
	return []byte("event: " + kind + "\ndata: " + string(data) + "\n\n"), nil
}
func (w *messagesStreamWriter) emit(kind string, fields map[string]any) error {
	data, err := w.frame(kind, fields)
	if err != nil {
		return err
	}
	n, err := w.ResponseWriter.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
func (w *messagesStreamWriter) Write(data []byte) (int, error) {
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		return w.ResponseWriter.Write(data)
	}
	if w.done || !bytes.HasPrefix(data, []byte("data: ")) || !bytes.HasSuffix(data, []byte("\n\n")) {
		return 0, errors.New("invalid Messages stream state")
	}
	raw := bytes.TrimSuffix(bytes.TrimPrefix(data, []byte("data: ")), []byte("\n\n"))
	if bytes.Equal(raw, []byte("[DONE]")) {
		if !w.started || !w.initial.MatchesFinal(w.usage) {
			return 0, errors.New("Messages completion lacks evidence")
		}
		result, err := provider.PublicMessages(provider.Response{Usage: w.usage, Choices: []provider.Choice{{Message: &provider.Delta{Content: new("")}, FinishReason: w.reason}}})
		if err != nil {
			return 0, err
		}
		if err := w.emit("content_block_stop", map[string]any{"index": 0}); err != nil {
			return 0, err
		}
		if err := w.emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": result.StopReason, "stop_sequence": nil}, "usage": map[string]int64{"output_tokens": result.Usage.OutputTokens}}); err != nil {
			return 0, err
		}
		terminal, err := w.frame("message_stop", map[string]any{})
		if err != nil {
			return 0, err
		}
		failure, err := w.frame("error", map[string]any{"error": map[string]string{"type": "api_error", "message": "content_audit_unavailable"}, "request_id": w.id})
		if err != nil {
			return 0, err
		}
		var n int
		if audit, ok := w.ResponseWriter.(interface {
			writeTerminal([]byte, []byte) (int, error)
		}); ok {
			n, err = audit.writeTerminal(terminal, failure)
		} else {
			n, err = w.ResponseWriter.Write(terminal)
		}
		if err != nil {
			return 0, err
		}
		if n != len(terminal) {
			return 0, io.ErrShortWrite
		}
		w.done = true
		return len(data), nil
	}
	var value provider.Response
	if json.Unmarshal(raw, &value) != nil {
		return 0, errors.New("invalid normalized Messages event")
	}
	if len(value.Error) > 0 {
		var failure struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(value.Error, &failure) != nil || failure.Code == "" {
			return 0, errors.New("invalid Messages failure")
		}
		if err := w.emit("error", map[string]any{"error": map[string]string{"type": "api_error", "message": failure.Code}, "request_id": w.id}); err != nil {
			return 0, err
		}
		w.done = true
		return len(data), nil
	}
	if !w.started {
		if w.initial == nil {
			return 0, errors.New("missing initial Messages evidence")
		}
		start, err := w.frame("message_start", map[string]any{"message": map[string]any{"id": "msg_" + w.id, "type": "message", "role": "assistant", "model": w.request.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": w.initial}})
		if err != nil {
			return 0, err
		}
		block, err := w.frame("content_block_start", map[string]any{"index": 0, "content_block": provider.MessagesText{Type: "text", Text: ""}})
		if err != nil {
			return 0, err
		}
		initial := append(start, block...)
		n, err := w.ResponseWriter.Write(initial)
		if err != nil {
			return 0, err
		}
		if n != len(initial) {
			return 0, io.ErrShortWrite
		}
		w.started = true
	}
	for _, choice := range value.Choices {
		if choice.FinishReason != nil {
			w.reason = choice.FinishReason
		}
		if choice.Delta != nil && choice.Delta.Content != nil {
			text := *choice.Delta.Content
			if len(text) > 1<<20-w.textBytes {
				return 0, errors.New("Messages text exceeds boundary")
			}
			w.textBytes += len(text)
			if err := w.emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]string{"type": "text_delta", "text": text}}); err != nil {
				return 0, err
			}
		}
	}
	if value.Usage != nil {
		w.usage = value.Usage
	}
	return len(data), nil
}
