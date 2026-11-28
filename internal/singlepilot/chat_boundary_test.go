package singlepilot

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codeasier/go-router/internal/attemptlog"
)

func TestChatBodyBoundaryUsesActualNegotiation(t *testing.T) {
	const prefix = `{"model":"custom/test","messages":[{"role":"user","content":"<`
	const suffix = `"}],"stream_options":{"include_usage":true}}`
	body := func(size int, fill string) string {
		return prefix + strings.Repeat(fill, size-len(prefix)-len(suffix)) + suffix
	}

	for _, tc := range []struct {
		name, raw, accept, problem string
		status, wireSize           int
	}{
		{"real JSON fits once stream_options is removed", body(maxBody-1, "a"), "application/json", "", 200, 262101},
		{"SSE retains the field and actual wire size exceeds limit", strings.Replace(body(maxBody, "a"), prefix+"a", prefix+"<", 1), "text/event-stream", "invalid Chat body", 400, 0},
		{"real outbound exceeds limit after escaping", body(maxBody/2, "<"), "application/json", "invalid Chat body", 400, 0},
		{"raw input exceeds limit even if mapped request could fit", body(maxBody+1, "a"), "application/json", "Chat body too large", 413, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var intent attemptlog.Intent
			var commits, dispatched int
			h, err := routes("custom", []string{"test"}, "https://fixture.invalid/v1", "fixture", journalFunc(func(in attemptlog.Intent) error {
				commits++
				intent = in
				return nil
			}), roundTripFunc(func(r *http.Request) (*http.Response, error) {
				dispatched++
				defer r.Body.Close()
				wire, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if commits != 1 || intent.BodyDigest != sha256.Sum256(wire) || intent.Model != "custom/test" {
					t.Fatal("dispatch without the exact durable-body intent")
				}
				var fields map[string]json.RawMessage
				if len(wire) != tc.wireSize || r.Header.Get("Accept") != "*/*" ||
					json.Unmarshal(wire, &fields) != nil || len(fields) != 2 ||
					string(fields["model"]) != `"test"` || fields["messages"] == nil {
					t.Fatalf("unexpected wire payload (len=%d; accept=%q)", len(wire), r.Header.Get("Accept"))
				}
				return &http.Response{
					StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"object":"chat.completion","choices":[{}]}`)),
				}, nil
			}), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", tc.accept)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			wantCalls := 0
			if tc.status == 200 {
				wantCalls = 1
			}
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.problem) ||
				commits != wantCalls || dispatched != wantCalls {
				t.Fatalf("HTTP %d (%q), journal writes %d, dispatches %d", w.Code, w.Body.String(), commits, dispatched)
			}
		})
	}
}

func TestChatValidatesStructureBeforeLookingUpUnknownModel(t *testing.T) {
	h, err := routes("custom", []string{"known"}, "https://fixture.invalid/v1", "fixture",
		journalFunc(func(attemptlog.Intent) error {
			t.Error("journal write on rejected request")
			return nil
		}),
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("dispatch on rejected request")
			return nil, nil
		}), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw string
		status    int
	}{
		{"unknown but well-formed", `{"model":"custom/unknown","stream":false}`, 404},
		{"unknown with invalid stream", `{"model":"custom/unknown","stream":null}`, 400},
		{"unknown with duplicate stream", `{"model":"custom/unknown","stream":false,"st\u0072eam":true}`, 400},
		{"unknown with aliased stream", `{"model":"custom/unknown","Stream":false}`, 400},
		{"unknown with aliased model", `{"MODEL":"custom/unknown"}`, 400},
		{"unknown with duplicate model", `{"model":"custom/unknown","model":"custom/unknown"}`, 400},
		{"unknown with duplicate role", `{"model":"custom/unknown","messages":[{"role":"developer","role":"user"}]}`, 400},
		{"unknown followed by another object", `{"model":"custom/unknown"}{}`, 400},
		{"non-string model", `{"model":[]}`, 400},
		{"missing model", `{"stream":true}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.raw))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("HTTP %d (%q), want %d", w.Code, w.Body.String(), tc.status)
			}
		})
	}
}
