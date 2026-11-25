// Package syntheticchat implements a deliberately unmounted, loopback-only
// gateway fixture. It is not a production HTTP adapter or a provider registry.
package syntheticchat

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/catalog"
)

const (
	chatPath         = "/v1/chat/completions"
	fixtureTransport = "synthetic-http"
	maxRequest       = 256 << 10
	maxReply         = 1 << 20
	fixtureTimeout   = 5 * time.Second
)

type IntentLog interface {
	RecordIntent(attemptlog.Intent) error
}

type Execution string

const (
	NotSent  Execution = "NOT_SENT"
	Executed Execution = "EXECUTED"
	Unknown  Execution = "UNKNOWN"
)

// Observation is in-process fixture evidence, not persistent settlement.
// Complete and Usage are independent; nil Usage means unknown, never zero.
type Observation struct {
	RequestID string
	AttemptID string
	Execution Execution
	Complete  bool
	Usage     *Usage
}

type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// Fixture holds trusted, preconfigured test dependencies. New rejects a
// non-loopback origin and only routes synthetic-http catalog metadata.
// Transport must send directly to the configured loopback origin, honor the
// context, and not rewrite destinations, proxy, follow redirects, or retry.
// Observe, if set, can run concurrently; it runs after response-body closure.
type Fixture struct {
	Origin    string
	Transport http.RoundTripper
	Registry  *catalog.Registry
	Journal   IntentLog
	Observe   func(Observation)
}

type handler struct {
	fixture Fixture
	target  string
}

// New returns only the fixture handler. Nothing mounts it in the daemon.
func New(f Fixture) (http.Handler, error) {
	u, err := url.Parse(f.Origin)
	if err != nil || !loopbackOrigin(u) || f.Transport == nil || f.Registry == nil || f.Journal == nil {
		return nil, errors.New("invalid synthetic chat fixture configuration")
	}
	for _, model := range f.Registry.List() {
		if model.Transport == fixtureTransport && (!journalCatalogID(model.ProviderID) || !journalCatalogID(model.ID)) {
			return nil, errors.New("synthetic catalog contains metadata unsupported by the dispatch journal")
		}
	}
	h := &handler{fixture: f, target: u.String() + chatPath}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+chatPath, h.chat)
	return mux, nil
}

// journalCatalogID checks the intersection with the intent journal's v1 ID
// contract. Catalog has already checked the identifier grammar. It allows '@'
// and unlimited identifier lengths, which the journal
// rejects. Persist full canonical IDs unchanged; aliases are not journaled.
func journalCatalogID(id string) bool {
	return len(id) <= 128 && !strings.Contains(id, "@") && !strings.Contains(id, "://")
}

func loopbackOrigin(u *url.URL) bool {
	if u.Scheme != "http" || u.Opaque != "" || u.User != nil || u.Path != "" ||
		u.RawPath != "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
		return false
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	n, nerr := strconv.ParseUint(port, 10, 16)
	return err == nil && nerr == nil && n > 0 && ip.Zone() == "" && ip.IsLoopback()
}

func readBounded(r io.Reader, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	return b, err == nil && int64(len(b)) <= limit
}

func readInput(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, string, bool, bool) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Expected application/json")
		return nil, "", false, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request exceeds the fixture limit")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "Could not read request")
		}
		return nil, "", false, false
	}
	var fields map[string]json.RawMessage
	var model string
	var stream *bool
	if json.Unmarshal(body, &fields) != nil || json.Unmarshal(fields["model"], &model) != nil || model == "" ||
		(fields["stream"] != nil && (json.Unmarshal(fields["stream"], &stream) != nil || stream == nil)) {
		writeError(w, http.StatusBadRequest, "invalid_request", "Expected a model and an optional boolean stream")
		return nil, "", false, false
	}
	return fields, model, stream != nil && *stream, true
}

func (h *handler) chat(w http.ResponseWriter, r *http.Request) {
	fields, key, stream, ok := readInput(w, r)
	if !ok {
		return
	}
	model, err := h.fixture.Registry.Lookup(key)
	if err != nil || model.Transport != fixtureTransport {
		writeError(w, http.StatusNotFound, "model_not_found", "Unknown synthetic model")
		return
	}
	// Normalize only the locally routed model name and preserve other fields.
	fields["model"], _ = json.Marshal(model.Name)
	wireBody, err := json.Marshal(fields)
	if err != nil || len(wireBody) > maxRequest {
		writeError(w, http.StatusBadRequest, "invalid_request", "Could not build the bounded upstream request")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), fixtureTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.target, bytes.NewReader(wireBody))
	if err != nil {
		writeError(w, http.StatusBadGateway, "fixture_unavailable", "Could not build the upstream request")
		return
	}
	// Do not copy client headers (including credentials and idempotency
	// keys), follow redirects, or let the transport reconstruct the POST.
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	intent := attemptlog.Intent{
		RequestID:    rand.Text(),
		AttemptID:    rand.Text(),
		Provider:     model.ProviderID,
		Model:        model.ID,
		ConnectionID: "synthetic-loopback",
		Operation:    "inference",
		BodyDigest:   sha256.Sum256(wireBody),
	}
	o := Observation{RequestID: intent.RequestID, AttemptID: intent.AttemptID, Execution: NotSent}
	defer func() {
		if h.fixture.Observe != nil {
			h.fixture.Observe(o)
		}
	}()
	w.Header().Set("X-Request-ID", intent.RequestID)
	if ctx.Err() != nil {
		return
	}
	if err := h.fixture.Journal.RecordIntent(intent); err != nil {
		writeError(w, http.StatusServiceUnavailable, "dispatch_unavailable", "Could not record dispatch intent")
		return
	}
	if ctx.Err() != nil {
		if r.Context().Err() == nil {
			writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "Synthetic dispatch deadline exceeded")
		}
		return
	}
	o.Execution = Unknown
	res, err := h.fixture.Transport.RoundTrip(request)
	if res != nil && res.Body != nil {
		defer res.Body.Close()
	}
	if err != nil || res == nil || res.Body == nil || res.StatusCode != http.StatusOK {
		if r.Context().Err() == nil {
			if ctx.Err() == context.DeadlineExceeded {
				writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "Synthetic upstream timed out")
			} else {
				writeError(w, http.StatusBadGateway, "upstream_unavailable", "Synthetic upstream did not return a completion")
			}
		}
		return
	}

	media, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if stream && media == "text/event-stream" {
		o.Complete, o.Usage = forwardStream(r.Context(), w, res.Body)
	} else if !stream && media == "application/json" {
		o.Complete, o.Usage = forwardJSON(r.Context(), w, res.Body)
	} else {
		writeError(w, http.StatusBadGateway, "invalid_upstream_response", "Unexpected upstream media type")
	}
	if o.Complete {
		o.Execution = Executed
	}
}

type payload struct {
	Object  string            `json:"object"`
	Choices []json.RawMessage `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
	Error   json.RawMessage   `json:"error"`

	uniqueUsage bool
}

func parsePayload(b []byte, kind string) (payload, bool) {
	var p payload
	if json.Unmarshal(b, &p) != nil || p.Object != kind || p.Choices == nil || p.Error != nil {
		return payload{}, false
	}
	for _, choice := range p.Choices {
		if len(choice) == 0 || choice[0] != '{' {
			return payload{}, false
		}
	}
	if p.Usage != nil {
		_, p.uniqueUsage = uniqueJSONFields(b, "usage")
	}
	return p, true
}

// uniqueJSONFields rejects duplicate wanted keys, including escape-equivalent
// keys and aliases folded by encoding/json. Other upstream extension fields
// are not interpreted. The JSON was already bounded by the HTTP relay.
func uniqueJSONFields(raw []byte, wanted ...string) (map[string]json.RawMessage, bool) {
	fields := make(map[string]json.RawMessage, len(wanted))
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		for _, name := range wanted {
			if strings.EqualFold(key, name) {
				if _, exists := fields[name]; exists {
					return nil, false
				}
				fields[name] = value
				break
			}
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false
	}
	_, err = d.Token()
	return fields, err == io.EOF
}

func (p payload) confirmedUsage() *Usage {
	if !p.uniqueUsage {
		return nil
	}
	const promptKey, completionKey, totalKey = "prompt_tokens", "completion_tokens", "total_tokens"
	fields, ok := uniqueJSONFields(p.Usage, promptKey, completionKey, totalKey)
	var prompt, completion, total *int64
	if !ok || json.Unmarshal(fields[promptKey], &prompt) != nil ||
		json.Unmarshal(fields[completionKey], &completion) != nil || json.Unmarshal(fields[totalKey], &total) != nil ||
		prompt == nil || completion == nil || total == nil ||
		*prompt < 0 || *completion < 0 || *total < *prompt || *total-*prompt != *completion {
		return nil
	}
	return &Usage{PromptTokens: *prompt, CompletionTokens: *completion, TotalTokens: *total}
}

func forwardJSON(ctx context.Context, w http.ResponseWriter, r io.Reader) (bool, *Usage) {
	body, bounded := readBounded(r, maxReply)
	parsed, valid := parsePayload(body, "chat.completion")
	if ctx.Err() != nil {
		return false, nil
	}
	if !bounded || !valid || len(parsed.Choices) == 0 {
		writeError(w, http.StatusBadGateway, "invalid_upstream_response", "Invalid synthetic completion")
		return false, nil
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
	return true, parsed.confirmedUsage()
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]map[string]string{
		"error": {"code": code, "type": "synthetic_chat_error", "message": message},
	})
}
