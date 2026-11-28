package openaichat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func volceFixture(model string, source []byte, accept string) (*Prepared, error) {
	in, err := parseRequest(source)
	if err != nil {
		return nil, err
	}
	return PrepareVolceParsed(model, in, []string{accept})
}

func TestVolceChatReferenceWire(t *testing.T) {
	// Native Chat wire captured at 9router 39a793a, using a fake upstream.
	// No external endpoint is contacted.
	for _, tc := range []struct {
		name, model, input, accept, want string
		sse                              bool
	}{
		{"kimi", "kimi-k3", `{"model":"volceapi/kimi-k3","stream":false,"temperature":0.7,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"ping"}]}`, "application/json",
			`{"model":"kimi-k3","stream":false,"temperature":1,"messages":[{"role":"user","content":"ping"}]}`, false},
		{"ordinary", "demo", `{"model":"volceapi/demo","stream":false,"temperature":0.7,"messages":[]}`, "", `{"model":"demo","stream":false,"temperature":0.7,"messages":[]}`, false},
		{"explicit stream wins", "demo", `{"stream":true,"stream_options":{"include_usage":true},"messages":[]}`, "application/json", `{"model":"demo","stream":true,"stream_options":{"include_usage":true},"messages":[]}`, true},
		{"omitted default SSE", "demo", `{"messages":[]}`, "", `{"model":"demo","messages":[]}`, true},
		{"omitted explicit JSON", "demo", `{"messages":[]}`, "application/json", `{"model":"demo","messages":[]}`, false},
		{"omitted mixed", "demo", `{"messages":[]}`, "application/json, text/event-stream", `{"model":"demo","messages":[]}`, true},
		{"case insensitive override", "KIMI-K3", `{"messages":[]}`, "application/json", `{"model":"KIMI-K3","messages":[],"temperature":1}`, false},
		{"claude strip", "Claude-demo", `{"messages":[],"temperature":0.8,"top_p":0.2}`, "application/json", `{"model":"Claude-demo","messages":[],"top_p":0.2}`, false},
		{"kimi historical tool call", "kimi-k3", `{"messages":[{"role":"assistant","tool_calls":[{"id":"abc"}]},{"role":"assistant","content":"ok"},{"role":"assistant","tool_calls":[{}],"reasoning_content":"saved"}]}`, "application/json", `{"model":"kimi-k3","temperature":1,"messages":[{"role":"assistant","tool_calls":[{"id":"abc","type":"function"}],"reasoning_content":" "},{"role":"tool","tool_call_id":"abc","content":""},{"role":"assistant","content":"ok"},{"role":"assistant","tool_calls":[{"id":"call_msg2_tc0","type":"function"}],"reasoning_content":"saved"}]}`, false},
		{"deepseek historical assistant", "deepseek-v4", `{"messages":[{"role":"assistant","content":"done"},{"role":"user","content":"next"}]}`, "application/json", `{"model":"deepseek-v4","messages":[{"role":"assistant","content":"done","reasoning_content":" "},{"role":"user","content":"next"}]}`, false},
		{"native schema and common roles", "demo", `{"messages":[{"role":"developer","content":"yes"}],"tools":[],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}}`, "application/json", `{"model":"demo","messages":[{"role":"system","content":"yes"}],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := volceFixture(tc.model, []byte(tc.input), tc.accept)
			if err != nil {
				t.Fatal(err)
			}
			r, err := p.NewRequest(context.Background(), "fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if r.Method != "POST" || r.URL.String() != VolceChatBaseURL+"/chat/completions" || p.stream != tc.sse || r.GetBody != nil {
				t.Fatal("endpoint, method, streaming or replay contract")
			}
			expected := http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer fixture-key"}, "Accept": {"*/*"}}
			if tc.sse {
				expected.Set("Accept", "text/event-stream")
			}
			if !reflect.DeepEqual(expected, r.Header) {
				t.Fatalf("headers = %v", r.Header)
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if p.BodyDigest() != sha256.Sum256(b) {
				t.Fatal("intent digest differs from final payload")
			}
			var want, got any
			if json.Unmarshal([]byte(tc.want), &want) != nil || json.Unmarshal(b, &got) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("body %s, want %s", b, tc.want)
			}
		})
	}
}

func TestVolceChatRejectsAmbiguousRequests(t *testing.T) {
	for _, input := range []string{`{"stream":null}`, `{"stream":"no"}`, `{"stream":false,"stream":true}`, `{"Model":"other"}`, `{"model":"a","model":"b"}`, strings.Repeat("x", maxRequest+1)} {
		if p, err := volceFixture("demo", []byte(input), ""); p != nil || !errors.Is(err, ErrRequest) {
			t.Fatalf("bad request not rejected")
		}
	}
	p, err := volceFixture("demo", []byte(`{"messages":[]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.NewRequest(context.Background(), "fixture\nsplit"); !errors.Is(err, ErrCredential) {
		t.Fatal("invalid credential accepted")
	}
}

func TestVolceReasoningSplicePreservesOpaqueExtensionKeys(t *testing.T) {
	for _, prior := range []string{"", `,"reasoning_content":null`} {
		r, err := volceFixture("deepseek-test", []byte(`{"messages":[{"role":"assistant","content":"ok","\ud800":1,"\ufffd":2`+prior+`}]}`), "application/json")
		if err != nil {
			t.Fatal(err)
		}
		body := string(r.body)
		for _, want := range []string{`"\ud800":1`, `"\ufffd":2`, `"reasoning_content":" "`} {
			if !strings.Contains(body, want) {
				t.Fatal("lost extension field during reasoning splice")
			}
		}
	}
}

func TestVolceDeepSeekToolNamesFollowPinnedDedup(t *testing.T) {
	// Raw OpenAI tools: first exact name wins, preserving nested data, order
	// and unnamed entries. The root name has precedence over function.name.
	const first = `{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}},"extension":[2,1]}`
	const duplicate = `{"type":"function","function":{"name":"l\u006fokup","description":"later"}}`
	const upper = `{"type":"function","function":{"name":"Lookup"}}`
	const noName = `{"type":"function","function":{"name":""},"opaque":true}`
	const root = `{"type":"function","name":"lookup","function":{"name":"different"}}`
	const fallback = `{"type":"function","name":"","function":{"name":"lookup"}}`
	const order = `{"type":"function","function":{"name":"other"}}`
	const tools = first + "," + noName + "," + duplicate + "," + upper + "," + noName + "," + root + "," + fallback + "," + order
	const unique = first + "," + noName + "," + upper + "," + noName + "," + order
	src := []byte(`{"tools":[` + tools + `]}`)
	for _, tc := range []struct {
		name, model, want string
		generic           bool
	}{
		{"native", "deepseek-v4", unique, false},
		{"native last path component", "namespace/DeEpSeEk-v4", unique, false},
		{"nonmatching last path component", "deepseek-v4/demo", tools, false},
		{"contains but not prefix", "mini-deepseek-v4", tools, false},
		{"Kimi", "kimi-k3", tools, false},
		{"generic even with model prefix", "deepseek-v4", tools, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p *Prepared
			var err error
			if tc.generic {
				a, e := New("https://example.invalid/v1")
				if e != nil {
					t.Fatal(e)
				}
				p, err = a.Prepare(tc.model, src)
			} else {
				p, err = volceFixture(tc.model, src, "application/json")
			}
			if err != nil {
				t.Fatal(err)
			}
			fields, ok := requestFields(p.body)
			if !ok || string(fields["tools"]) != "["+tc.want+"]" {
				t.Fatalf("unexpected tools on wire: %s", fields["tools"])
			}
		})
	}
}

func TestVolceDeepSeekDedupKeepsOpaqueWireValues(t *testing.T) {
	const first = `{"function":{"name":"😀"}}`
	const same = `{"function":{"name":"\ud83d\ude00"}}`
	const lone = `{"function":{"name":"\ud800"}}`
	const replacement = `{"function":{"name":"\ufffd"}}`
	const noName = `{"function":{"name":0}}`
	const numeric = `{"name":1,"function":{"name":"fallback-not-used"}}`
	const sameNumeric = `{"function":{"name":1.0}}`
	const object = `{"name":{},"function":{"name":"fallback-not-used"}}`
	const src = first + "," + same + "," + lone + "," + lone + "," + replacement + "," + noName + "," + noName + "," + numeric + "," + sameNumeric + "," + object + "," + object
	const want = first + "," + lone + "," + replacement + "," + noName + "," + noName + "," + numeric + "," + object + "," + object
	p, err := volceFixture("deepseek-v4", []byte(`{"tools":[`+src+`]}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	fields, ok := requestFields(p.body)
	if !ok || string(fields["tools"]) != "["+want+"]" {
		t.Fatalf("unexpected tools on wire: %s", fields["tools"])
	}
}
