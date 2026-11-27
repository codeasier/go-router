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

// Checked against the unmodified XiaomiTokenplanExecutor at 39a793a:
// buildUrl, buildHeaders and transformRequest, with its registry's native
// OpenAI transport and a fake credential. No provider is contacted here.
func TestXiaomiTokenPlanChatReference(t *testing.T) {
	for _, tc := range []struct {
		region, hostRegion, model, bodyStream, accept string
		stream                                        bool
	}{
		{"cn", "cn", "mimo-v2.6-pro", "false", "", false},
		{"ams", "ams", "mimo-v2.6-flash", "true", "application/json", true},
		{"sgp", "sgp", "mimo-v2.6-flash", "", "", true},
		{"unknown", "sgp", "mimo-v2.6-pro", "", "application/json", false},
		{"", "sgp", "mimo-v2.6-pro", "", "application/json, text/event-stream", true},
		{"CN", "sgp", "mimo-v2.6-pro", "false", "text/event-stream", false},
		{"https://example.invalid", "sgp", "mimo-v2.6-flash", "true", "", true},
	} {
		t.Run(tc.region+"/"+tc.bodyStream, func(t *testing.T) {
			raw := map[string]any{"model": "public-alias", "messages": []any{map[string]any{"role": "user", "content": "ping"}}, "temperature": 0.7, "stream_options": map[string]any{"include_usage": true}}
			if tc.bodyStream != "" {
				raw["stream"] = tc.bodyStream == "true"
			}
			src, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			p, err := PrepareXiaomiTokenPlanChat(tc.region, tc.model, src, tc.accept)
			if err != nil {
				t.Fatal(err)
			}
			r, err := p.NewRequest(context.Background(), "fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if p.Streaming() != tc.stream || r.Method != http.MethodPost || r.URL.String() != "https://token-plan-"+tc.hostRegion+".xiaomimimo.com/v1/chat/completions" || r.GetBody != nil {
				t.Fatal("region, method, replay, or stream mismatch")
			}
			expectedHeaders := http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer fixture-key"}}
			if tc.stream {
				expectedHeaders.Set("Accept", "text/event-stream")
			}
			if !reflect.DeepEqual(r.Header, expectedHeaders) {
				t.Fatalf("headers %v", r.Header)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if p.BodyDigest() != sha256.Sum256(body) {
				t.Fatal("incorrect dispatch digest")
			}
			raw["model"] = tc.model
			if !tc.stream {
				delete(raw, "stream_options")
			}
			var got map[string]any
			if json.Unmarshal(body, &got) != nil || !reflect.DeepEqual(got, raw) {
				t.Fatalf("body mismatch: %s, want %v", body, raw)
			}
		})
	}
}

func TestXiaomiTokenPlanChatBoundaries(t *testing.T) {
	for _, model := range []string{"mimo-v2.5-tts", "mimo-v2.5-asr", "xiaomi-tokenplan/mimo-v2.6-pro", "claude-mimo", "mimo-v2.5-pro", ""} {
		if p, err := PrepareXiaomiTokenPlanChat("cn", model, []byte(`{}`), ""); p != nil || !errors.Is(err, ErrRequest) {
			t.Fatal("non-chat or unregistered model accepted")
		}
	}
	for _, source := range []string{`{"stream":null}`, `{"stream":"false"}`, `{"model":"a","Model":"b"}`, `{"stream":true,"stream":false}`} {
		if p, err := PrepareXiaomiTokenPlanChat("cn", "mimo-v2.6-pro", []byte(source), ""); p != nil || !errors.Is(err, ErrRequest) {
			t.Fatal("ambiguous routing fields accepted")
		}
	}
	p, err := PrepareXiaomiTokenPlanChat("sgp", "mimo-v2.6-flash", []byte(`{"messages":[]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.NewRequest(context.Background(), "fixture\nsplit"); !errors.Is(err, ErrCredential) {
		t.Fatal("accepted malformed credential")
	}
}
