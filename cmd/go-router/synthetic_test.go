package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/codeasier/go-router/internal/attemptlog"
)

func TestSyntheticPreparationRequiresExplicitLocalMode(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "missing")
	for _, addr := range []string{":20128", "0.0.0.0:20128", "localhost:20128", "127.0.0.1:0", "[::1%lo]:20128"} {
		if _, _, err := prepareSynthetic(addr, path, newLocalConfiguration("unused", ""), logger); err == nil {
			t.Fatalf("accepted unsafe listener %q", addr)
		}
	}
	if _, _, err := prepareSynthetic("127.0.0.1:20128", path, nil, logger); err == nil {
		t.Fatal("accepted missing config")
	}
	if _, _, err := prepareSynthetic("127.0.0.1:20128", path, newLocalConfiguration("unused", ""), logger); err == nil {
		t.Fatal("silently initialized an unavailable journal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unexpected journal creation")
	}
}

func TestSyntheticActivationIsAllOrNothing(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("durable journal not implemented on this platform")
	}
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
	const key = "synthetic-management-key-0123456789"
	path := filepath.Join(dir, "config.json")
	config := newLocalConfiguration(path, key)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const addr = "127.0.0.1:20128"
	srv, closeFixture, err := prepareSynthetic(addr, dir, config, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFixture()
	request := func(method, path, host, origin, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		srv.Handler().ServeHTTP(rec, r)
		return rec
	}
	load := func(revision, name, transport string) (bool, error) {
		doc := fmt.Sprintf(`{"schema_version":1,"revision":%q,"providers":[{"id":"fx","transport":%q,"models":[{"id":%q,"aliases":["alias"]}]}]}`, revision, transport, name)
		writeConfigFixture(t, path, doc)
		return config.load(context.Background())
	}
	if _, err := load("r1", "demo/chat", "synthetic-http"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ name, transport string }{
		{"not/a/fixture", "chat"},
		{"not@journalable", "synthetic-http"},
		{key, "synthetic-http"},
	} {
		if changed, err := load("r2", bad.name, bad.transport); changed || err == nil {
			t.Fatal("unusable model published")
		}
		if revision, ready := config.status(); revision != "r1" || !ready {
			t.Fatal("invalid config changed revision/readiness")
		}
	}
	// No stale/mixed directory after rejection: use the same previous
	// generation to resolve an alias, dispatch, and report readiness.
	models := request("GET", "/v1/models", addr, "", "")
	if models.Code != 200 || !strings.Contains(models.Body.String(), `"id":"fx/demo/chat"`) || strings.Contains(models.Body.String(), "alias") {
		t.Fatal("unexpected active directory")
	}
	const body = `{"model":"fx/alias","messages":[{"role":"user","content":"private synthetic input"}]}`
	for _, caller := range []struct{ host, origin string }{{"example.invalid:20128", ""}, {addr, "https://example.invalid"}} {
		if r := request("POST", "/v1/chat/completions", caller.host, caller.origin, body); r.Code != http.StatusForbidden {
			t.Fatal("nonlocal HTTP authority or browser origin reached the fixture")
		}
	}
	out := request("POST", "/v1/chat/completions", addr, "", body)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"model":"demo/chat"`) {
		t.Fatalf("previous routing generation did not survive a rejected candidate: %d", out.Code)
	}
	journal, err := os.ReadFile(filepath.Join(dir, "dispatch-intents.v1"))
	id := out.Header().Get("X-Request-ID")
	if err != nil || id == "" || !strings.Contains(string(journal), id) || strings.Count(string(journal), "\n") != 2 ||
		strings.Contains(string(journal), "private synthetic input") || strings.Contains(string(journal), key) {
		t.Fatal("expected exactly one matching intent and no sensitive input")
	}
	// A rejected candidate must not consume the revision label.
	if changed, err := load("r2", "other", "synthetic-http"); err != nil || !changed {
		t.Fatal("valid replacement under previously rejected label failed")
	}
}
