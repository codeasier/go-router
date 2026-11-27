package openaichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeGenericChat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		streaming bool
		input     string
		want      string
	}{
		{"strict provider nonstream", false,
			`{"messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}],"stream":false,"tools":[],"stream_options":{"include_usage":true}}`,
			`{"messages":[{"role":"system","content":"rules"},{"role":"user","content":"hi"}],"stream":false}`},
		{"omitted stream defaults to upstream SSE", true,
			`{"messages":[],"tools":[ ],"stream_options":{"include_usage":true}}`,
			`{"messages":[],"stream_options":{"include_usage":true}}`},
		{"omitted stream negotiated JSON", false,
			`{"messages":[],"stream_options":{"include_usage":true}}`, `{"messages":[]}`},
		{"explicit body true protects options", false,
			`{"messages":[],"stream":true,"stream_options":null}`, `{"messages":[],"stream":true,"stream_options":null}`},
		{"negotiated streaming protects options", true,
			`{"messages":[],"stream":false,"stream_options":null}`, `{"messages":[],"stream":false,"stream_options":null}`},
		{"absent messages skips translator not executor", false,
			`{"tools":[],"stream_options":null}`, `{"tools":[]}`},
		{"null messages skips tools", true, `{"messages":null,"tools":[]}`, `{"messages":null,"tools":[]}`},
		{"nonarray tools retained", false, `{"messages":[],"tools":null}`, `{"messages":[],"tools":null}`},
		{"exact case only", false,
			`{"messages":[{"Role":"developer","role":"Developer","more":{"role":"developer"}}],"Tools":[],"Stream_Options":true}`,
			`{"messages":[{"Role":"developer","role":"Developer","more":{"role":"developer"}}],"Tools":[],"Stream_Options":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields, ok := requestFields([]byte(tc.input))
			if !ok {
				t.Fatal("invalid test input")
			}
			if err := normalizeGenericChat(fields, tc.streaming); err != nil {
				t.Fatal(err)
			}
			want, _ := requestFields([]byte(tc.want))
			gotJSON, _ := json.Marshal(fields)
			wantJSON, _ := json.Marshal(want)
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestNormalizeGenericChatPreservesRawMessageContent(t *testing.T) {
	const messages = `[ { "r\u006fle" : "d\u0065veloper" , "content":[{"type":"text","text":"developer"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}],"extension":{"role":"developer","id":9007199254740993,"score":1.20e+03}}, {"role":"user","content":"developer"}, {"content":"rules","role" : "developer" } ]`
	const want = `[ { "r\u006fle" : "system" , "content":[{"type":"text","text":"developer"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}],"extension":{"role":"developer","id":9007199254740993,"score":1.20e+03}}, {"role":"user","content":"developer"}, {"content":"rules","role" : "system" } ]`
	const tools = `[{"type":"function","function":{"name":"probe"}}]`
	fields := map[string]json.RawMessage{"messages": []byte(messages), "tools": []byte(tools)}
	if err := normalizeGenericChat(fields, true); err != nil || string(fields["messages"]) != want || string(fields["tools"]) != tools {
		t.Fatalf("unrelated content or tools changed: %v", err)
	}
}

func TestNormalizeGenericChatRejectsAmbiguousRoleWithoutPartialMutation(t *testing.T) {
	const messages = `[{"role":"developer"},{"role":"user","r\u006fle":"developer"}]`
	fields := map[string]json.RawMessage{"messages": []byte(messages), "tools": []byte("[]"), "stream_options": []byte("null")}
	if err := normalizeGenericChat(fields, false); err != ErrRequest || string(fields["messages"]) != messages || len(fields) != 3 {
		t.Fatalf("ambiguous request was not rejected before rewriting fields: %v", err)
	}
	fields["messages"] = []byte(`["` + strings.Repeat("x", maxRequest) + `"]`)
	if err := normalizeGenericChat(fields, false); err != ErrRequest {
		t.Fatalf("oversized messages: %v", err)
	}
}
