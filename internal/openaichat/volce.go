package openaichat

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// VolceChatBaseURL is the fixed registry endpoint in the public 9router fork,
// build/v0.5.123-patch @ 39a793a, open-sse/config/volceapi.js.
// Per-request and per-connection URL overrides are not part of this provider.
const VolceChatBaseURL = "https://st8tp3ajl0df3n8b8l8qu.apigateway-cn-beijing.volceapi.com/v1"

// VolceChatRequest is a prepared native Chat request (not a Messages or
// Responses translator). The transport and exactly-one dispatch are owned by
// the caller. It retains no credential.
type VolceChatRequest struct{ *Prepared }

// PrepareVolceChat takes the catalog-selected upstream model and original
// client Accept, never a client-selected endpoint. Missing stream remains
// absent on the wire, even when negotiation selects SSE.
func PrepareVolceChat(model string, source []byte, accept string) (*VolceChatRequest, error) {
	a := Adapter{endpoint: VolceChatBaseURL + "/chat/completions"}
	p, err := a.Prepare(model, source)
	if err != nil {
		return nil, err
	}
	fields, ok := requestFields(p.body)
	if !ok {
		return nil, ErrRequest
	}
	if fields["stream"] == nil {
		p.stream = !strings.Contains(accept, "application/json") || strings.Contains(accept, "text/event-stream")
	}
	if !p.stream {
		delete(fields, "stream_options")
	}
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
			return nil, ErrRequest
		}
		changed := false
		for i, raw := range messages {
			var msg map[string]json.RawMessage
			if json.Unmarshal(raw, &msg) != nil || msg == nil {
				continue
			}
			var role, prior string
			if json.Unmarshal(msg["role"], &role) != nil || role != "assistant" {
				continue
			}
			if json.Unmarshal(msg["reasoning_content"], &prior) == nil && prior != "" {
				continue
			}
			if kimi {
				var calls []json.RawMessage
				if json.Unmarshal(msg["tool_calls"], &calls) != nil || len(calls) == 0 {
					continue
				}
			}
			msg["reasoning_content"] = json.RawMessage(`" "`)
			messages[i], err = json.Marshal(msg)
			if err != nil {
				return nil, ErrRequest
			}
			changed = true
		}
		if changed {
			fields["messages"], err = json.Marshal(messages)
			if err != nil {
				return nil, ErrRequest
			}
		}
	}
	p.body, err = json.Marshal(fields)
	if err != nil || len(p.body) > maxRequest {
		return nil, ErrRequest
	}
	return &VolceChatRequest{p}, nil
}

// Streaming returns the negotiated wire mode, which can differ from the
// presence of the stream key in the request body.
func (v *VolceChatRequest) Streaming() bool { return v.Prepared.stream }

// NewRequest omits the provider Accept override for JSON. It does not emit
// caller headers, follow redirects or open a connection.
func (v *VolceChatRequest) NewRequest(ctx context.Context, key string) (*http.Request, error) {
	if v == nil || v.Prepared == nil {
		return nil, ErrRequest
	}
	r, err := v.Prepared.NewRequest(ctx, key)
	if err == nil && !v.Streaming() {
		r.Header.Del("Accept")
	}
	return r, err
}
