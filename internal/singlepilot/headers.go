package singlepilot

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	maxHeadersConfigBytes = 16 << 10
	maxCustomHeaderBytes  = 8 << 10
	maxCustomHeaders      = 16
	maxCustomHeaderName   = 128
	maxCustomHeaderValue  = 2048
)

var errCustomHeaders = errors.New("invalid generic Chat outbound header configuration")

// Parse one operator-authored object, in textual order. Like the pinned
// executor, a later differently-cased spelling replaces an earlier one.
// Exact duplicate JSON members and reserved headers fail the entire setup.
func parseCustomHeaders(src string) (map[string]string, error) {
	if len(src) == 0 || len(src) > maxHeadersConfigBytes {
		return nil, errCustomHeaders
	}
	dec := json.NewDecoder(strings.NewReader(src))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errCustomHeaders
	}
	headers := make(map[string]string)
	seen := make(map[string]struct{})
	bytes := 0
	for dec.More() {
		if len(seen) == maxCustomHeaders {
			return nil, errCustomHeaders
		}
		tok, err := dec.Token()
		name, ok := tok.(string)
		if err != nil || !ok || !validCustomHeaderName(name) {
			return nil, errCustomHeaders
		}
		if _, ok := seen[name]; ok {
			return nil, errCustomHeaders
		}
		seen[name] = struct{}{}
		// Token does not recurse into a non-string value.
		tok, err = dec.Token()
		value, ok := tok.(string)
		if err != nil || !ok || len(value) > maxCustomHeaderValue ||
			strings.Contains(value, "${") || !printableHeaderValue(value) {
			return nil, errCustomHeaders
		}
		value = strings.Trim(value, " ")
		if strings.EqualFold(name, "User-Agent") && value == "" {
			return nil, errCustomHeaders // Go would suppress it, not send an empty value
		}
		// Bound the whole configuration's contribution, including entries
		// superseded by a later case variant and line-level overhead.
		bytes += len(name) + len(value) + 4
		if bytes > maxCustomHeaderBytes {
			return nil, errCustomHeaders
		}
		headers[http.CanonicalHeaderKey(name)] = value
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errCustomHeaders
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errCustomHeaders
	}
	return headers, nil
}

func printableHeaderValue(value string) bool {
	for i := range len(value) {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func validCustomHeaderName(name string) bool {
	if name == "" || len(name) > maxCustomHeaderName {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	lower := strings.ToLower(name)
	switch lower {
	case "authorization", "x-authorization", "api-key", "x-api-key", "x-auth-token", "x-access-token",
		"accept", "host", "content-type", "content-length", "connection", "transfer-encoding", "te", "trailer", "upgrade",
		"accept-encoding", "content-encoding", "expect", "keep-alive", "http2-settings",
		"cookie", "set-cookie", "forwarded", "via", "x-real-ip", "x-http-method",
		"x-http-method-override", "x-method-override", "__proto__":
		return false
	}
	return !strings.HasPrefix(lower, "proxy-") && !strings.HasPrefix(lower, "x-forwarded-")
}
