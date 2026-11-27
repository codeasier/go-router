package openaichat

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// XiaomiTokenPlanChatBaseURL resolves operator connection regions against the
// fixed registry in 9router 39a793a. A missing or unknown region falls back
// to sgp in that registry; no input string is interpolated into a URL.
func XiaomiTokenPlanChatBaseURL(region string) string {
	switch region {
	case "cn":
		return "https://token-plan-cn.xiaomimimo.com/v1"
	case "ams":
		return "https://token-plan-ams.xiaomimimo.com/v1"
	default:
		return "https://token-plan-sgp.xiaomimimo.com/v1"
	}
}

// XiaomiTokenPlanChatRequest implements only the native OpenAI Chat wire of
// the pinned provider, not the separate native Anthropic transport.
type XiaomiTokenPlanChatRequest struct{ *Prepared }

// PrepareXiaomiTokenPlanChat handles the two Chat models in the pinned
// registry. The caller supplies an operator-selected region and upstream model,
// not a client-selected endpoint. Audio entries belong to different routes.
func PrepareXiaomiTokenPlanChat(region, model string, source []byte, accept string) (*XiaomiTokenPlanChatRequest, error) {
	if model != "mimo-v2.6-pro" && model != "mimo-v2.6-flash" {
		return nil, ErrRequest
	}
	a := Adapter{endpoint: XiaomiTokenPlanChatBaseURL(region) + "/chat/completions"}
	p, err := a.Prepare(model, source)
	if err != nil {
		return nil, err
	}
	fields, ok := requestFields(p.body)
	if !ok {
		return nil, ErrRequest
	}
	if fields["stream"] == nil {
		// ChatCore negotiates the response mode without inserting a body field.
		p.stream = !strings.Contains(accept, "application/json") || strings.Contains(accept, "text/event-stream")
	}
	if !p.stream && fields["stream_options"] != nil {
		delete(fields, "stream_options")
		p.body, err = json.Marshal(fields)
		if err != nil {
			return nil, ErrRequest
		}
	}
	return &XiaomiTokenPlanChatRequest{p}, nil
}

// NewRequest sets only the API-key Bearer and pinned content negotiation.
// The prepared object retains no key; constructing the request sends nothing.
func (p *XiaomiTokenPlanChatRequest) NewRequest(ctx context.Context, key string) (*http.Request, error) {
	if p == nil || p.Prepared == nil {
		return nil, ErrRequest
	}
	r, err := p.Prepared.NewRequest(ctx, key)
	if err == nil && !p.Streaming() {
		r.Header.Del("Accept")
	}
	return r, err
}
