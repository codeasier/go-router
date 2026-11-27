package openaichat

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func schemaTestFields(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	f, ok := requestFields([]byte(body))
	if !ok {
		t.Fatal("bad test fixture")
	}
	return f
}

func testSchemaBody(schema, messages string) string {
	return `{"response_format":{"type":"json_schema","json_schema":{"strict":true,"name":"example","schema":` + schema + `}},"messages":` + messages + `,"vendor":{"unchanged":true}}`
}

func referenceSchemaPrompt(schema string) string {
	return "You must respond with valid JSON that strictly follows this JSON schema:\n```json\n" + schema + "\n```\nRespond ONLY with the JSON object, no other text."
}

func TestGenericSchemaFallbackEditsFirstSystemOnly(t *testing.T) {
	msgs := `[{"role":"user","content":"hi"},{"role":"system","content":"rules","id":9007199254740993},{"role":"system","content":"stay"}]`
	f := schemaTestFields(t, testSchemaBody(`{"type":"object","properties":{"z":{"type":"integer"},"a":{"const":1}}}`, msgs))
	if err := applyGenericChatSchemaFallback(f); err != nil {
		t.Fatal(err)
	}
	var got []map[string]json.RawMessage
	if err := json.Unmarshal(f["messages"], &got); err != nil || len(got) != 3 {
		t.Fatal("messages", err)
	}
	var prompt string
	want := "rules\n\n" + referenceSchemaPrompt(`{
  "type": "object",
  "properties": {
    "z": {
      "type": "integer"
    },
    "a": {
      "const": 1
    }
  }
}`)
	if json.Unmarshal(got[1]["content"], &prompt) != nil || prompt != want {
		t.Fatalf("prompt %q", prompt)
	}
	if string(got[0]["content"]) != `"hi"` || string(got[1]["id"]) != "9007199254740993" || string(got[2]["content"]) != `"stay"` ||
		string(f["response_format"]) != `{"type":"json_object"}` || string(f["vendor"]) != `{"unchanged":true}` {
		t.Fatal("changed unrelated fields or retained schema wrapper")
	}
}

func TestGenericSchemaFallbackMessageForms(t *testing.T) {
	p := referenceSchemaPrompt("{}")
	part := map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA=="}}
	for _, tc := range []struct {
		name, msgs string
		want       []any
	}{
		{"multimodal", `[{"role":"system","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]`, []any{map[string]any{"role": "system", "content": []any{part, map[string]any{"type": "text", "text": "\n\n" + p}}}}},
		{"empty string", `[{"role":"system","content":""}]`, []any{map[string]any{"role": "system", "content": "\n\n" + p}}},
		{"no exact system", `[{"role":"System","content":"keep"}]`, []any{map[string]any{"role": "system", "content": p}, map[string]any{"role": "System", "content": "keep"}}},
		{"no array", `null`, []any{map[string]any{"role": "system", "content": p}}},
		{"existing null", `[{"role":"system","content":null}]`, []any{map[string]any{"role": "system", "content": nil}}},
		{"existing missing", `[{"role":"system"}]`, []any{map[string]any{"role": "system"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := schemaTestFields(t, testSchemaBody("{}", tc.msgs))
			if err := applyGenericChatSchemaFallback(f); err != nil {
				t.Fatal(err)
			}
			var got []any
			if err := json.Unmarshal(f["messages"], &got); err != nil || !reflect.DeepEqual(got, tc.want) || string(f["response_format"]) != `{"type":"json_object"}` {
				t.Fatalf("got %#v; want %#v", got, tc.want)
			}
		})
	}
}

func TestGenericSchemaFallbackOnlyForTruthySchema(t *testing.T) {
	for _, schema := range []string{"false", "null", `""`, "-0", "0e10", "0.00"} {
		f := schemaTestFields(t, testSchemaBody(schema, ` [1] `))
		before, _ := json.Marshal(f)
		if err := applyGenericChatSchemaFallback(f); err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(f)
		if !bytes.Equal(before, after) {
			t.Fatalf("changed falsy schema %s", schema)
		}
	}
	for _, rf := range []string{`{"type":"json_object"}`, `null`, `42`, `{"type":"json_schema","JSON_SCHEMA":{"schema":true}}`, `{"type":"Json_Schema","json_schema":{"schema":{}}}`} {
		f := schemaTestFields(t, `{"response_format":`+rf+`}`)
		if err := applyGenericChatSchemaFallback(f); err != nil || string(f["response_format"]) != rf || len(f) != 1 {
			t.Fatalf("changed unrelated %s: %v", rf, err)
		}
	}
}

func TestGenericSchemaFallbackJSStringification(t *testing.T) {
	// Numeric index keys precede insertion-order string keys. Percent, slash,
	// HTML and line separators are literal; numeric spelling follows JS Number.
	in := `{"z":0,"10":"ten","4294967295":1e21,"02":"zero two","2":-0,"4294967294":1e-7,"a":[1e-6,1e20,9007199254740993,5e-324],"s":"<>&/\u2028\u2029\t\u0007\ud83d\ude0a\\udc00\ufffd"}`
	want := "{\n  \"2\": 0,\n  \"10\": \"ten\",\n  \"4294967294\": 1e-7,\n  \"z\": 0,\n  \"4294967295\": 1e+21,\n  \"02\": \"zero two\",\n  \"a\": [\n    0.000001,\n    100000000000000000000,\n    9007199254740992,\n    5e-324\n  ],\n  \"s\": \"<>&/\u2028\u2029\\t\\u0007😊\\\\udc00�\"\n}"
	f := schemaTestFields(t, testSchemaBody(in, "[]"))
	if err := applyGenericChatSchemaFallback(f); err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]string
	if json.Unmarshal(f["messages"], &msgs) != nil || len(msgs) != 1 || msgs[0]["content"] != referenceSchemaPrompt(want) {
		t.Fatalf("got %#v", msgs)
	}
}

func TestGenericSchemaFallbackRejectsUnsupportedBeforePublication(t *testing.T) {
	for _, tc := range []struct{ name, schema, messages string }{
		{"lone high surrogate", `{"const":"\ud800"}`, "[]"},
		{"lone low surrogate", `{"\udc00":true}`, "[]"},
		{"overflow", `{"minimum":1e999}`, "[]"},
		{"ambiguous schema property", `{"type":"object","t\u0079pe":"array"}`, "[]"},
		{"container depth", strings.Repeat("[", maxSchemaDepth+1) + "true" + strings.Repeat("]", maxSchemaDepth+1), "[]"},
		{"pretty expansion", strings.Repeat("[", 48) + strings.Repeat("0,", 4000) + "0" + strings.Repeat("]", 48), "[]"},
		{"message expansion", "{}", `[{"role":"system","content":"` + strings.Repeat("x", maxRequest-130) + `"}]`},
		{"invalid message object", "{}", `[{"role":"system","content":"unchanged"},2]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := schemaTestFields(t, testSchemaBody(tc.schema, tc.messages))
			before, _ := json.Marshal(f)
			err := applyGenericChatSchemaFallback(f)
			after, _ := json.Marshal(f)
			if err != ErrRequest || !bytes.Equal(before, after) {
				t.Fatalf("expected atomic rejection; err=%v", err)
			}
		})
	}
}

func TestGenericSchemaFallbackMessageMemberNames(t *testing.T) {
	t.Run("preserve scalar members and raw nested values", func(t *testing.T) {
		msg := `[{"role":"system","content":"text","\ufffd":"replacement","\ud83d\ude03":"pair","\\ud800":"literal escape","vendor":{"\ud800":"opaque","\ufffd":"distinct"}}]`
		f := schemaTestFields(t, testSchemaBody("{}", msg))
		if err := applyGenericChatSchemaFallback(f); err != nil {
			t.Fatal(err)
		}
		for _, part := range []string{`"\ufffd":"replacement"`, `"\ud83d\ude03":"pair"`, `"\\ud800":"literal escape"`, `"vendor":{"\ud800":"opaque","\ufffd":"distinct"}`} {
			if !bytes.Contains(f["messages"], []byte(part)) {
				t.Fatalf("lost raw member %s: %s", part, f["messages"])
			}
		}
		var got []map[string]json.RawMessage
		if err := json.Unmarshal(f["messages"], &got); err != nil || len(got) != 1 {
			t.Fatalf("invalid messages: %v", err)
		}
		var prompt string
		if json.Unmarshal(got[0]["content"], &prompt) != nil || prompt != "text\n\n"+referenceSchemaPrompt("{}") ||
			string(got[0]["�"]) != `"replacement"` || string(got[0]["😃"]) != `"pair"` {
			t.Fatalf("wrong fields or transformed content: %s", f["messages"])
		}
	})
	for _, tc := range []struct{ name, messages string }{
		{"surrogate and Unicode replacement collision", `[{"role":"system","content":"text","\ud800":"lone","\ufffd":"real"}]`},
		{"single unpaired low surrogate", `[{"role":"system","content":[],"\udfff":"lone"}]`},
		{"duplicate content with distinct spelling", `[{"role":"system","content":"first","cont\u0065nt":"second"}]`},
		{"duplicate Unicode name", `[{"role":"system","content":"text","\ufffd":"first","�":"second"}]`},
		{"later invalid message is still atomic", `[{"role":"system","content":"text"},{"role":"user","content":"hi","\ud800":null}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := schemaTestFields(t, testSchemaBody("{}", tc.messages))
			before, _ := json.Marshal(f)
			if err := applyGenericChatSchemaFallback(f); err != ErrRequest {
				t.Fatalf("ambiguous member not rejected: %v; messages=%s", err, f["messages"])
			}
			after, _ := json.Marshal(f)
			if !bytes.Equal(before, after) {
				t.Fatal("fallback committed only part of an invalid request")
			}
		})
	}
}
