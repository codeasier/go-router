package openaichat

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// applyGenericChatSchemaFallback is only for the generic openai-compatible
// Chat executor, after developer-role normalization. It builds both edits
// before publishing either. A registered native provider must not call it.
func applyGenericChatSchemaFallback(fields map[string]json.RawMessage) error {
	raw := fields["response_format"]
	if len(raw) > maxRequest {
		return ErrRequest
	}
	rf := schemaObject(raw)
	var typ string
	if json.Unmarshal(rf["type"], &typ) != nil || typ != "json_schema" {
		return nil
	}
	schema := schemaObject(rf["json_schema"])["schema"]
	if len(schema) == 0 {
		return nil
	}
	switch string(schema) {
	case "null", "false", `""`:
		return nil
	}
	if schema[0] == '-' || (schema[0] >= '0' && schema[0] <= '9') {
		n, err := strconv.ParseFloat(string(schema), 64)
		if err != nil {
			return ErrRequest
		}
		if n == 0 {
			return nil
		}
	}

	if !utf8.Valid(schema) || !schemaScalarEscapes(schema) {
		return ErrRequest
	}
	var p schemaPrinter
	if !p.value(schema, 0) {
		return ErrRequest
	}
	prompt := "You must respond with valid JSON that strictly follows this JSON schema:\n```json\n" +
		string(p.out) + "\n```\nRespond ONLY with the JSON object, no other text."
	raw = bytes.TrimSpace(fields["messages"])
	if len(raw) > maxRequest {
		return ErrRequest
	}
	var msgs []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' && json.Unmarshal(raw, &msgs) != nil {
		return ErrRequest
	}
	var found bool
	for i, msg := range msgs {
		role, start, end, err := schemaMessageContent(msg)
		if err != nil {
			return err
		}
		if found || role != "system" {
			continue
		}
		found = true
		if end == 0 {
			continue
		}
		content := msg[start:end]
		var replacement []byte
		switch content[0] {
		case '"':
			// Splice JSON string literals so existing text (even an escaped
			// UTF-16 code unit) is not decoded and silently replaced by Go.
			suffix, _ := json.Marshal("\n\n" + prompt)
			replacement = make([]byte, 0, len(content)+len(suffix)-2)
			replacement = append(replacement, content[:len(content)-1]...)
			replacement = append(replacement, suffix[1:]...)
		case '[':
			part, _ := json.Marshal(map[string]string{"type": "text", "text": "\n\n" + prompt})
			replacement = make([]byte, 0, len(content)+len(part)+1)
			replacement = append(replacement, content[:len(content)-1]...)
			if len(bytes.TrimSpace(content[1:len(content)-1])) != 0 {
				replacement = append(replacement, ',')
			}
			replacement = append(replacement, part...)
			replacement = append(replacement, ']')
		default:
			// The reference still rewrites response_format but does not
			// replace existing content which is neither text nor an array.
			continue
		}
		edited := make([]byte, 0, len(msg)-len(content)+len(replacement))
		edited = append(edited, msg[:start]...)
		edited = append(edited, replacement...)
		msgs[i] = append(edited, msg[end:]...)
	}
	if !found {
		sys, _ := json.Marshal(map[string]string{"role": "system", "content": prompt})
		msgs = append([]json.RawMessage{sys}, msgs...)
	}
	encoded, err := json.Marshal(msgs)
	if err != nil || len(encoded) > maxRequest {
		return ErrRequest
	}
	fields["messages"], fields["response_format"] = encoded, json.RawMessage(`{"type":"json_object"}`)
	return nil
}

// Read only the outer message members and the raw content span. Decoding
// the whole message into a map would collapse a lone UTF-16 surrogate key
// into U+FFFD and could drop a distinct member. Nested values stay opaque.
func schemaMessageContent(msg json.RawMessage) (role string, start, end int, err error) {
	d := json.NewDecoder(bytes.NewReader(msg))
	if token, e := d.Token(); e != nil || token != json.Delim('{') {
		return "", 0, 0, ErrRequest
	}
	seen := make(map[string]struct{})
	for d.More() {
		keyStart := d.InputOffset() // may include the member separator or whitespace
		token, e := d.Token()
		key, ok := token.(string)
		if e != nil || !ok || !schemaScalarEscapes(msg[keyStart:d.InputOffset()]) {
			return "", 0, 0, ErrRequest
		}
		if _, duplicate := seen[key]; duplicate {
			return "", 0, 0, ErrRequest
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return "", 0, 0, ErrRequest
		}
		switch key {
		case "role":
			if value[0] == '"' && json.Unmarshal(value, &role) != nil {
				return "", 0, 0, ErrRequest
			}
		case "content":
			end = int(d.InputOffset())
			start = end - len(value)
		}
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') {
		return "", 0, 0, ErrRequest
	}
	return role, start, end, nil
}

// RawMessage is from the bounded JSON request decoder, not arbitrary input.
func schemaObject(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// schemaPrinter mirrors JSON.stringify: indented for schema prompts, compact
// for tool arguments. Go maps alone lose non-index property insertion order.
// The byte limit applies directly to the selected output form.
type schemaPrinter struct {
	out     []byte
	compact bool
}

const maxSchemaDepth = 64

func (p *schemaPrinter) add(s string) bool {
	if len(s) > maxRequest-len(p.out) {
		return false
	}
	p.out = append(p.out, s...)
	return true
}
func (p *schemaPrinter) newline(level int) bool {
	return p.compact || (p.add("\n") && p.add(strings.Repeat("  ", level)))
}
func (p *schemaPrinter) quoted(s string) bool {
	// JS JSON strings don't escape HTML, slash, DEL, U+2028 or U+2029.
	const hex = "0123456789abcdef"
	if !p.add(`"`) {
		return false
	}
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		var escaped string
		switch c {
		case '"':
			escaped = `\"`
		case '\\':
			escaped = `\\`
		case '\b':
			escaped = `\b`
		case '\f':
			escaped = `\f`
		case '\n':
			escaped = `\n`
		case '\r':
			escaped = `\r`
		case '\t':
			escaped = `\t`
		default:
			escaped = "\\u00" + string([]byte{hex[c>>4], hex[c&15]})
		}
		if !p.add(s[start:i]) || !p.add(escaped) {
			return false
		}
		start = i + 1
	}
	return p.add(s[start:]) && p.add(`"`)
}
func (p *schemaPrinter) value(raw json.RawMessage, level int) bool {
	if level > maxSchemaDepth || len(raw) == 0 {
		return false
	}
	switch raw[0] {
	case '{', '[':
		object := raw[0] == '{'
		d := json.NewDecoder(bytes.NewReader(raw))
		if _, err := d.Token(); err != nil {
			return false
		}
		type entry struct {
			key   string
			index uint64
			val   json.RawMessage
		}
		var items []entry
		seen := make(map[string]bool)
		for d.More() {
			var e entry
			if object {
				t, err := d.Token()
				k, ok := t.(string)
				if err != nil || !ok || seen[k] {
					return false
				}
				seen[k] = true
				e.key = k
				e.index = math.MaxUint32
				if n, err := strconv.ParseUint(k, 10, 32); err == nil && strconv.FormatUint(n, 10) == k {
					e.index = n
				}
			}
			if d.Decode(&e.val) != nil {
				return false
			}
			items = append(items, e)
		}
		if _, err := d.Token(); err != nil {
			return false
		}
		open, close := "[", "]"
		if object {
			open, close = "{", "}"
			// ECMA array-index keys precede other keys (2^32-1 is not an index).
			sort.SliceStable(items, func(i, j int) bool { return items[i].index < items[j].index })
		}
		if !p.add(open) {
			return false
		}
		colon := ": "
		if p.compact {
			colon = ":"
		}
		for i, e := range items {
			if i > 0 && !p.add(",") {
				return false
			}
			if !p.newline(level+1) || (object && (!p.quoted(e.key) || !p.add(colon))) || !p.value(e.val, level+1) {
				return false
			}
		}
		return (len(items) == 0 || p.newline(level)) && p.add(close)
	case '"':
		var s string
		return json.Unmarshal(raw, &s) == nil && p.quoted(s)
	case 't', 'f', 'n':
		return p.add(string(raw))
	default:
		n, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return false
		}
		if n == 0 {
			return p.add("0")
		} // JS serializes negative zero as zero
		// encoding/json's float64 encoding implements ES6 number formatting.
		b, err := json.Marshal(n)
		return err == nil && p.add(string(b))
	}
}

// Go JSON decoding replaces lone surrogate escapes with U+FFFD, whereas JS
// retains them. Reject those schemas rather than put a different schema in
// the prompt. Valid pairs, literal U+FFFD and escaped backslashes are allowed.
func schemaScalarEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n < 0xd800 || n > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		lo, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if e != nil || lo < 0xdc00 || lo > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
