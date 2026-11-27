package openaichat

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

const (
	maxCorrelationMessages = 2048 // input and expanded history
	maxCorrelationCalls    = 256  // total calls per request, not per message
	maxCorrelationDepth    = 64
)

// applyGenericChatToolCorrelation repairs ordinary OpenAI Chat history only.
// Call after generic role normalization, before schema fallback (which may
// prepend a message). The indices in generated IDs refer to the original
// history. No tool is invoked and no assistant call is created. No edits are
// published on error. Non-array histories and Claude or mixed-format tool
// blocks are outside this leaf and are passed through without partial repair.
func applyGenericChatToolCorrelation(fields map[string]json.RawMessage) error {
	raw := bytes.TrimSpace(fields["messages"])
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	if len(raw) > maxRequest {
		return ErrRequest
	}
	candidate, bounded := correlationScope(raw)
	if !candidate {
		return nil
	}
	if !bounded {
		return ErrRequest
	}
	msgs, err := correlationArray(raw, maxCorrelationMessages)
	if err != nil {
		return err
	}

	type turn struct {
		msg            map[string]json.RawMessage
		role, resultID string
		calls          []string
	}
	turns := make([]turn, len(msgs))
	// Identify the source format before changing anything. A Claude result
	// block can satisfy a call in the reference; it must not acquire a second
	// synthetic OpenAI result just because this adapter didn't recognize it.
	for i, msg := range msgs {
		if msg[0] != '{' {
			return nil
		}
		m, err := correlationObject(msg)
		if err != nil {
			return err
		}
		var role string
		_ = json.Unmarshal(m["role"], &role)
		turns[i] = turn{msg: m, role: role}
		if c := m["content"]; len(c) > 0 && c[0] == '[' {
			blocks, err := correlationArray(c, maxCorrelationMessages)
			if err != nil {
				return err
			}
			for _, block := range blocks {
				if block[0] != '{' {
					continue
				}
				b, err := correlationObject(block)
				if err != nil {
					return err
				}
				var typ string
				_ = json.Unmarshal(b["type"], &typ)
				if typ == "tool_use" || typ == "tool_result" {
					return nil
				}
			}
		}
	}

	var changed bool
	total := 0
	for i := range turns {
		t := &turns[i]
		m := t.msg
		edited := false
		switch t.role {
		case "assistant":
			c := m["tool_calls"]
			if len(c) == 0 || c[0] != '[' {
				continue
			}
			calls, err := correlationArray(c, maxCorrelationCalls-total)
			if err != nil {
				return err
			}
			total += len(calls)
			seen := make(map[string]bool, len(calls))
			for j, call := range calls {
				id, replacement, err := normalizeHistoryCall(call, i, j)
				// Sanitization or generation may collapse two IDs into one.
				// Do not attach a real result to an ambiguous parallel call.
				if err != nil || seen[id] {
					return ErrRequest
				}
				seen[id] = true
				t.calls = append(t.calls, id)
				if replacement != nil {
					calls[j], edited = replacement, true
				}
			}
			if edited {
				m["tool_calls"], err = json.Marshal(calls)
				if err != nil {
					return ErrRequest
				}
			}
		case "tool":
			id, ok := correlationString(m["tool_call_id"])
			if !ok {
				return ErrRequest
			}
			t.resultID = id
			if id != "" {
				t.resultID = cleanCorrelationID(id)
				if t.resultID == "" {
					t.resultID = generatedCorrelationID(i, 0, "")
				}
				if t.resultID != id {
					m["tool_call_id"], _ = json.Marshal(t.resultID)
					edited = true
				}
			}
		}
		if edited {
			msgs[i], err = json.Marshal(m)
			if err != nil || len(msgs[i]) > maxRequest {
				return ErrRequest
			}
			changed = true
		}
	}
	// Only the *next original* message is relevant. If it matches ANY one
	// of the parallel IDs, the reference inserts NOTHING. It does not try
	// to complete a partial batch or append results after a trailing call.
	out := make([]json.RawMessage, 0, len(msgs))
	for i, m := range msgs {
		out = append(out, m)
		ids := turns[i].calls
		if len(ids) == 0 || i+1 == len(turns) {
			continue
		}
		next := turns[i+1]
		matched := false
		for _, id := range ids {
			if next.role == "tool" && id == next.resultID {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if len(out)+len(msgs)-i-1+len(ids) > maxCorrelationMessages {
			return ErrRequest
		}
		for _, id := range ids { // id has been restricted to ASCII identifier characters
			out = append(out, json.RawMessage(`{"role":"tool","tool_call_id":"`+id+`","content":""}`))
		}
		changed = true
	}
	if changed {
		b, err := json.Marshal(out)
		if err != nil || len(b) > maxRequest {
			return ErrRequest
		}
		fields["messages"] = b
	}
	return nil
}

func normalizeHistoryCall(raw json.RawMessage, i, j int) (string, json.RawMessage, error) {
	call, err := correlationObject(raw)
	if err != nil {
		return "", nil, err
	}
	id, ok := correlationString(call["id"])
	if !ok {
		return "", nil, ErrRequest
	}
	f := call["function"]
	var fn map[string]json.RawMessage
	if len(f) != 0 && string(f) != "null" {
		fn, err = correlationObject(f)
		if err != nil {
			return "", nil, err
		}
	}
	fixed := cleanCorrelationID(id)
	if fixed == "" {
		name, ok := correlationString(fn["name"])
		if !ok {
			return "", nil, ErrRequest
		}
		fixed = generatedCorrelationID(i, j, name)
	}
	edited := fixed != id
	if edited {
		call["id"], _ = json.Marshal(fixed)
	}
	typ, ok := correlationString(call["type"])
	if !ok {
		return "", nil, ErrRequest
	}
	if typ == "" {
		call["type"] = json.RawMessage(`"function"`)
		edited = true
	}
	// JS stringifies truthy non-string arguments; JSON string arguments
	// (including invalid inner JSON) are syntactic history, not executable.
	if a := fn["arguments"]; len(a) > 0 && a[0] != '"' && string(a) != "null" && string(a) != "false" {
		// Bound the compact representation, not an intermediate indented form.
		p := schemaPrinter{compact: true}
		if !schemaScalarEscapes(a) || !p.value(a, 0) {
			return "", nil, ErrRequest
		}
		if string(p.out) != "0" { // covers 0, -0 and other zero numerals
			fn["arguments"], _ = json.Marshal(string(p.out))
			call["function"], err = json.Marshal(fn)
			if err != nil {
				return "", nil, ErrRequest
			}
			edited = true
		}
	}
	if !edited {
		return fixed, nil, nil
	}
	b, err := json.Marshal(call)
	if err != nil || len(b) > maxRequest {
		return "", nil, ErrRequest
	}
	return fixed, b, nil
}

func cleanCorrelationID(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return -1
	}, s)
}

func generatedCorrelationID(i, j int, name string) string {
	id := "call_msg" + strconv.Itoa(i) + "_tc" + strconv.Itoa(j)
	if name != "" {
		id += "_" + cleanCorrelationID(name)
	}
	return id
}

func correlationString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// Reject duplicate or lossy property names before interpreting/re-encoding
// objects; opaque nested content remains raw, preserving its numbers and keys.
func correlationObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if tok, e := d.Token(); e != nil || tok != json.Delim('{') {
		return nil, ErrRequest
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		start := d.InputOffset()
		tok, e := d.Token()
		key, ok := tok.(string)
		if e != nil || !ok || fields[key] != nil || !schemaScalarEscapes(raw[start:d.InputOffset()]) {
			return nil, ErrRequest
		}
		var val json.RawMessage
		if d.Decode(&val) != nil {
			return nil, ErrRequest
		}
		fields[key] = val
	}
	if tok, e := d.Token(); e != nil || tok != json.Delim('}') {
		return nil, ErrRequest
	}
	return fields, nil
}

func correlationArray(raw json.RawMessage, limit int) ([]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if tok, e := d.Token(); e != nil || tok != json.Delim('[') {
		return nil, ErrRequest
	}
	var vals []json.RawMessage
	for d.More() {
		if len(vals) >= limit {
			return nil, ErrRequest
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrRequest
		}
		vals = append(vals, value)
	}
	if tok, e := d.Token(); e != nil || tok != json.Delim(']') {
		return nil, ErrRequest
	}
	return vals, nil
}

// The request decoder already established valid JSON. Scan only for direct
// message keys (including escaped spellings), not strings inside e.g. content
// or extensions. The narrower limits and object parsing below must not apply
// to ordinary tool-free histories. Count depth without recursive allocation.
func correlationScope(raw []byte) (candidate, bounded bool) {
	depth := 0
	bounded = true
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '[', '{':
			depth++
			if depth > maxCorrelationDepth {
				bounded = false
			}
		case ']', '}':
			depth--
		case '"':
			start := i
			for i++; i < len(raw) && raw[i] != '"'; i++ {
				if raw[i] == '\\' {
					i++
				}
			}
			if candidate || depth != 2 || i >= len(raw) || i-start < 10 || i-start > 1+6*len("tool_call_id") {
				continue
			}
			j := i + 1
			for j < len(raw) && (raw[j] == ' ' || raw[j] == '\n' || raw[j] == '\r' || raw[j] == '\t') {
				j++
			}
			if j < len(raw) && raw[j] == ':' {
				var key string
				if json.Unmarshal(raw[start:i+1], &key) == nil && (key == "tool_calls" || key == "tool_call_id") {
					candidate = true
				}
			}
		}
	}
	return candidate, bounded
}
