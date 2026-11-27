// Package singlepilot wires configured Chat adapters into single-node inference.
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

type prepared interface {
	NewRequest(context.Context, string) (*http.Request, error)
	BodyDigest() [32]byte
	Decode(context.Context, *http.Response, func([]byte) error) (openaichat.Observation, error)
}

// Connection is an immutable operator-defined Chat connection.
type Connection struct {
	id, key string
	models  []string
	prepare func(model string, body []byte, accept string) (prepared, bool, error)
}

func (Connection) String() string     { return "Chat connection (redacted)" }
func (c Connection) GoString() string { return c.String() }

func Generic(provider string, names []string, baseURL, key string) (Connection, error) {
	switch strings.ToLower(provider) {
	case "openrouter", "volceapi", "xai", "xiaomi-tokenplan", "xiaomi-tokenplan-cn", "opencode-go":

		return Connection{}, errors.New("reserved provider ID")
	}
	adapter, err := openaichat.New(baseURL)
	if err != nil {
		return Connection{}, err
	}
	return Connection{provider, key, append([]string(nil), names...), func(model string, body []byte, _ string) (prepared, bool, error) {
		p, err := adapter.Prepare(model, body)
		if err != nil {
			return nil, false, err
		}
		return p, p.Streaming(), nil
	}}, nil
}

func Volce(names []string, key string) Connection {
	return Connection{"volceapi", key, append([]string(nil), names...), func(model string, body []byte, accept string) (prepared, bool, error) {
		p, err := openaichat.PrepareVolceChat(model, body, accept)
		if err != nil {
			return nil, false, err
		}
		return p, p.Streaming(), nil
	}}
}

func XAI(names []string, key string) Connection {
	return Connection{"xai", key, append([]string(nil), names...), func(model string, body []byte, accept string) (prepared, bool, error) {
		p, err := openaichat.PrepareXAIChat(model, body, accept)
		if err != nil {
			return nil, false, err
		}
		return p, p.Streaming(), nil
	}}
}
func Xiaomi(names []string, region, key string) (Connection, error) {
	// The raw reference defaults unknown regions to Singapore. Require an
	// explicit valid region in operator config rather than hiding a typo.
	if region != "" && region != "sgp" && region != "cn" && region != "ams" {
		return Connection{}, errors.New("invalid Xiaomi Chat region")
	}
	return Connection{"xiaomi-tokenplan", key, append([]string(nil), names...), func(model string, body []byte, accept string) (prepared, bool, error) {
		p, err := openaichat.PrepareXiaomiTokenPlanChat(region, model, body, accept)
		if err != nil {
			return nil, false, err
		}
		return p, p.Streaming(), nil
	}}, nil
}

// New mounts selected Chat connections without fallback or retry. The caller
// owns the listener and the journal lifetime.
func New(log journal, connections ...Connection) (http.Handler, error) {
	t := &http.Transport{DisableKeepAlives: true, DisableCompression: true,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	return routes(log, t, 30*time.Second, connections...)
}

func routes(log journal, transport http.RoundTripper, timeout time.Duration, connections ...Connection) (http.Handler, error) {
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
			if len(id) > 128 {
				return nil, errors.New("catalog ID exceeds journal limit")
			}
			for _, candidate := range connections {
				if candidate.key != "" && strings.Contains(id, candidate.key) {
					return nil, errors.New("provider credential overlaps with a public model ID")
				}
			}
			probe, _, err := c.prepare(name, []byte("{}"), "application/json")
			if err != nil {
				return nil, err
			}
			if _, err = probe.NewRequest(context.Background(), c.key); err != nil {
				return nil, err
			}
			models = append(models, catalog.ModelSpec{ID: name})
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
		modelID, err := openaichat.InputModel(body)
		if err != nil {
			reject(w, 400, "invalid Chat body")
			return
		}
		model, err := registry.Lookup(modelID)
		if err != nil {
			reject(w, 404, "unknown model")
			return
		}
		connection := byID[model.ProviderID]
		request, stream, err := connection.prepare(model.Name, body, strings.Join(r.Header.Values("Accept"), ","))
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
		_, err = request.Decode(ctx, response, func(data []byte) error {
			if !started {
				w.Header().Set("Cache-Control", "no-store")
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				started = true
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
			if stream {
				return http.NewResponseController(w).Flush()
			}
			return nil
		})
		if err != nil && !started {
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
