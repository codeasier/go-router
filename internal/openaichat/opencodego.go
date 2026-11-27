package openaichat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// OpenCodeGoChatBaseURL is the pinned registry's API-key Chat lane. Messages
// and Responses have different endpoints and are not handled by this adapter.
const OpenCodeGoChatBaseURL = "https://opencode.ai/inference/go/openai/v1"

// OpenCodeGoSession is the explicit input to the executor session transform.
// Native is the one selected x-opencode-session header, not other client
// headers. Resolved is a conversation identity selected by the caller's
// session resolver. ClientTool is a detected tool name (empty means generic).
// Neither a provider key nor a common model/provider ID is a conversation ID.
type OpenCodeGoSession struct{ Native, Resolved, ClientTool string }

func (OpenCodeGoSession) String() string     { return "OpenCode Go session (redacted)" }
func (v OpenCodeGoSession) GoString() string { return v.String() }

// OpenCodeGoChatModel reports whether the pinned curated catalog explicitly
// lists a native Chat lane. Avoid routing future/Responses-only/Claude-only
// models to Chat. Omen is a fallback guess even in the pinned reference.
func OpenCodeGoChatModel(model string) bool {
	switch model {
	case "deepseek-flash", "deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4.1-flash",
		"glm-5.3-flash", "glm-5.3", "glm-5.2", "glm-5.1", "glm-5",
		"kimi-k2.7-code", "kimi-k2.6", "kimi-k2.5", "kimi-k3", "kimi-k3.1-pro",
		"longcat-2.0", "mimo-v2.6-flash", "mimo-v2.6-pro", "mimo-v2.5", "mimo-v2.5-pro", "mimo-v2-pro", "mimo-v2-omni",
		"minimax-m3", "minimax-m2.7", "minimax-m2.5", "space-bunny-free",
		"qwen3.8-max", "qwen3.8-flash", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-plus", "qwen3.5-plus",
		"hy4-preview", "hy3", "hy3-preview":
		return true
	}
	return false
}

// OpenCodeGoChatRequest retains the derived session header, not a provider key.
type OpenCodeGoChatRequest struct {
	*Prepared
	session string
}

func (s OpenCodeGoSession) headerValue() (string, error) {
	// Normalize outer SP/HTAB, but never launder control characters or emit
	// untrusted non-ASCII bytes in a native HTTP header.
	native := strings.Trim(s.Native, " \t")
	if native != "" {
		if len(native) > 256 || strings.IndexFunc(native, func(r rune) bool { return r < 0x20 || r > 0x7e }) >= 0 {
			return "", ErrRequest
		}
		return native, nil
	}
	seed := strings.TrimSpace(s.Resolved)
	if seed == "" || len(seed) > 1024 || !utf8.ValidString(seed) || len(utf16.Encode([]rune(seed))) > 256 ||
		strings.ContainsFunc(seed, unicode.IsControl) || len(s.ClientTool) > 128 || !utf8.ValidString(s.ClientTool) || strings.ContainsFunc(s.ClientTool, unicode.IsControl) {
		return "", ErrRequest
	}
	tool := s.ClientTool
	if tool == "" {
		tool = "generic"
	}
	sum := sha256.Sum256([]byte("opencode-go\x00" + tool + "\x00" + seed))
	return "ses_" + hex.EncodeToString(sum[:16]), nil
}

// PrepareOpenCodeGoChat implements the native Chat executor boundary only,
// not the facade's tool detection, session discovery or protocol translation.
func PrepareOpenCodeGoChat(model string, source []byte, accept string, session OpenCodeGoSession) (*OpenCodeGoChatRequest, error) {
	if !OpenCodeGoChatModel(model) {
		return nil, ErrRequest
	}
	sid, err := session.headerValue()
	if err != nil {
		return nil, err
	}
	a := Adapter{endpoint: OpenCodeGoChatBaseURL + "/chat/completions"}
	p, err := a.Prepare(model, source)
	if err != nil {
		return nil, err
	}
	fields, ok := requestFields(p.body)
	if !ok {
		return nil, ErrRequest
	}
	// Pinned OpenCodeGoExecutor delegates super.transformRequest(model, body)
	// without passing stream. Only an explicit body stream:true keeps options.
	if !p.stream {
		delete(fields, "stream_options")
	}
	if fields["stream"] == nil {
		p.stream = session.ClientTool != "deepseek-tui" && (!strings.Contains(accept, "application/json") || strings.Contains(accept, "text/event-stream"))
	}
	kimi := strings.HasPrefix(model, "kimi-")
	deepseek := strings.HasPrefix(model, "deepseek")
	if deepseek {
		delete(fields, "response_format")
	}
	if v := fields["messages"]; (kimi || deepseek) && v != nil {
		var msgs []json.RawMessage
		if json.Unmarshal(v, &msgs) != nil {
			return nil, ErrRequest
		}
		changed := false
		for i, raw := range msgs {
			var m map[string]json.RawMessage
			var role, prior string
			if json.Unmarshal(raw, &m) != nil || json.Unmarshal(m["role"], &role) != nil || role != "assistant" ||
				(json.Unmarshal(m["reasoning_content"], &prior) == nil && prior != "") {
				continue
			}
			if kimi {
				var calls []json.RawMessage
				if json.Unmarshal(m["tool_calls"], &calls) != nil || len(calls) == 0 {
					continue
				}
			}
			m["reasoning_content"] = json.RawMessage(`" "`)
			msgs[i], err = json.Marshal(m)
			if err != nil {
				return nil, ErrRequest
			}
			changed = true
		}
		if changed {
			fields["messages"], err = json.Marshal(msgs)
			if err != nil {
				return nil, ErrRequest
			}
		}
	}
	p.body, err = json.Marshal(fields)
	if err != nil || len(p.body) > maxRequest {
		return nil, ErrRequest
	}
	return &OpenCodeGoChatRequest{p, sid}, nil
}

func (v *OpenCodeGoChatRequest) NewRequest(ctx context.Context, key string) (*http.Request, error) {
	if v == nil || v.Prepared == nil || v.session == "" {
		return nil, ErrRequest
	}
	r, err := v.Prepared.NewRequest(ctx, key)
	if err != nil {
		return nil, err
	}
	r.Header.Set("X-Opencode-Session", v.session)
	if !v.Streaming() {
		r.Header.Del("Accept")
	}
	return r, nil
}
