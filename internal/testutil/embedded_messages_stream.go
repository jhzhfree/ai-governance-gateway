package testutil

import (
	"encoding/json"
	"fmt"
)

// EmbeddedMessagesStream is synthetic, explicitly cumulative native evidence.
func EmbeddedMessagesStream(initial, final, content string) string {
	quoted, _ := json.Marshal(content)
	return fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"fixture-model\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":%s}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":%s}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", initial, quoted, final)
}
