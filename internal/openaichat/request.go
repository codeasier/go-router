// Package openaichat implements a transport-free OpenAI Chat protocol adapter.
// The caller's dispatcher owns endpoint approval, outbound policy, durable
// dispatch intent, the single transport call and all retry decisions.
package openaichat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxRequest = 256 << 10

var (
	ErrEndpoint   = errors.New("invalid OpenAI-compatible base URL")
	ErrRequest    = errors.New("invalid OpenAI-compatible request")
	ErrCredential = errors.New("invalid OpenAI-compatible bearer credential")

	dnsLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	pathPart = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	bearer   = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
)

// Adapter stores one immutable operator-approved HTTPS endpoint, never a
// client-supplied URL. Syntax validation here is NOT a network allowlist.
type Adapter struct{ endpoint string }

// New accepts a base URL (including a version path when required). It never
// rewrites a host or infers a version. Request paths are not configurable.
func New(baseURL string) (*Adapter, error) {
	if len(baseURL) > 2048 || !strings.HasPrefix(baseURL, "https://") || strings.ContainsAny(baseURL, "?#") {
		return nil, ErrEndpoint
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" || u.RawPath != "" || u.RawQuery != "" {
		return nil, ErrEndpoint
	}
	host, port := u.Hostname(), u.Port()
	if !validHost(host) || strings.HasSuffix(u.Host, ":") ||
		(strings.HasPrefix(u.Host, "[") && !strings.Contains(host, ":")) {
		return nil, ErrEndpoint
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, ErrEndpoint
		}
	}
	path := strings.TrimSuffix(u.Path, "/")
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if path != "" && (!pathPart.MatchString(part) || part == "." || part == "..") {
			return nil, ErrEndpoint
		}
	}
	if strings.HasPrefix(u.Path, "//") || strings.HasSuffix(path, "/chat/completions") {
		return nil, ErrEndpoint
	}
	u.Path = path + "/chat/completions"
	return &Adapter{endpoint: u.String()}, nil
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Zone() == ""
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// Prepared is a fixed request body and its destination. It contains no bearer
// or caller-supplied headers. Do not log its body or the HTTP request.
type Prepared struct {
	endpoint          string
	body              []byte
	stream            bool
	allowImplicitJSON bool
}

func (p *Prepared) String() string   { return "OpenAI-compatible prepared request (redacted)" }
func (p *Prepared) GoString() string { return p.String() }

// ParsedRequest holds validated structural data; no model mapping or wire
// negotiation has happened yet. Its contents are not suitable for logging.
type ParsedRequest struct {
	fields        map[string]json.RawMessage
	stream        *bool
	model         string
	normalization genericChatNormalization
}

func (p *ParsedRequest) String() string   { return "OpenAI-compatible parsed request (redacted)" }
func (p *ParsedRequest) GoString() string { return p.String() }

// Model is the client's model name for a catalog lookup, not an upstream
// destination or authorization.
func (p *ParsedRequest) Model() string { return p.model }

// ParseClientRequest rejects structurally invalid requests before catalog
// lookup, without serializing a synthetic outbound request.
func ParseClientRequest(source []byte) (*ParsedRequest, error) {
	p, err := parseRequest(source)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(p.fields["model"], &p.model) != nil || p.model == "" {
		return nil, ErrRequest
	}
	return p, nil
}

func parseRequest(source []byte) (*ParsedRequest, error) {
	if len(source) > maxRequest || !utf8.Valid(source) {
		return nil, ErrRequest
	}
	fields, ok := requestFields(source)
	if !ok {
		return nil, ErrRequest
	}
	var stream *bool
	if fields["stream"] != nil && (json.Unmarshal(fields["stream"], &stream) != nil || stream == nil) {
		return nil, ErrRequest
	}
	n, err := inspectGenericChat(fields)
	if err != nil {
		return nil, err
	}
	return &ParsedRequest{fields: fields, stream: stream, normalization: n}, nil
}

// Prepare routes the default generic Chat request for a catalog-selected model.
func (a *Adapter) Prepare(model string, source []byte) (*Prepared, error) {
	return a.PrepareWithAccept(model, source, nil)
}

// PrepareWithAccept is for callers that already selected the upstream model
// and do not need to read a client model from the request first.
func (a *Adapter) PrepareWithAccept(model string, source []byte, accept []string) (*Prepared, error) {
	p, err := parseRequest(source)
	if err != nil {
		return nil, err
	}
	return a.PrepareParsed(model, p, accept)
}

// PrepareParsed negotiates ordinary generic Chat like 9router chatCore.
// The inbound Accept is used only to select a mode, never forwarded. Matching
// is literal substring matching as in the pinned baseline, not RFC q ranking.
func (a *Adapter) PrepareParsed(model string, p *ParsedRequest, accept []string) (*Prepared, error) {
	if a == nil || a.endpoint == "" || p == nil || p.fields == nil ||
		model == "" || len(model) > 512 || strings.TrimSpace(model) != model ||
		!utf8.ValidString(model) || strings.ContainsFunc(model, unicode.IsControl) {
		return nil, ErrRequest
	}
	stream := p.stream
	// A missing stream field is distinct from false and stays absent on wire.
	// Multiple header lines are equivalent to one comma-separated Fetch header.
	var acceptJSON, acceptSSE bool
	for _, value := range accept {
		acceptJSON = acceptJSON || strings.Contains(value, "application/json")
		acceptSSE = acceptSSE || strings.Contains(value, "text/event-stream")
	}
	upstreamStreaming := !acceptJSON || acceptSSE
	if stream != nil {
		upstreamStreaming = *stream
	}
	fields := maps.Clone(p.fields)
	p.normalization.apply(fields, upstreamStreaming)
	fields["model"], _ = json.Marshal(model)
	body, err := json.Marshal(fields)
	if err != nil || len(body) > maxRequest {
		return nil, ErrRequest
	}
	return &Prepared{endpoint: a.endpoint, body: body, stream: upstreamStreaming,
		allowImplicitJSON: stream == nil && upstreamStreaming && !acceptSSE}, nil
}

func requestFields(raw []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil ||
			(key != "model" && strings.EqualFold(key, "model")) ||
			(key != "stream" && strings.EqualFold(key, "stream")) {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	_, err := d.Token()
	return fields, err == io.EOF
}

// BodyDigest matches the exact body the dispatcher may send after it commits
// intent. It never exposes the body itself.
func (p *Prepared) BodyDigest() [sha256.Size]byte { return sha256.Sum256(p.body) }

// NewRequest attaches only the credential belonging to the selected provider
// connection. The dispatcher must use RoundTrip once (no redirects; no
// request replay) after persisting intent. Request construction sends nothing.
func (p *Prepared) NewRequest(ctx context.Context, token string) (*http.Request, error) {
	if p == nil || p.endpoint == "" || ctx == nil {
		return nil, ErrRequest
	}
	if len(token) == 0 || len(token) > 4096 || !bearer.MatchString(token) {
		return nil, ErrCredential
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(p.body))
	if err != nil {
		return nil, ErrRequest
	}
	r.GetBody = nil
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	// Fetch sends */* on the wire when DefaultExecutor omits Accept. Go's
	// transport supplies no default; declare the observed wire value explicitly.
	r.Header.Set("Accept", "*/*")
	if p.stream {
		r.Header.Set("Accept", "text/event-stream")
	}
	return r, nil
}
