package syntheticchat

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/codeasier/go-router/internal/catalog"
)

// Routes is compiled from a single immutable, already validated registry.
// Requests hold the same route set for their entire lifetime, even on reload.
type Routes struct {
	http.Handler
	Ready bool
}

func (l *Local) Routes(registry *catalog.Registry, journal IntentLog, observe func(Observation)) (*Routes, error) {
	type listing struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	models := make([]listing, 0)
	for _, m := range registry.List() {
		if m.Transport != fixtureTransport {
			return nil, errors.New("unsupported transport in synthetic catalog")
		}
		models = append(models, listing{m.ID, "model", m.ProviderID})
	}
	chat, err := New(Fixture{
		Origin: l.origin, Transport: l.transport, Registry: registry,
		Journal: journal, Observe: observe,
	})
	if err != nil {
		return nil, err
	}
	ready := len(models) > 0
	mux := http.NewServeMux()
	mux.Handle("POST "+chatPath, chat)
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": models})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ready": ready})
	})
	return &Routes{Handler: mux, Ready: ready}, nil
}
