package openaichat

import (
	"bytes"
	"encoding/json"
)

// Inspect the message structure once, before looking up a requested model.
// Retain only the offsets needed to rewrite exact developer roles later.
type genericChatNormalization struct {
	messageArray   bool
	developerRoles [][2]int
}

func inspectGenericChat(fields map[string]json.RawMessage) (genericChatNormalization, error) {
	var result genericChatNormalization
	messages := bytes.TrimSpace(fields["messages"])
	if len(messages) > maxRequest {
		return result, ErrRequest
	}
	if len(messages) == 0 || messages[0] != '[' {
		return result, nil
	}
	result.messageArray = true
	d := json.NewDecoder(bytes.NewReader(messages))
	if _, err := d.Token(); err != nil {
		return result, ErrRequest
	}
	for d.More() {
		var message json.RawMessage
		if err := d.Decode(&message); err != nil {
			return result, ErrRequest
		}
		if message[0] != '{' {
			continue
		}
		start, end, err := developerRoleRange(message)
		if err != nil {
			return result, err
		}
		if end != 0 {
			base := int(d.InputOffset()) - len(message)
			result.developerRoles = append(result.developerRoles, [2]int{base + start, base + end})
		}
	}
	if _, err := d.Token(); err != nil {
		return result, ErrRequest
	}
	return result, nil
}

// apply mutates the prepared field map, not the structurally checked input.
// It implements the scoped 9router OpenAI filter and DefaultExecutor changes.
func (n genericChatNormalization) apply(fields map[string]json.RawMessage, upstreamStreaming bool) {
	if n.messageArray {
		if len(n.developerRoles) != 0 {
			messages := bytes.TrimSpace(fields["messages"])
			out := make([]byte, 0, len(messages))
			cursor := 0
			for _, span := range n.developerRoles {
				out = append(out, messages[cursor:span[0]]...)
				out = append(out, `"system"`...)
				cursor = span[1]
			}
			fields["messages"] = append(out, messages[cursor:]...)
		}

		// The reference OpenAI filter returns early without a messages array.
		tools := bytes.TrimSpace(fields["tools"])
		if len(tools) >= 2 && tools[0] == '[' && tools[len(tools)-1] == ']' && len(bytes.TrimSpace(tools[1:len(tools)-1])) == 0 {
			delete(fields, "tools")
		}
	}
	if !upstreamStreaming && !bytes.Equal(bytes.TrimSpace(fields["stream"]), []byte("true")) {
		delete(fields, "stream_options")
	}
}

// The message is valid JSON from Decode. Reject an ambiguous duplicate role
// rather than forwarding two role claims interpreted differently upstream.
func developerRoleRange(message json.RawMessage) (int, int, error) {
	d := json.NewDecoder(bytes.NewReader(message))
	_, _ = d.Token() // caller checked '{'
	var seen bool
	var start, end int
	for d.More() {
		key, err := d.Token()
		var value json.RawMessage
		if err != nil || d.Decode(&value) != nil {
			return 0, 0, ErrRequest
		}
		if key != "role" {
			continue
		}
		if seen {
			return 0, 0, ErrRequest
		}
		seen = true
		var role string
		if value[0] == '"' && json.Unmarshal(value, &role) == nil && role == "developer" {
			end = int(d.InputOffset())
			start = end - len(value)
		}
	}
	return start, end, nil
}
