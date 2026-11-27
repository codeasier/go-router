package openaichat

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Captured from translateRequest(openai, openai) -> DefaultExecutor at
// codeasier/9router 39a793a282629ad8f1bfd6ce703e5dad26be700a. The first
// vector is the original observed difference, not a generated synthetic ID.
func TestGenericToolCorrelationReference(t *testing.T) {
	vectors := []struct{ name, input, want string }{
		{"captured missing id and next user",
			`[{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"user","content":"after"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"f","arguments":"{}"},"id":"call_msg0_tc0_f"}]},{"role":"tool","tool_call_id":"call_msg0_tc0_f","content":""},{"role":"user","content":"after"}]`},
		{"trailing call with no next turn",
			`[{"role":"assistant","content":"","tool_calls":[{"function":{"name":"fn","arguments":"{}"}}]}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"function":{"name":"fn","arguments":"{}"},"id":"call_msg0_tc0_fn","type":"function"}]}]`},
		{"known valid call and matching result",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"S12_-","function":{"name":{"invalid":"retained"},"arguments":"not json"},"type":"function"}]},{"role":"tool","tool_call_id":"S12_-","content":"genuine"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"S12_-","function":{"name":{"invalid":"retained"},"arguments":"not json"},"type":"function"}]},{"role":"tool","tool_call_id":"S12_-","content":"genuine"}]`},
		{"partial parallel next result only",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"one","function":{"name":"fn","arguments":"{}"}},{"id":"TWO_2","function":{"name":"fn","arguments":"{}"}}]},{"role":"tool","tool_call_id":"TWO_2","content":"genuine"},{"role":"user","content":"after"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"one","function":{"name":"fn","arguments":"{}"},"type":"function"},{"id":"TWO_2","function":{"name":"fn","arguments":"{}"},"type":"function"}]},{"role":"tool","tool_call_id":"TWO_2","content":"genuine"},{"role":"user","content":"after"}]`},
		{"mismatched next result with later real result",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"one","function":{"name":"fn","arguments":"{}"}},{"id":"two","function":{"name":"fn","arguments":"{}"}}]},{"role":"tool","tool_call_id":"unknown","content":"unrelated result"},{"role":"tool","tool_call_id":"two","content":"genuine"},{"role":"user","content":"after"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"one","function":{"name":"fn","arguments":"{}"},"type":"function"},{"id":"two","function":{"name":"fn","arguments":"{}"},"type":"function"}]},{"role":"tool","tool_call_id":"one","content":""},{"role":"tool","tool_call_id":"two","content":""},{"role":"tool","tool_call_id":"unknown","content":"unrelated result"},{"role":"tool","tool_call_id":"two","content":"genuine"},{"role":"user","content":"after"}]`},
		{"adjacent calls use original indexes",
			`[{"role":"user","content":"after"},{"role":"assistant","content":"","tool_calls":[{"function":{"name":"猫 -a?","arguments":"{}"}}]},{"role":"assistant","content":"","tool_calls":[{"function":{"name":"","arguments":"{}"}},{"function":{"name":"!?","arguments":"{}"}}]},{"role":"user","content":"after"}]`,
			`[{"role":"user","content":"after"},{"role":"assistant","content":"","tool_calls":[{"function":{"name":"猫 -a?","arguments":"{}"},"id":"call_msg1_tc0_-a","type":"function"}]},{"role":"tool","tool_call_id":"call_msg1_tc0_-a","content":""},{"role":"assistant","content":"","tool_calls":[{"function":{"name":"","arguments":"{}"},"id":"call_msg2_tc0","type":"function"},{"function":{"name":"!?","arguments":"{}"},"id":"call_msg2_tc1_","type":"function"}]},{"role":"tool","tool_call_id":"call_msg2_tc0","content":""},{"role":"tool","tool_call_id":"call_msg2_tc1_","content":""},{"role":"user","content":"after"}]`},
		{"both sides receive same sanitation",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"id/1:abc","function":{"name":"fn","arguments":"{}"}}]},{"role":"tool","tool_call_id":"id/1:abc","content":"genuine"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"id1abc","function":{"name":"fn","arguments":"{}"},"type":"function"}]},{"role":"tool","tool_call_id":"id1abc","content":"genuine"}]`},
		{"empty sanitation result IDs use own indices",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"!!!","function":{"name":"猫","arguments":"{}"}}]},{"role":"tool","tool_call_id":"!!!","content":"genuine"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"call_msg0_tc0_","function":{"name":"猫","arguments":"{}"},"type":"function"}]},{"role":"tool","tool_call_id":"call_msg0_tc0_","content":""},{"role":"tool","tool_call_id":"call_msg1_tc0","content":"genuine"}]`},
		{"JSON stringify argument property order",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"x","arguments":{"1":"one","2":"two","zz":"<>&","next":[true,null,{"d":"x"}],"n":0,"e":1e-07}}}]},{"role":"user","content":"after"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"x","arguments":"{\"1\":\"one\",\"2\":\"two\",\"zz\":\"<>&\",\"next\":[true,null,{\"d\":\"x\"}],\"n\":0,\"e\":1e-7}"},"type":"function"}]},{"role":"tool","tool_call_id":"a","content":""},{"role":"user","content":"after"}]`},
		{"arguments false null 0 empty remain",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"f","arguments":false}},{"id":"b","function":{"name":"f","arguments":null}},{"id":"c","function":{"name":"f","arguments":0}},{"id":"d","function":{"name":"f","arguments":""}}]},{"role":"user","content":"after"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"f","arguments":false},"type":"function"},{"id":"b","function":{"name":"f","arguments":null},"type":"function"},{"id":"c","function":{"name":"f","arguments":0},"type":"function"},{"id":"d","function":{"name":"f","arguments":""},"type":"function"}]},{"role":"tool","tool_call_id":"a","content":""},{"role":"tool","tool_call_id":"b","content":""},{"role":"tool","tool_call_id":"c","content":""},{"role":"tool","tool_call_id":"d","content":""},{"role":"user","content":"after"}]`},
		{"arguments true numeric array object",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"f","arguments":true}},{"id":"b","function":{"name":"f","arguments":42}},{"id":"c","function":{"name":"f","arguments":[]}},{"id":"d","function":{"name":"f","arguments":{}}}]},{"role":"user","content":"after"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"f","arguments":"true"},"type":"function"},{"id":"b","function":{"name":"f","arguments":"42"},"type":"function"},{"id":"c","function":{"name":"f","arguments":"[]"},"type":"function"},{"id":"d","function":{"name":"f","arguments":"{}"},"type":"function"}]},{"role":"tool","tool_call_id":"a","content":""},{"role":"tool","tool_call_id":"b","content":""},{"role":"tool","tool_call_id":"c","content":""},{"role":"tool","tool_call_id":"d","content":""},{"role":"user","content":"after"}]`},
		{"untouched vendor and input result",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"id","function":{"name":"f","arguments":"{}"},"type":"custom","vendor":"keep"}],"vendor":{"deep":true}},{"role":"tool","tool_call_id":"id","content":[{"type":"text","text":"keep actual output"}]}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"id","function":{"name":"f","arguments":"{}"},"type":"custom","vendor":"keep"}],"vendor":{"deep":true}},{"role":"tool","tool_call_id":"id","content":[{"type":"text","text":"keep actual output"}]}]`},
		{"null and absent result IDs remain",
			`[{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"f","arguments":"x"},"type":null}]},{"role":"tool","tool_call_id":null,"content":"genuine"},{"role":"tool","content":"no-id"},{"role":"user","content":"after"}]`,
			`[{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"f","arguments":"x"},"type":"function"}]},{"role":"tool","tool_call_id":"a","content":""},{"role":"tool","tool_call_id":null,"content":"genuine"},{"role":"tool","content":"no-id"},{"role":"user","content":"after"}]`},
		{"escaped literal text and block types",
			`[{"role":"user","content":[{"type":"text","text":"[{\"tool_use\":true}]"}]},{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"fn","arguments":"{}"}}]},{"role":"user","content":"after"}]`,
			`[{"role":"user","content":[{"type":"text","text":"[{\"tool_use\":true}]"}]},{"role":"assistant","content":"","tool_calls":[{"id":"a","function":{"name":"fn","arguments":"{}"},"type":"function"}]},{"role":"tool","tool_call_id":"a","content":""},{"role":"user","content":"after"}]`},
	}
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			src := json.RawMessage(v.input)
			fields := map[string]json.RawMessage{"messages": src, "extension": json.RawMessage(`{"role":"developer"}`)}
			if err := applyGenericChatToolCorrelation(fields); err != nil {
				t.Fatal(err)
			}
			var got, want any
			if json.Unmarshal(fields["messages"], &got) != nil || json.Unmarshal([]byte(v.want), &want) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s; want %s", fields["messages"], v.want)
			}
			if string(src) != v.input || string(fields["extension"]) != `{"role":"developer"}` {
				t.Fatal("modified input or unrelated field")
			}
		})
	}
}

func TestCorrelationAtomicAndBounded(t *testing.T) {
	const call = `{"id":"a","type":"function"}`
	manyCalls := `[{"role":"assistant","tool_calls":[` + strings.Repeat(call+",", maxCorrelationCalls) + call + `]}]`
	manyMessages := strings.Repeat(`{"role":"user","content":"x"},`, maxCorrelationMessages-1)
	cases := []string{
		manyCalls,
		"[" + manyMessages + `{},{"role":"tool","tool_call_id":"x"}]`,
		"[" + manyMessages + `{"role":"assistant","tool_calls":[{}]},{}]`,
		`[{"role":"assistant","tool_calls":[],"extension":` + strings.Repeat("[", maxCorrelationDepth) + strings.Repeat("]", maxCorrelationDepth) + "}]",
		`[{"role":"user","content":"` + strings.Repeat("x", maxRequest) + `"}]`,
		// Each input is within the byte limit, but duplicating its ID in a result is not.
		`[{"role":"assistant","tool_calls":[{"id":"` + strings.Repeat("a", maxRequest/2) + `","type":"function"}]},{"role":"user","content":"x"}]`,
		`[{"role":"assistant","tool_calls":[{"id":"a/b"},{"id":"ab"}]},{"role":"tool","tool_call_id":"ab","content":"R"}]`,
		`[{"role":"assistant","tool_calls":[{},{"id":"call_msg0_tc0"}]}]`,
		`[{"role":"assistant","tool_calls":[{"id":"x","\u0069d":"other"}]}]`,
		`[{"role":"assistant","tool_calls":[{"function":{"arguments":{"a":1,"a":2}}}]}]`,
		`[{"role":"assistant","tool_calls":[{"id":"\ufffd","\ud800":1,"\ufffd":2}]}]`,
		`[{"role":"assistant","tool_calls":[{"function":{"arguments":{"k":"\ud800"}}}]}]`,
		`[{"role":"assistant","tool_calls":[{}]},{"role":"tool","tool_call_id":7}]`,
		`[{"role":"assistant","tool_calls":[{"id":true}]}]`,
		`[{"role":"assistant","tool_calls":[{"function":{"name":7}}]}]`,
		`[{"role":"assistant","tool_calls":[{"type":[]}]}]`,
		`[{"role":"assistant","tool_calls":[null]}]`,
	}
	for i, in := range cases {
		fields := map[string]json.RawMessage{"messages": json.RawMessage(in)}
		if err := applyGenericChatToolCorrelation(fields); !errors.Is(err, ErrRequest) || string(fields["messages"]) != in {
			t.Fatalf("case %d: expected atomic rejection, got %v", i, err)
		}
	}
}

func TestCorrelationNoChange(t *testing.T) {
	for _, in := range []string{
		// A nested lookalike must not apply stricter tool-history limits.
		`[{"role":"user","extension":{"tool_calls":[], "deep":` + strings.Repeat("[", maxCorrelationDepth) + strings.Repeat("]", maxCorrelationDepth) + `}}]`,
		`[{"role":"user","content":"text","\ud801":1,"\ufffd":2}]`,
		"null", `{}`, "[]",
		`[  {"role":"assistant","tool_calls":[{"type":"function","id":"a","function":{"arguments":"{bad"}}]}, {"role":"tool","content":"answer","tool_call_id":"a"}  ]`,
		`[ {"role":"user","content":[{"type":"image_url","image_url":{"url":"local"}}]} ]`,
		`[ {"role":"assistant","tool_calls":[{}]}, {"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"real"}]} ]`,
		`[ {"role":"assistant","content":[{"type":"tool_use","id":"x"}]}, {"role":"user","content":"next"} ]`,
	} {
		fields := map[string]json.RawMessage{"messages": json.RawMessage(in)}
		if err := applyGenericChatToolCorrelation(fields); err != nil || string(fields["messages"]) != in {
			t.Fatalf("non-target changed: %q, %v", in, err)
		}
	}
	if err := applyGenericChatToolCorrelation(nil); err != nil {
		t.Fatal(err)
	}
}

func TestCorrelationExpansionBoundAndEscapes(t *testing.T) {
	escaped := map[string]json.RawMessage{"messages": json.RawMessage(`[{"role":"assistant","t\u006fol_calls":[{}]},{"role":"user","content":"x"}]`)}
	if err := applyGenericChatToolCorrelation(escaped); err != nil || !bytes.Contains(escaped["messages"], []byte("call_msg0_tc0")) {
		t.Fatal("escaped protocol key not recognized")
	}
	const msg = `{"role":"user","content":"x"}`
	// At the message limit BEFORE inserting a placeholder.
	in := `[{"role":"assistant","tool_calls":[{}]},` + strings.Repeat(msg+",", maxCorrelationMessages-2) + msg + "]"
	fields := map[string]json.RawMessage{"messages": json.RawMessage(in)}
	if err := applyGenericChatToolCorrelation(fields); err != ErrRequest || !bytes.Equal(fields["messages"], []byte(in)) {
		t.Fatal("expanded message count not bounded")
	}
	// Opaque nested data, including exact large numbers and lone escaped UTF-16
	// values, need not be decoded or re-keyed just because another field changed.
	in = `[{"role":"assistant","tool_calls":[{"function":{"name":"X","arguments":"{bad"}}],"extension":{"n":900719925474099312345,"s":"\ud800","\ud801":"unchanged","\ufffd":"different"}},{"role":"tool","content":"keep","tool_call_id":"call_msg0_tc0_X"}]`
	fields["messages"] = json.RawMessage(in)
	if err := applyGenericChatToolCorrelation(fields); err != nil || !bytes.Contains(fields["messages"], []byte(`"\ud801":"unchanged","\ufffd":"different"`)) || !bytes.Contains(fields["messages"], []byte("900719925474099312345")) {
		t.Fatalf("lost opaque value: %s %v", fields["messages"], err)
	}
}

// The payload and outbound body are both below maxRequest, although the
// intermediate pretty-printed JSON from the former implementation was not.
func TestPrepareGenericLargeCompactToolArguments(t *testing.T) {
	const sampleCount = 54000
	prefix := `{"model":"fixture","stream":false,"messages":[{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"f","arguments":`
	suffix := `}}]},{"role":"user","content":"after"}]}`
	input := prefix + `{"samples":[` + strings.Repeat("1, ", sampleCount-1) + "1]}" + suffix
	if len(input) >= maxRequest {
		t.Fatal("fixture must fit the ingress limit")
	}
	a, err := New("https://127.0.0.1/v1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Prepare("fixture", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.body) >= maxRequest {
		t.Fatal("prepared body must fit the outbound limit")
	}
	var got struct {
		Messages []struct {
			Role       string
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string
				Function struct{ Arguments string }
			} `json:"tool_calls"`
		}
	}
	want := `{"samples":[` + strings.Repeat("1,", sampleCount-1) + "1]}"
	if err := json.Unmarshal(p.body, &got); err != nil || len(got.Messages) != 3 ||
		len(got.Messages[0].ToolCalls) != 1 || got.Messages[0].ToolCalls[0].Function.Arguments != want ||
		got.Messages[1].Role != "tool" || got.Messages[1].ToolCallID != "call_msg0_tc0_f" ||
		got.Messages[0].ToolCalls[0].ID != got.Messages[1].ToolCallID {
		t.Fatalf("large history lost its exact compact arguments or correlation: %v", err)
	}

	// Compact JSON alone fits, but encoding quotes inside a JSON string
	// makes the final outbound representation too large.
	large := "[" + strings.Repeat(`{"a":"b"},`, 20000) + `{"a":"b"}]`
	if len(prefix)+len(large)+len(suffix) >= maxRequest {
		t.Fatal("oversize outbound fixture must fit the ingress limit")
	}
	if _, err := a.Prepare("fixture", []byte(prefix+large+suffix)); !errors.Is(err, ErrRequest) {
		t.Fatalf("oversize outbound body should still be rejected: %v", err)
	}
}

func TestGenericCompactToolArgumentsECMAScript(t *testing.T) {
	// Exercise numeric-index sorting, large-number rounding, UTF-16 pairs,
	// literal U+FFFD, U+2028, U+2029, and HTML/control string escaping.
	input := json.RawMessage(`{"02":9007199254740993,"4294967295":1e21,"2":1e-6,"1":-0,"z":["\ud83d\ude00","�","\u2028","\u2029","<>&","\b\r\t\u0000"]}`)
	p := schemaPrinter{compact: true}
	if !schemaScalarEscapes(input) || !p.value(input, 0) {
		t.Fatal("valid ECMAScript values refused")
	}
	want := `{"1":0,"2":0.000001,"02":9007199254740992,"4294967295":1e+21,"z":["😀","�","` + "\u2028" + `","` + "\u2029" + `","<>&","\b\r\t\u0000"]}`
	if string(p.out) != want {
		t.Fatalf("compact output %q, want %q", p.out, want)
	}
}
