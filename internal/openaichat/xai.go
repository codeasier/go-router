package openaichat

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// XAIChatBaseURL is the xai registry's fixed native Chat origin/version in
// codeasier/9router@39a793a. Neither the request nor the key selects a URL.
const XAIChatBaseURL = "https://api.x.ai/v1"

// XAIChatRequest is an xAI API-key native Chat request for a catalog-selected
// Chat model. It is separate from xAI OAuth, Grok Web and the Grok CLI.
type XAIChatRequest struct{ *Prepared }

// PrepareXAIChat preserves Grok-native fields, including Structured Outputs.
// The executor negotiates SSE independently from whether the client supplied
// a stream key; it strips stream_options only for a non-streaming request.
func PrepareXAIChat(model string, source []byte, accept string) (*XAIChatRequest, error) {
	a := Adapter{endpoint: XAIChatBaseURL + "/chat/completions"}
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
	if !p.stream && fields["stream_options"] != nil {
		delete(fields, "stream_options")
		p.body, err = json.Marshal(fields)
		if err != nil {
			return nil, ErrRequest
		}
	}
	return &XAIChatRequest{p}, nil
}

// NewRequest takes only the configured API key. No OAuth or user-provided
// headers are involved. The reference sets an Accept override only for SSE.
func (p *XAIChatRequest) NewRequest(ctx context.Context, key string) (*http.Request, error) {
	if p == nil || p.Prepared == nil {
		return nil, ErrRequest
	}
	r, err := p.Prepared.NewRequest(ctx, key)
	if err == nil && !p.Streaming() {
		r.Header.Del("Accept")
	}
	return r, err
}
