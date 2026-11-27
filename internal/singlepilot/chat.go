// Package singlepilot wires the generic Chat adapter into the single-node
// inference path. It does not implement provider-specific protocol transforms.
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

func routes(provider string, names []string, baseURL, key string, log journal, transport http.RoundTripper, timeout time.Duration) (http.Handler, error) {
	if len(names) == 0 || len(names) > 128 || log == nil || strings.EqualFold(provider, "openrouter") || strings.EqualFold(provider, "volceapi") {
		return nil, errors.New("invalid generic Chat connection")
	}
	adapter, err := openaichat.New(baseURL)
	if err != nil {
		return nil, err
	}
	specs := make([]catalog.ModelSpec, 0, len(names))
	for _, name := range names {
		if len(provider)+1+len(name) > 128 {
			return nil, errors.New("catalog ID exceeds journal limit")
		}
		if key != "" && strings.Contains(provider+"/"+name, key) {
			return nil, errors.New("provider credential overlaps with a public model ID")
		}
		specs = append(specs, catalog.ModelSpec{ID: name})
	}
	registry, err := catalog.New([]catalog.ProviderSpec{{ID: provider, Transport: "generic-chat", Models: specs}})
	if err != nil {
		return nil, err
	}
	probe, err := adapter.Prepare(names[0], []byte("{}"))
	if err != nil {
		return nil, err
	}
	if _, err = probe.NewRequest(context.Background(), key); err != nil {
		return nil, err
	}
	type listedModel struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	models := make([]listedModel, 0, len(names))
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
		request, err := adapter.PrepareParsed(model.Name, input, r.Header.Values("Accept"))
		if err != nil {
			reject(w, 400, "invalid Chat body")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		outbound, err := request.NewRequest(ctx, key)
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
