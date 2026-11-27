package openaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Golden wire data obtained offline with OpenCodeGoExecutor at 39a793a;
// native models/payloads, API key and seed below are synthetic fixtures.
func TestOpenCodeGoGoldenWires(t *testing.T) {
	for _, tc := range []struct {
		model, src, want string
		sse              bool
	}{
		{"deepseek-v4-flash", `{"model":"client","response_format":{"type":"json_object"},"messages":[{"role":"assistant","content":"x"}],"stream_options":{"include_usage":true}}`,
			`{"model":"deepseek-v4-flash","messages":[{"role":"assistant","content":"x","reasoning_content":" "}]}`, true},
		{"kimi-k3", `{"model":"client","temperature":0.2,"messages":[{"role":"assistant","content":"x","tool_calls":[{}]}],"stream":true,"stream_options":{"include_usage":true}}`,
			`{"model":"kimi-k3","temperature":0.2,"messages":[{"role":"assistant","content":"x","tool_calls":[{}],"reasoning_content":" "}],"stream":true,"stream_options":{"include_usage":true}}`, true},
		{"glm-5.3", `{"model":"client","stream":false,"response_format":{"type":"json_object"},"stream_options":{"include_usage":true}}`,
			`{"model":"glm-5.3","stream":false,"response_format":{"type":"json_object"}}`, false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			p, err := PrepareOpenCodeGoChat(tc.model, []byte(tc.src), "", OpenCodeGoSession{Resolved: "fixture-42"})
			if err != nil {
				t.Fatal(err)
			}
			r, err := p.NewRequest(context.Background(), "fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			h := http.Header{}
			h.Set("Content-Type", "application/json")
			h.Set("Authorization", "Bearer fixture-key")
			h.Set("X-Opencode-Session", "ses_179bf5cceaff75b8e3f097fa01e5a0e7")
			if tc.sse {
				h.Set("Accept", "text/event-stream")
			}
			if !reflect.DeepEqual(r.Header, h) || p.Streaming() != tc.sse ||
				r.URL.String() != "https://opencode.ai/inference/go/openai/v1/chat/completions" || r.Method != "POST" || r.GetBody != nil {
				t.Fatal("wrong request envelope")
			}
			defer r.Body.Close()
			gotBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if json.Unmarshal(gotBody, &got) != nil || json.Unmarshal([]byte(tc.want), &want) != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("got %s, want %s", gotBody, tc.want)
			}
		})
	}
}

func TestOpenCodeGoRoutingAndSessions(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "grok-4.7", "gpt-6-luna", "muse-spark-1.3-contributor", "unknown", "omen-alpha", "GLM-5"} {
		if _, err := PrepareOpenCodeGoChat(model, []byte("{}"), "", OpenCodeGoSession{Native: "x"}); err == nil {
			t.Errorf("wrong native Chat lane for %s", model)
		}
	}
	for _, s := range []OpenCodeGoSession{{}, {Native: "a\r\nb"}, {Native: strings.Repeat("a", 257)}, {Resolved: strings.Repeat("🐧", 129)}, {Resolved: "a\x00b"}, {Resolved: "fixture-42", ClientTool: "bad\nname"}} {
		if _, err := PrepareOpenCodeGoChat("glm-5", []byte("{}"), "", s); err == nil {
			t.Fatal("accepted missing/unsafe session")
		}
	}
	seed := OpenCodeGoSession{Resolved: "fixture-42"}
	h, _ := seed.headerValue()
	h2, _ := (OpenCodeGoSession{Resolved: "fixture-42", ClientTool: "another-tool"}).headerValue()
	if h == h2 || !strings.HasPrefix(h2, "ses_") {
		t.Error("tool namespace not respected")
	}
	native := OpenCodeGoSession{Native: "  test-native-id  ", Resolved: "fixture-42", ClientTool: "deepseek-tui"}
	p, err := PrepareOpenCodeGoChat("mimo-v2-pro", []byte("{}"), "text/event-stream", native)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.NewRequest(context.Background(), "fixture")
	if err != nil || p.Streaming() || r.Header.Get("X-Opencode-Session") != "test-native-id" {
		t.Fatal("native session/tool priority")
	}
	for _, v := range []any{native, p} {
		if s := fmt.Sprintf("%v | %#v", v, v); strings.Contains(s, "test-native-id") || strings.Contains(s, "fixture-42") {
			t.Fatalf("session not redacted")
		}
	}
	if _, err = p.NewRequest(context.Background(), "bad\r\nkey"); err == nil {
		t.Error("invalid credential accepted")
	}
}

func TestOpenCodeGoMessageScopes(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"plain"},{"role":"assistant","reasoning_content":"keep","tool_calls":[{}]},{"role":"user","tool_calls":[{}]}]}`)
	p, err := PrepareOpenCodeGoChat("kimi-k3", body, "application/json", OpenCodeGoSession{Native: "s"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Messages []map[string]json.RawMessage }
	if json.Unmarshal(p.body, &got) != nil || len(got.Messages) != 3 || p.Streaming() ||
		got.Messages[0]["reasoning_content"] != nil || string(got.Messages[1]["reasoning_content"]) != `"keep"` || got.Messages[2]["reasoning_content"] != nil {
		t.Fatal("incorrect Kimi replayed-message scope")
	}
}
