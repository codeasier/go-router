package singlepilot

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/openaichat"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type journalFunc func(attemptlog.Intent) error

func (f journalFunc) RecordIntent(in attemptlog.Intent) error { return f(in) }

func TestChatDispatch(t *testing.T) {
	const completion = `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"upstream"},"finish_reason":"stop"}]}`
	chunk := `data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"upstream"}}]}` + "\n\n"
	for _, tc := range []struct {
		name, body, media, upstream, want string
		status, upstreamStatus, calls     int
		refuse, timeout                   bool
	}{
		{"JSON", `{"model":"custom/a"}`, "application/json", completion, "upstream", 200, 200, 1, false, false},
		{"SSE", `{"model":"custom/a","stream":true}`, "text/event-stream", chunk + "data: [DONE]\n\n", "[DONE]", 200, 200, 1, false, false},
		{"interrupted SSE", `{"model":"custom/a","stream":true}`, "text/event-stream", chunk, chunk, 200, 200, 1, false, false},
		{"external redirect", `{"model":"custom/a"}`, "application/json", "sensitive-refused", "invalid upstream", 502, 307, 1, false, false},
		{"upstream rate limit", `{"model":"custom/a"}`, "application/json", "sensitive-refused", "upstream rate limited", 429, 429, 1, false, false},
		{"upstream timeout", `{"model":"custom/a"}`, "application/json", "", "upstream timeout", 504, 0, 1, false, true},
		{"journal refused", `{"model":"custom/a"}`, "application/json", "", "journal unavailable", 503, 0, 0, true, false},
		{"wrong model", `{"model":"custom/b"}`, "application/json", "", "unknown model", 404, 0, 0, false, false},
		{"duplicate model", `{"model":"custom/a","model":"custom/a"}`, "application/json", "", "invalid Chat body", 400, 0, 0, false, false},
		{"bad stream", `{"model":"custom/a","stream":null}`, "application/json", "", "invalid Chat body", 400, 0, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var intent *attemptlog.Intent
			calls := 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if intent == nil || intent.BodyDigest != sha256.Sum256(raw) || intent.Model != "custom/a" || intent.Provider != "custom" {
					t.Fatalf("intent absent or wrong: %v", intent)
				}
				if r.URL.String() != "https://provider.invalid/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("Cookie") != "" || r.GetBody != nil || !strings.Contains(string(raw), `"model":"a"`) {
					t.Fatal("bad outbound request")
				}
				if tc.timeout {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: tc.upstreamStatus, Header: http.Header{"Content-Type": {tc.media}, "Location": {"https://other.invalid/denied"}}, Body: io.NopCloser(strings.NewReader(tc.upstream))}, nil
			})
			deadline := time.Second
			if tc.timeout {
				deadline = 30 * time.Millisecond
			}
			h, err := routes("custom", []string{"a"}, "https://provider.invalid/v1/", "fixture", journalFunc(func(in attemptlog.Intent) error {
				intent = &in
				if tc.refuse {
					return errors.New("unavailable")
				}
				return nil
			}), transport, deadline)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", "client-cookie")
			req.Header.Set("Authorization", "Bearer client-secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.status || calls != tc.calls || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("status %d calls %d body %q", w.Code, calls, w.Body.String())
			}
			if (tc.name == "interrupted SSE" && strings.Contains(w.Body.String(), "[DONE]")) || strings.Contains(w.Body.String(), "sensitive-refused") || w.Header().Get("Location") != "" {
				t.Fatal("unexpected replay or upstream metadata")
			}
		})
	}
}

func TestChatCanceledBeforeDispatch(t *testing.T) {
	h, err := routes("custom", []string{"a"}, "https://provider.invalid", "fixture",
		journalFunc(func(attemptlog.Intent) error { t.Fatal("recorded canceled request"); return nil }),
		roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("dispatched canceled request"); return nil, nil }), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"custom/a"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestVolceAndGenericShareCatalogWithoutSharingCredentials(t *testing.T) {
	headerConfig := `{"X-Tenant":"wrong","x-tenant":" operator-generic ","User-Agent":"generic-fixture-agent"}`
	generic, err := GenericConfigured("custom", []string{"same-model"}, "https://provider.invalid/v1", "custom-secret", &headerConfig)
	headerConfig = `{"X-Tenant":"cannot-change-snapshot"}`
	if err != nil {
		t.Fatal(err)
	}
	var intent attemptlog.Intent
	calls := 0
	h, err := connectionRoutes(journalFunc(func(in attemptlog.Intent) error { intent = in; return nil }), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if intent.BodyDigest != sha256.Sum256(data) {
			t.Fatal("digest did not match")
		}
		if r.Header.Get("X-Caller") != "" {
			t.Fatal("inlet headers crossed route boundary")
		}
		if calls == 1 {
			if r.Header.Get("X-Tenant") != "" || r.Header.Get("User-Agent") != "" {
				t.Fatal("generic operator headers reached Volce")
			}
			if r.URL.String() != openaichat.VolceChatBaseURL+"/chat/completions" || r.Header.Get("Authorization") != "Bearer volce-secret" ||
				r.Header.Get("Accept") != "text/event-stream" || strings.Contains(string(data), `"stream"`) ||
				!strings.Contains(string(data), `"temperature":1`) || intent.Model != "volceapi/kimi-k3" {
				t.Fatal("wrong Volce connection or wire")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"y"}}]}` + "\n\ndata: [DONE]\n\n"))}, nil
		}
		if r.Header.Get("X-Tenant") != "operator-generic" || r.Header.Get("User-Agent") != "generic-fixture-agent" {
			t.Fatal("generic operator snapshot missing")
		}
		if r.URL.String() != "https://provider.invalid/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer custom-secret" ||
			!strings.Contains(string(data), `"temperature":0.5`) || intent.Model != "custom/same-model" {
			t.Fatal("wrong generic connection or wire")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"y"}}]}`))}, nil
	}), time.Second, generic, Volce([]string{"kimi-k3"}, "volce-secret"))
	if err != nil {
		t.Fatal(err)
	}
	models := httptest.NewRecorder()
	h.ServeHTTP(models, httptest.NewRequest("GET", "/v1/models", nil))
	if !strings.Contains(models.Body.String(), "volceapi/kimi-k3") || !strings.Contains(models.Body.String(), "custom/same-model") {
		t.Fatal("incomplete model listing")
	}
	for _, tc := range []struct{ model, media string }{{"volceapi/kimi-k3", "text/event-stream"}, {"custom/same-model", "application/json"}} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+tc.model+`","temperature":0.5}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant", "incoming-tenant")
		req.Header.Set("X-Caller", "private-input")
		req.Header.Set("User-Agent", "caller")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != 200 || res.Header().Get("Content-Type") != tc.media {
			t.Fatalf("%s: %d %q", tc.model, res.Code, res.Body.String())
		}
	}
	if calls != 2 {
		t.Fatal("dispatch count", calls)
	}
}
