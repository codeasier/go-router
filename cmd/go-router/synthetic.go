package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/nodeconfig"
	"github.com/codeasier/go-router/internal/server"
	"github.com/codeasier/go-router/internal/syntheticchat"
)

// Prepare the fixed peer and journal once, not per reload. A candidate's
// registry and routes are published together only after validation succeeds.
func prepareSynthetic(addr, dir string, config *localConfiguration, logger *slog.Logger) (*server.Server, func(), error) {
	if config == nil {
		return nil, nil, errors.New("synthetic mode requires a local configuration")
	}
	srv, err := server.NewLocalSynthetic(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := config.active.Load()
		if active == nil || active.routes == nil {
			http.Error(w, "synthetic configuration unavailable", http.StatusServiceUnavailable)
			return
		}
		active.routes.ServeHTTP(w, r)
	}))
	if err != nil {
		return nil, nil, err
	}
	journal, err := attemptlog.Open(dir)
	if err != nil {
		return nil, nil, errors.New("synthetic journal unavailable")
	}
	peer, err := syntheticchat.StartLocal()
	if err != nil {
		journal.Close()
		return nil, nil, err
	}
	config.buildRoutes = func(snapshot *nodeconfig.Snapshot) (*syntheticchat.Routes, error) {
		registry := snapshot.Catalog()
		// Only canonical IDs are exposed in the directory, upstream reply and
		// journal. Do not publish a configured management credential in them.
		for _, m := range registry.List() {
			if config.managementKey != "" && strings.Contains(m.ID, config.managementKey) {
				return nil, errInvalidLocalConfig
			}
		}
		return peer.Routes(registry, journal, func(o syntheticchat.Observation) {
			logger.Info("synthetic dispatch", "request_id", o.RequestID, "execution", o.Execution, "complete", o.Complete)
		})
	}
	return srv, func() {
		peer.Close()
		journal.Close()
	}, nil
}

func (c *localConfiguration) status() (string, bool) {
	if c != nil {
		if active := c.active.Load(); active != nil {
			return active.Revision(), active.routes != nil && active.routes.Ready
		}
	}
	return "", false
}
