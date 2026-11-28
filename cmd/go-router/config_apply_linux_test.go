//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/nodeconfig"
)

func expectedConfig(revision, content string) configPrecondition {
	return configPrecondition{revision, sha256.Sum256([]byte(content))}
}

func TestManagedApplyRejectsBeforeReplacingRestartableSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := attemptlog.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.json")
	marshal := func(revision, transport, model string) string {
		return fmt.Sprintf(`{"schema_version":1,"revision":%q,"providers":[{"id":"fixture","transport":%q,"models":[{"id":%q}]}]}`, revision, transport, model)
	}
	old := marshal("old", "synthetic-http", "alpha")
	writeConfigFixture(t, file, old)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := func() (*localConfiguration, func()) {
		t.Helper()
		node := newLocalConfiguration(file, "")
		_, cleanup, err := prepareSynthetic("127.0.0.1:20128", dir, node, logger)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := node.load(context.Background()); err != nil {
			cleanup()
			t.Fatal(err)
		}
		return node, cleanup
	}
	node, cleanup := start()
	defer func() { cleanup() }()
	ctx := context.Background()
	expect := expectedConfig("old", old)
	candidate := marshal("next", "chat", "bravo")
	if _, err := nodeconfig.Parse([]byte(candidate)); err != nil {
		t.Fatal("fixture must pass metadata preview")
	}
	if changed, err := node.applyPersisted(ctx, expect, []byte(candidate)); changed || !errors.Is(err, errInvalidLocalConfig) {
		t.Fatalf("unsupported runtime candidate: changed=%v err=%v", changed, err)
	}
	if disk, err := os.ReadFile(file); err != nil || string(disk) != old {
		t.Fatal("rejection replaced restartable bytes")
	}
	cleanup()
	node, cleanup = start()
	if revision, digest, isReady := node.identity(); revision != "old" || digest != configDigest(old) || !isReady {
		t.Fatal("restart after rejection must still serve old snapshot")
	}
	candidate = marshal("next", "synthetic-http", "bravo")
	if changed, err := node.applyPersisted(ctx, expect, []byte(candidate)); !changed || err != nil {
		t.Fatalf("valid candidate: %v %v", changed, err)
	}
	if changed, err := node.applyPersisted(ctx, expect, []byte(candidate)); changed || !errors.Is(err, errConfigConflict) {
		t.Fatal("stale precondition replay must fail without writing")
	}
	cleanup()
	node, cleanup = start()
	if revision, digest, isReady := node.identity(); revision != "next" || digest != configDigest(candidate) || !isReady {
		t.Fatal("restart after success must serve new snapshot")
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("expected private artifact")
	}
	if stages, err := filepath.Glob(filepath.Join(dir, ".go-router-stage-*")); err != nil || len(stages) != 0 {
		t.Fatal("temporary file left behind")
	}
}

func TestManagedApplySerializesWritersAndRejectsDiskDrift(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.json")
	c := newLocalConfiguration(file, "")
	ctx := context.Background()
	start := configJSON("initial")
	writeConfigFixture(t, file, start)
	if _, err := c.load(ctx); err != nil {
		t.Fatal(err)
	}
	baseline := expectedConfig("initial", start)
	staleReload := c.active.Load()
	first, err := nodeconfig.Parse([]byte(configJSON("stale")))
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, r := range []string{"winner-a", "winner-b"} {
		wg.Go(func() {
			_, err := c.applyPersisted(ctx, baseline, []byte(configJSON(r)))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	var passed, failed int
	for err := range errs {
		switch {
		case err == nil:
			passed++
		case errors.Is(err, errConfigConflict):
			failed++
		default:
			t.Fatal(err)
		}
	}
	revision, digest, _ := c.identity()
	if passed != 1 || failed != 1 || digest != configDigest(configJSON(revision)) {
		t.Fatal("expected one matching atomic commit")
	}
	if changed, err := c.activateFrom(ctx, staleReload, first); changed || !errors.Is(err, errConfigConflict) {
		t.Fatal("deferred reload replaced newer publication")
	}
	written := configJSON("manual")
	writeConfigFixture(t, file, written)
	if changed, err := c.applyPersisted(ctx, expectedConfig(revision, configJSON(revision)), []byte(configJSON("unexpected"))); changed || !errors.Is(err, errConfigConflict) {
		t.Fatal("disk drift accepted")
	}
	if disk, err := os.ReadFile(file); err != nil || string(disk) != written {
		t.Fatal("overwrite of detected manual change")
	}
}

func TestManagedApplyRequiresPrivateRegularSlot(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	old := configJSON("old")
	path := filepath.Join(dir, "config.json")
	target := filepath.Join(dir, "external.json")
	writeConfigFixture(t, target, old)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	conf := newLocalConfiguration(path, "")
	if _, err := conf.load(ctx); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0755, 0700} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if changed, err := conf.applyPersisted(ctx, expectedConfig("old", old), []byte(configJSON("new"))); changed || !errors.Is(err, errConfigPersistence) {
			t.Fatalf("apply accepted in unsafe slot: changed=%t err=%v", changed, err)
		}
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("unsafe slot was replaced")
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != old {
		t.Fatal("symlink target was modified")
	}
}
