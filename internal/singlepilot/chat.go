// Package singlepilot wires configured generic and native Chat connections into single-node inference.
package singlepilot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/catalog"
	"github.com/codeasier/go-router/internal/openaichat"
)

const maxBody = 256 << 10

type journal interface {
	RecordIntent(attemptlog.Intent) error
}

// New serves one operator-configured Chat connection with an immutable catalog
// and no fallback or retry. The caller owns the listener and journal lifetime.
func New(provider string, names []string, baseURL, key string, log journal) (http.Handler, error) {
	return routes(provider, names, baseURL, key, log, chatTransport(), 30*time.Second)
}

// ChatOptions is an explicit operator snapshot. A nil value leaves its
// feature disabled; present-but-empty configuration is not a fallback.
type ChatOptions struct {
	StrictProxy *string
	HeadersJSON *string
}

func (ChatOptions) String() string   { return "generic Chat options (redacted)" }
func (ChatOptions) GoString() string { return "generic Chat options (redacted)" }

// NewConfigured selects headers and transport for the same fixed Chat
// connection. No request, model or client header may modify these options.
func NewConfigured(provider string, names []string, baseURL, key string, log journal, options ChatOptions) (http.Handler, error) {
	c, err := GenericConfigured(provider, names, baseURL, key, options.HeadersJSON)
	if err != nil {
		return nil, err
	}
	var transport http.RoundTripper
	if options.StrictProxy != nil {
		proxy, err := strictProxyOrigin(*options.StrictProxy)
		if err != nil {
			return nil, err
		}
		transport = newStrictProxyTransport(proxy, strictProxySetupTimeout)
	} else {
		transport = chatTransport()
	}
	return connectionRoutes(log, transport, 30*time.Second, c)
}

// Connection is an immutable operator-defined Chat connection.
type Connection struct {
	id, key string
	models  []string
	headers map[string]string
	prepare func(string, *openaichat.ParsedRequest, []string) (*openaichat.Prepared, error)
}

func (Connection) String() string     { return "Chat connection (redacted)" }
func (c Connection) GoString() string { return c.String() }

// Generic builds a connection without optional outbound headers.
func Generic(provider string, names []string, baseURL, key string) (Connection, error) {
	return GenericConfigured(provider, names, baseURL, key, nil)
}

// GenericConfigured parses an operator header snapshot belonging exclusively
// to this fixed generic connection. Native connections cannot opt into it.
func GenericConfigured(provider string, names []string, baseURL, key string, headersJSON *string) (Connection, error) {
	var headers map[string]string
	if headersJSON != nil {
		var err error
		headers, err = parseCustomHeaders(*headersJSON)
		if err != nil {
			return Connection{}, err
		}
	}
	if strings.EqualFold(provider, "openrouter") || strings.EqualFold(provider, "volceapi") {
		return Connection{}, errors.New("reserved provider ID")
	}
	adapter, err := openaichat.New(baseURL)
	if err != nil {
		return Connection{}, err
	}
	return Connection{id: provider, key: key, models: append([]string(nil), names...), headers: headers, prepare: adapter.PrepareParsed}, nil
}

func Volce(names []string, key string) Connection {
	return Connection{id: "volceapi", key: key, models: append([]string(nil), names...), prepare: openaichat.PrepareVolceParsed}
}

func NewConnections(log journal, connections ...Connection) (http.Handler, error) {
	return connectionRoutes(log, chatTransport(), 30*time.Second, connections...)
}

func routes(provider string, names []string, baseURL, key string, log journal, transport http.RoundTripper, timeout time.Duration) (http.Handler, error) {
	c, err := Generic(provider, names, baseURL, key)
	if err != nil {
		return nil, err
	}
	return connectionRoutes(log, transport, timeout, c)
}

func connectionRoutes(log journal, transport http.RoundTripper, timeout time.Duration, connections ...Connection) (http.Handler, error) {
	if log == nil || transport == nil || len(connections) == 0 || len(connections) > 16 || timeout <= 0 {
		return nil, errors.New("invalid Chat connections")
	}
	specs := make([]catalog.ProviderSpec, 0, len(connections))
	byID := make(map[string]Connection, len(connections))
	for _, c := range connections {
		if len(c.models) == 0 || len(c.models) > 128 || c.prepare == nil {
			return nil, errors.New("invalid Chat connection")
		}
		models := make([]catalog.ModelSpec, 0, len(c.models))
		for _, name := range c.models {
			id := c.id + "/" + name
			if !attemptlog.ValidRouteIDs(c.id, id, c.id) {
				return nil, errors.New("configured Chat route ID cannot be recorded in the journal")
			}
			for _, candidate := range connections {
				if candidate.key != "" && strings.Contains(id, candidate.key) {
					return nil, errors.New("provider credential overlaps with a public model ID")
				}
			}
			models = append(models, catalog.ModelSpec{ID: name})
		}
		parsed, err := openaichat.ParseClientRequest([]byte(`{"model":"startup"}`))
		if err != nil {
			return nil, err
		}
		probe, err := c.prepare(c.models[0], parsed, []string{"application/json"})
		if err != nil {
			return nil, err
		}
		if _, err = probe.NewRequest(context.Background(), c.key); err != nil {
			return nil, err
		}
		specs = append(specs, catalog.ProviderSpec{ID: c.id, Transport: "provider-chat", Models: models})
		byID[c.id] = c
	}
	registry, err := catalog.New(specs)
	if err != nil {
		return nil, err
	}
	type listedModel struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	models := make([]listedModel, 0)
	for _, m := range registry.List() {
		models = append(models, listedModel{m.ID, "model", m.ProviderID})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"ready": true})
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"object": "list", "data": models})
	})
	slots := make(chan struct{}, 16)
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.URL.RawQuery != "" || r.URL.ForceQuery {
			reject(w, 400, "invalid Chat request")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			reject(w, 503, "inference capacity reached")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var size *http.MaxBytesError
			if errors.As(err, &size) {
				reject(w, 413, "Chat body too large")
			} else {
				reject(w, 400, "invalid Chat body")
			}
			return
		}
		// Check structure before lookup, then apply the real route's model and
		// stream negotiation once, before recording intent or dispatching.
		input, err := openaichat.ParseClientRequest(body)
		if err != nil {
			reject(w, 400, "invalid Chat body")
			return
		}
		model, err := registry.Lookup(input.Model())
		if err != nil {
			reject(w, 404, "unknown model")
			return
		}
		connection := byID[model.ProviderID]
		request, err := connection.prepare(model.Name, input, r.Header.Values("Accept"))
		if err != nil {
			reject(w, 400, "invalid Chat body")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		outbound, err := request.NewRequest(ctx, connection.key)
		if err != nil {
			reject(w, 503, "upstream configuration unavailable")
			return
		}
		// Apply only the selected route's operator snapshot, only to its
		// origin request. Native routes have no generic header snapshot;
		// the transport never consumes it as CONNECT headers either.
		for name, value := range connection.headers {
			outbound.Header.Set(name, value)
		}
		outbound.Header.Set("Accept-Encoding", "identity")
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			reject(w, 503, "could not allocate request ID")
			return
		}
		id := hex.EncodeToString(nonce[:])
		if ctx.Err() != nil {
			return
		}
		if err = log.RecordIntent(attemptlog.Intent{RequestID: id, AttemptID: id, Provider: model.ProviderID,
			Model: model.ID, ConnectionID: model.ProviderID, Operation: "chat", BodyDigest: request.BodyDigest()}); err != nil {
			reject(w, 503, "dispatch journal unavailable")
			return
		}
		if ctx.Err() != nil {
			return
		}
		response, err := transport.RoundTrip(outbound)
		if err != nil {
			upstreamFailure(w, ctx, err)
			return
		}
		started := false
		observation, err := request.DecodeMedia(ctx, response, func(media string, data []byte) error {
			if !started {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", media)
				started = true
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
			if media == "text/event-stream" {
				return http.NewResponseController(w).Flush()
			}
			return nil
		})
		if err != nil && !started {
			if ctx.Err() == nil && errors.Is(err, openaichat.ErrHTTPStatus) && observation.HTTPStatus == http.StatusTooManyRequests {
				reject(w, http.StatusTooManyRequests, "upstream rate limited")
				return
			}
			upstreamFailure(w, ctx, err)
		}
		// An interrupted stream ends without a fabricated [DONE] or a second call.
	})
	return mux, nil
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func reject(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func upstreamFailure(w http.ResponseWriter, ctx context.Context, err error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	var timeout interface{ Timeout() bool }
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		reject(w, 504, "upstream timeout")
		return
	}
	reject(w, 502, "invalid upstream response")
}
