// Package chatwire decodes bounded OpenAI-compatible payloads and SSE frames.
// It does not own HTTP responses or issue transport calls.
package chatwire

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type Payload struct {
	Object  string            `json:"object"`
	Choices []json.RawMessage `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
	Error   json.RawMessage   `json:"error"`

	uniqueUsage bool
}

func ParsePayload(b []byte, kind string) (Payload, bool) {
	var p Payload
	if json.Unmarshal(b, &p) != nil || p.Object != kind || p.Choices == nil || p.Error != nil {
		return Payload{}, false
	}
	for _, choice := range p.Choices {
		if len(choice) == 0 || choice[0] != '{' {
			return Payload{}, false
		}
	}
	if p.Usage != nil {
		_, p.uniqueUsage = uniqueJSONFields(b, "usage")
	}
	return p, true
}

// uniqueJSONFields rejects duplicate wanted keys, including escape-equivalent
// keys and aliases folded by encoding/json. Other upstream extension fields
// are not interpreted. The JSON was already bounded by the HTTP relay.
func uniqueJSONFields(raw []byte, wanted ...string) (map[string]json.RawMessage, bool) {
	fields := make(map[string]json.RawMessage, len(wanted))
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		for _, name := range wanted {
			if strings.EqualFold(key, name) {
				if _, exists := fields[name]; exists {
					return nil, false
				}
				fields[name] = value
				break
			}
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false
	}
	_, err = d.Token()
	return fields, err == io.EOF
}

func (p Payload) ConfirmedUsage() *Usage {
	if !p.uniqueUsage {
		return nil
	}
	const promptKey, completionKey, totalKey = "prompt_tokens", "completion_tokens", "total_tokens"
	fields, ok := uniqueJSONFields(p.Usage, promptKey, completionKey, totalKey)
	var prompt, completion, total *int64
	if !ok || json.Unmarshal(fields[promptKey], &prompt) != nil ||
		json.Unmarshal(fields[completionKey], &completion) != nil || json.Unmarshal(fields[totalKey], &total) != nil ||
		prompt == nil || completion == nil || total == nil ||
		*prompt < 0 || *completion < 0 || *total < *prompt || *total-*prompt != *completion {
		return nil
	}
	return &Usage{PromptTokens: *prompt, CompletionTokens: *completion, TotalTokens: *total}
}
