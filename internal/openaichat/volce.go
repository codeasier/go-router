package openaichat

import (
	"bytes"
	"encoding/json"

	"strings"
)

// VolceChatBaseURL is the fixed registry endpoint in the public 9router fork,
// build/v0.5.123-patch @ 39a793a, open-sse/config/volceapi.js.
// Per-request and per-connection URL overrides are not part of this provider.
const VolceChatBaseURL = "https://st8tp3ajl0df3n8b8l8qu.apigateway-cn-beijing.volceapi.com/v1"

// PrepareVolceParsed takes a catalog-selected upstream model. Missing stream
// stays absent on the wire; a schema is passed through without being rewritten
// into the generic executor's system prompt.
func PrepareVolceParsed(model string, input *ParsedRequest, accept []string) (*Prepared, error) {
	a := Adapter{endpoint: VolceChatBaseURL + "/chat/completions"}
	return a.prepareChat(model, input, accept, func(fields map[string]json.RawMessage) error {
		return applyVolceModel(model, fields)
	})
}

func applyVolceModel(model string, fields map[string]json.RawMessage) error {
	if strings.Contains(strings.ToLower(model), "claude") {
		delete(fields, "temperature")
	}
	if strings.EqualFold(model, "kimi-k3") {
		fields["temperature"] = json.RawMessage("1")
	}
	// The executor applies the family-specific rule to replayed assistant
	// turns, not to user/system messages. An existing non-empty string wins.
	lower := strings.ToLower(model)
	kimi, deepseek := strings.HasPrefix(lower, "kimi-"), strings.Contains(lower, "deepseek")
	if v := fields["messages"]; (kimi || deepseek) && v != nil {
		var messages []json.RawMessage
		if json.Unmarshal(v, &messages) != nil {
			return ErrRequest
		}
		changed := false
		for i, raw := range messages {
			msg, edit, err := volceReasoningMessage(raw, kimi)
			if err != nil {
				return err
			}
			if !edit {
				continue
			}
			messages[i] = msg
			changed = true
		}
		if changed {
			var err error
			fields["messages"], err = json.Marshal(messages)
			if err != nil {
				return ErrRequest
			}
		}
	}
	return nil
}

// Only splice the one selected member. Re-decoding every key into a Go map
// would collapse distinct extension keys containing escaped UTF-16 code units.
func volceReasoningMessage(raw json.RawMessage, kimi bool) (json.RawMessage, bool, error) {
	if len(raw) == 0 || raw[0] != '{' {
		return raw, false, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if _, err := d.Token(); err != nil {
		return nil, false, ErrRequest
	}
	var role, prior string
	var calls []json.RawMessage
	var seenRole, seenCalls, seenPrior bool
	start, end := 0, 0
	for d.More() {
		k, err := d.Token()
		var val json.RawMessage
		if err != nil || d.Decode(&val) != nil {
			return nil, false, ErrRequest
		}
		switch k {
		case "role":
			if seenRole {
				return nil, false, ErrRequest
			}
			seenRole = true
			_ = json.Unmarshal(val, &role)
		case "tool_calls":
			if seenCalls {
				return nil, false, ErrRequest
			}
			seenCalls = true
			_ = json.Unmarshal(val, &calls)
		case "reasoning_content":
			if seenPrior {
				return nil, false, ErrRequest
			}
			seenPrior = true
			_ = json.Unmarshal(val, &prior)
			end = int(d.InputOffset())
			start = end - len(val)
		}
	}
	if _, err := d.Token(); err != nil {
		return nil, false, ErrRequest
	}
	if role != "assistant" || prior != "" || (kimi && len(calls) == 0) {
		return raw, false, nil
	}
	replacement := []byte(`" "`)
	if !seenPrior {
		start = int(d.InputOffset()) - 1
		end = start
		// A matched assistant object already contains its role member.
		replacement = []byte(`,"reasoning_content":" "`)
	}
	out := make([]byte, 0, len(raw)+len(replacement))
	out = append(out, raw[:start]...)
	out = append(out, replacement...)
	out = append(out, raw[end:]...)
	return out, true, nil
}
