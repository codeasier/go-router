package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/codeasier/go-router/internal/nodeconfig"
	"github.com/codeasier/go-router/internal/server"
)

func (c *localConfiguration) revision() string {
	revision, _ := c.identity()
	return revision
}

func configDigest(body string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(body))) }

func configJSON(revision string) string {
	return fmt.Sprintf(`{"schema_version":1,"revision":%q,"providers":[]}`, revision)
}

func writeConfigFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationActivation(t *testing.T) {
	const key = "synthetic-management-key-0123456789"
	path := filepath.Join(t.TempDir(), "metadata.json")
	config := newLocalConfiguration(path, key)
	for _, c := range []*localConfiguration{config, nil} {
		if r, d := c.identity(); r != "" || d != "" {
			t.Fatal("configuration was not initially empty")
		}
	}

	cases := []struct {
		name, body, want string
		changed, reject  bool
	}{
		{"startup", configJSON("r1"), "r1", true, false},
		{"identical", configJSON("r1"), "r1", false, false},
		{"active-label-new-bytes", configJSON("r1") + "\n", "r1", false, true},
		{"invalid-file", `{"revision":"broken",`, "r1", false, true},
		{"revision-with-managed-key", configJSON("r-" + key + "-suffix"), "r1", false, true},
		{"update", configJSON("r2"), "r2", true, false},
		{"reuse-inactive-label", configJSON("r1"), "r2", false, true},
		{"rollback-under-new-label", configJSON("r3"), "r3", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeConfigFixture(t, path, tc.body)
			changed, err := config.load(context.Background())
			if changed != tc.changed || (err != nil) != tc.reject ||
				(tc.reject && !errors.Is(err, errInvalidLocalConfig)) || config.revision() != tc.want {
				t.Fatalf("configuration activation: changed=%t, rejected=%t, active matches=%t",
					changed, err != nil, config.revision() == tc.want)
			}
		})
	}
}

func TestConfigurationRevisionLimitIsFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	config := newLocalConfiguration(path, "")
	writeConfigFixture(t, path, configJSON("current"))
	if _, err := config.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < maxProcessRevisions; i++ {
		config.accepted[fmt.Sprintf("previous-%d", i)] = struct{}{}
	}
	if changed, err := config.load(context.Background()); changed || err != nil {
		t.Fatal("active identical candidate was not a no-op at capacity")
	}
	writeConfigFixture(t, path, configJSON("new"))
	if changed, err := config.load(context.Background()); changed || !errors.Is(err, errInvalidLocalConfig) || config.revision() != "current" {
		t.Fatal("exhausted revision history was evicted or active revision modified")
	}
}

func TestConcurrentRevisionReadback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	config := newLocalConfiguration(path, "")
	writeConfigFixture(t, path, configJSON("before"))
	if _, err := config.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for ctx.Err() == nil {
				if r, d := config.identity(); (r != "before" && r != "after") || d != configDigest(configJSON(r)) {
					t.Error("reader saw a partial or unaccepted snapshot")
					return
				}
			}
		})
	}
	defer func() {
		stop()
		readers.Wait()
	}()
	writeConfigFixture(t, path, configJSON("after"))
	if _, err := config.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, d := config.identity(); r != "after" || d != configDigest(configJSON("after")) {
		t.Fatal("accepted revision was not published")
	}
}

func TestIdentityNeverEchoesHexBearer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	body := configJSON("r1")
	digest := configDigest(body)
	key := digest[8:40]
	config := newLocalConfiguration(path, key)
	writeConfigFixture(t, path, body)
	if _, err := config.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, d := config.identity(); r != "r1" || d != "" {
		t.Fatal("expected revision and no digest for credential collision")
	}
}

func TestCanceledConfigurationDoesNotPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	config := newLocalConfiguration(path, "")
	writeConfigFixture(t, path, configJSON("old"))
	if _, err := config.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeConfigFixture(t, path, configJSON("new"))
	candidate, err := nodeconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	config.fence()
	if changed, err := config.activate(ctx, candidate); changed || !errors.Is(err, context.Canceled) || config.revision() != "old" {
		t.Fatal("canceled reload published an already read candidate")
	}
}

func TestServeFencesBeforeReturningFromBindFailure(t *testing.T) {
	fenced := false
	err := serve(context.Background(), []*server.Server{server.New("127.0.0.1:65536")}, func() {
		fenced = true
	})
	if err == nil || !fenced {
		t.Fatalf("bind failure did not run the reload fence")
	}
}
