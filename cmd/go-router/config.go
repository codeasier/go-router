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
)

const maxProcessRevisions = 1024

var errInvalidLocalConfig = errors.New("invalid node configuration")

// localConfiguration exposes one immutable accepted snapshot at a time.
// Only startup and, later, the single local reload watcher call load.
type localConfiguration struct {
	path          string
	managementKey string
	commit        sync.Mutex
	accepted      map[string]struct{}
	active        atomic.Pointer[nodeconfig.Snapshot]
}

func newLocalConfiguration(path, managementKey string) *localConfiguration {
	return &localConfiguration{
		path: path, managementKey: managementKey,
		accepted: make(map[string]struct{}),
	}
}

// identity pairs the revision and byte digest from one immutable snapshot. Never
// re-read active between the two values: a concurrent reload may replace it.
func (c *localConfiguration) identity() (revision, digest string) {
	if c == nil {
		return "", ""
	}
	snap := c.active.Load()
	if snap == nil {
		return "", ""
	}
	sum := snap.Digest()
	digest = hex.EncodeToString(sum[:])
	// Even canonical hex can equal or contain a deliberately chosen bearer.
	if c.managementKey != "" && strings.Contains(digest, c.managementKey) {
		return snap.Revision(), ""
	}
	return snap.Revision(), digest
}

func (c *localConfiguration) load(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	candidate, err := nodeconfig.Load(c.path)
	if err != nil || candidate == nil {
		return false, errInvalidLocalConfig
	}
	return c.activate(ctx, candidate)
}

func (c *localConfiguration) activate(ctx context.Context, candidate *nodeconfig.Snapshot) (bool, error) {
	c.commit.Lock()
	defer c.commit.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	revision := candidate.Revision()
	if revision == "" || (c.managementKey != "" && strings.Contains(revision, c.managementKey)) {
		return false, errInvalidLocalConfig
	}
	if active := c.active.Load(); active != nil && active.Revision() == revision && active.Digest() == candidate.Digest() {
		return false, nil
	}
	// Rollback must use a new label, including when the prior payload is known.
	// Never evict labels or permit rebinding within this process. This isn't a
	// durable audit or a mechanism for rolling back across processes.
	if _, exists := c.accepted[revision]; exists || len(c.accepted) >= maxProcessRevisions {
		return false, errInvalidLocalConfig
	}
	c.accepted[revision] = struct{}{}
	c.active.Store(candidate)
	return true, nil
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
