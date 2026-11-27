package openaichat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func TestXAIChatAPIKeyWire(t *testing.T) {
	// Native request, headers and URL compared with DefaultExecutor("xai")
	// at 39a793a using only fake credentials; no supplier calls.
	const extra = `"messages":[{"role":"assistant","content":"ok"}],"temperature":0.7,"client_metadata":{"fixture":true},"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object"}}}`
	for _, tc := range []struct {
		name, accept, in, want string
		stream                 bool
	}{
		{"json", "", `{` + extra + `,"stream":false,"stream_options":{"include_usage":true}}`, `{` + extra + `,"stream":false,"model":"grok-4.7"}`, false},
		{"explicit SSE beats JSON Accept", "application/json", `{"model":"xai/grok-4.7","messages":[],"stream":true,"stream_options":{"include_usage":true}}`, `{"model":"grok-4.7","messages":[],"stream":true,"stream_options":{"include_usage":true}}`, true},
		{"explicit false beats SSE Accept", "text/event-stream", `{"stream":false,"stream_options":null}`, `{"model":"grok-4.7","stream":false}`, false},
		{"omitted stream stays absent", "", `{"messages":[],"stream_options":{"include_usage":true}}`, `{"model":"grok-4.7","messages":[],"stream_options":{"include_usage":true}}`, true},
		{"JSON only", "application/json", `{"stream_options":{"include_usage":true}}`, `{"model":"grok-4.7"}`, false},
		{"mixed Accept", "application/json, text/event-stream", `{}`, `{"model":"grok-4.7"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PrepareXAIChat("grok-4.7", []byte(tc.in), tc.accept)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r, err := p.NewRequest(ctx, "fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if p.Streaming() != tc.stream || r.Context() != ctx || r.Method != http.MethodPost || r.URL.String() != "https://api.x.ai/v1/chat/completions" || r.GetBody != nil {
				t.Fatal("wrong streaming mode, context, method, destination or replay contract")
			}
			headers := http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer fixture-key"}}
			if tc.stream {
				headers.Set("Accept", "text/event-stream")
			}
			if !reflect.DeepEqual(headers, r.Header) {
				t.Fatal("headers differ from native fixture")
			}
			wire, err := io.ReadAll(r.Body)
			if err != nil || sha256.Sum256(wire) != p.BodyDigest() {
				t.Fatal("request digest did not bind the exact wire body")
			}
			var got, want any
			if json.Unmarshal(wire, &got) != nil || json.Unmarshal([]byte(tc.want), &want) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("body = %s; want %s", wire, tc.want)
			}
		})
	}
}

func TestXAIChatInvalidRequestAndCredential(t *testing.T) {
	for _, in := range []string{`{"stream":null}`, `{"stream":false,"stream":true}`, `{"Model":"other"}`, `{}{}`} {
		if _, err := PrepareXAIChat("grok-3", []byte(in), ""); !errors.Is(err, ErrRequest) {
			t.Fatal("ambiguous/invalid request accepted")
		}
	}
	p, err := PrepareXAIChat("grok-3", []byte(`{}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "fake\r\nX-Test: 1"} {
		if _, err := p.NewRequest(context.Background(), key); !errors.Is(err, ErrCredential) {
			t.Fatal("empty or malformed key accepted")
		}
	}
}
