package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/codeasier/go-router/internal/nodeconfig"
	"github.com/codeasier/go-router/internal/syntheticchat"
)

const maxProcessRevisions = 1024

var (
	errInvalidLocalConfig = errors.New("invalid node configuration")
	errConfigConflict     = errors.New("node configuration changed")
)

// localConfiguration exposes one immutable accepted snapshot at a time.
// The local reload watcher and a managed apply share its publication lock.
type activeConfiguration struct {
	*nodeconfig.Snapshot
	routes *syntheticchat.Routes
}

type localConfiguration struct {
	path          string
	managementKey string
	commit        sync.Mutex
	accepted      map[string]struct{}
	active        atomic.Pointer[activeConfiguration]
	buildRoutes   func(*nodeconfig.Snapshot) (*syntheticchat.Routes, error)
}

func newLocalConfiguration(path, managementKey string) *localConfiguration {
	return &localConfiguration{
		path: path, managementKey: managementKey,
		accepted: make(map[string]struct{}),
	}
}

// identity pairs the revision and byte digest from one immutable snapshot. Never
// re-read active between the two values: a concurrent reload may replace it.
func (c *localConfiguration) identity() (revision, digest string, ready bool) {
	if c == nil {
		return "", "", false
	}
	snap := c.active.Load()
	if snap == nil {
		return "", "", false
	}
	activeReady := snap.routes != nil && snap.routes.Ready
	sum := snap.Digest()
	digest = hex.EncodeToString(sum[:])
	// Even canonical hex can equal or contain a deliberately chosen bearer.
	if c.managementKey != "" && strings.Contains(digest, c.managementKey) {
		return snap.Revision(), "", activeReady
	}
	return snap.Revision(), digest, activeReady
}

func (c *localConfiguration) load(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	previous := c.active.Load()
	candidate, err := nodeconfig.Load(c.path)
	if err != nil || candidate == nil {
		return false, errInvalidLocalConfig
	}
	return c.activateFrom(ctx, previous, candidate)
}

func (c *localConfiguration) activateFrom(ctx context.Context, previous *activeConfiguration, candidate *nodeconfig.Snapshot) (bool, error) {
	c.commit.Lock()
	defer c.commit.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c.active.Load() != previous {
		return false, errConfigConflict
	}
	next, err := c.prepareLocked(candidate)
	if err != nil || next == nil {
		return false, err
	}
	c.publishLocked(next)
	return true, nil
}

// prepareLocked validates the runtime routes but does not consume a revision
// or alter the active snapshot. Caller must hold commit.
func (c *localConfiguration) prepareLocked(candidate *nodeconfig.Snapshot) (*activeConfiguration, error) {
	revision := candidate.Revision()
	if revision == "" || (c.managementKey != "" && strings.Contains(revision, c.managementKey)) {
		return nil, errInvalidLocalConfig
	}
	if active := c.active.Load(); active != nil && active.Revision() == revision && active.Digest() == candidate.Digest() {
		return nil, nil
	}
	// Rollback must use a new label, including when the prior payload is known.
	// Never evict labels or permit rebinding within this process. This isn't a
	// durable audit or a mechanism for rolling back across processes.
	if _, exists := c.accepted[revision]; exists || len(c.accepted) >= maxProcessRevisions {
		return nil, errInvalidLocalConfig
	}
	next := &activeConfiguration{Snapshot: candidate}
	if c.buildRoutes != nil {
		routes, err := c.buildRoutes(candidate)
		if err != nil || routes == nil {
			return nil, errInvalidLocalConfig
		}
		next.routes = routes
	}
	return next, nil
}

func (c *localConfiguration) publishLocked(next *activeConfiguration) {
	c.accepted[next.Revision()] = struct{}{}
	c.active.Store(next)
}

// fence waits for any publication already racing cancellation. File I/O is
// outside this lock, so a blocked read cannot delay listener shutdown.
func (c *localConfiguration) fence() {
	if c != nil {
		c.commit.Lock()
		c.commit.Unlock()
	}
}

func (c *localConfiguration) watch(ctx context.Context, signals <-chan os.Signal, logger *slog.Logger) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-signals:
			if !ok {
				return
			}
		}
		changed, err := c.load(ctx)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			logger.Warn("node configuration reload rejected")
		case changed:
			logger.Info("node configuration reloaded")
		default:
			logger.Info("node configuration unchanged")
		}
	}
}
