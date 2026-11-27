package openaichat

import (
	"bytes"
	"encoding/json"
)

// normalizeGenericChat applies the ordinary generic Chat rules shared with
// 9router's OpenAI filter and DefaultExecutor. fields must come from the
// bounded requestFields decoder. upstreamStreaming is the negotiated upstream
// mode, not simply the value of the optional client stream field.
func normalizeGenericChat(fields map[string]json.RawMessage, upstreamStreaming bool) error {
	messages := bytes.TrimSpace(fields["messages"])
	if len(messages) > maxRequest {
		return ErrRequest
	}
	if len(messages) > 0 && messages[0] == '[' {
		normalized, err := normalizeDeveloperRoles(messages)
		if err != nil {
			return err
		}
		if normalized != nil {
			fields["messages"] = normalized
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
	return nil
}

// Rewrite only the exact role value in each message object, not arbitrary
// strings, content parts, nested vendor fields, or their numeric representations.
// A nil result means nothing changed. Every replacement shortens the input.
func normalizeDeveloperRoles(messages []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(messages))
	if _, err := d.Token(); err != nil { // caller checked the opening '['
		return nil, ErrRequest
	}
	var out []byte
	cursor := 0
	for d.More() {
		var message json.RawMessage
		if err := d.Decode(&message); err != nil {
			return nil, ErrRequest
		}
		if message[0] != '{' {
			continue
		}
		start, end, err := developerRoleRange(message)
		if err != nil {
			return nil, err
		}
		if end != 0 {
			base := int(d.InputOffset()) - len(message)
			if out == nil {
				out = make([]byte, 0, len(messages))
			}
			out = append(out, messages[cursor:base+start]...)
			out = append(out, `"system"`...)
			cursor = base + end
		}
	}
	if _, err := d.Token(); err != nil {
		return nil, ErrRequest
	}
	if out != nil {
		out = append(out, messages[cursor:]...)
	}
	return out, nil
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
