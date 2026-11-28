package openaichat

import (
	"encoding/binary"
	"encoding/json"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// The exact-name DeepSeek rule in pinned 9router's toolDeduper.js drops
// only later tools with the same name. Retain each surviving raw object,
// including its nonstandard fields. Do not touch tool history or definitions
// for any other connection or native model family.
func dedupeVolceToolNames(fields map[string]json.RawMessage) error {
	raw := fields["tools"]
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return ErrRequest
	}
	seen := make(map[any]struct{})
	kept := tools[:0]
	for _, tool := range tools {
		members := schemaObject(tool)
		name, truthy := volceToolKey(members["name"])
		if !truthy {
			name, _ = volceToolKey(schemaObject(members["function"])["name"])
		}
		if name != nil {
			if _, duplicate := seen[name]; duplicate {
				continue
			}
			seen[name] = struct{}{}
		}
		kept = append(kept, tool)
	}
	if len(kept) == len(tools) {
		return nil
	}
	value, err := json.Marshal(kept)
	if err != nil {
		return ErrRequest
	}
	fields["tools"] = value
	return nil
}

// The reference uses name || function.name and a JavaScript Set. Unnamed
// tools and object-valued names are kept. Distinguish the primitive key types.
func volceToolKey(raw json.RawMessage) (key any, truthy bool) {
	if len(raw) == 0 {
		return nil, false
	}
	switch raw[0] {
	case 'n', 'f':
		return nil, false // null, false
	case 't':
		return true, true
	case '[', '{':
		return nil, true // unique by object identity in parsed JSON
	case '"':
		if len(raw) == 2 {
			return nil, false
		}
		return volceToolStringKey(raw), true
	default:
		n, _ := strconv.ParseFloat(string(raw), 64) // valid JSON number; overflow becomes ±Inf
		if n == 0 {
			return nil, false
		}
		return n, true
	}
}

// JSON string names use exact UTF-16 equality in the pinned implementation.
// Go's decoder would replace distinct lone surrogate escapes by U+FFFD.
func volceToolStringKey(raw []byte) string {
	var key []byte
	for i := 1; i < len(raw)-1; {
		if raw[i] == '\\' {
			switch raw[i+1] {
			case 'u':
				n, _ := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
				key = binary.BigEndian.AppendUint16(key, uint16(n))
				i += 6
				continue
			case 'b':
				key = binary.BigEndian.AppendUint16(key, '\b')
			case 'f':
				key = binary.BigEndian.AppendUint16(key, '\f')
			case 'n':
				key = binary.BigEndian.AppendUint16(key, '\n')
			case 'r':
				key = binary.BigEndian.AppendUint16(key, '\r')
			case 't':
				key = binary.BigEndian.AppendUint16(key, '\t')
			default:
				key = binary.BigEndian.AppendUint16(key, uint16(raw[i+1]))
			}
			i += 2
			continue
		}
		r, size := utf8.DecodeRune(raw[i:])
		i += size
		if r > 0xffff {
			hi, lo := utf16.EncodeRune(r)
			key = binary.BigEndian.AppendUint16(key, uint16(hi))
			key = binary.BigEndian.AppendUint16(key, uint16(lo))
		} else {
			key = binary.BigEndian.AppendUint16(key, uint16(r))
		}
	}
	return string(key)
}
