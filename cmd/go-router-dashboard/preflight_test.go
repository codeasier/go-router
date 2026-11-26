package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/codeasier/go-router/internal/controlplane"
)

const (
	preflightCandidate = `{"schema_version":1,"revision":"rev-7","providers":[{"id":"synthetic","transport":"chat","models":[{"id":"demo"}]}]}` + "\n"
	preflightKeyA      = "fixture-preflight-a-0123456789abcdef"
	preflightKeyB      = "fixture-preflight-b-0123456789abcdef"
)

func TestPreflightPreviewAndCheck(t *testing.T) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(preflightCandidate)))
	var results [2]atomic.Int32
	var mode atomic.Int32
	serve := func(i int, id, key string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			results[i].Add(1)
			if r.Method != http.MethodGet || r.URL.Path != "/admin/v1/status" || r.URL.RawQuery != "" ||
				r.Header.Get("Authorization") != "Bearer "+key {
				t.Error("preflight has an unexpected request contract")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if i == 1 {
				switch mode.Load() {
				case 1:
					fmt.Fprintf(w, `{"node_id":%q,"ready":false,"applied_revision":"rev-7","applied_digest":%q}`, id, strings.Repeat("b", 64))
					return
				case 2:
					fmt.Fprintf(w, `{"node_id":%q,"ready":false,"applied_revision":"rev-7"}`, id)
					return
				case 3:
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				case 4:
					fmt.Fprintf(w, `{"node_id":%q,"ready":false,"applied_revision":"rev-7","applied_digest":%q}`, id, key)
					return
				}
			}
			fmt.Fprintf(w, `{"node_id":%q,"ready":false,"applied_revision":"rev-7","applied_digest":%q}`, id, digest)
		}))
	}
	a, b := serve(0, "node-a", preflightKeyA), serve(1, "node-b", preflightKeyB)
	defer a.Close()
	defer b.Close()
	secrets := map[string]string{
		"GO_ROUTER_DASHBOARD_KEY_A": preflightKeyA,
		"GO_ROUTER_DASHBOARD_KEY_B": preflightKeyB,
	}
	lookup := func(key string) (string, bool) {
		value, ok := secrets[key]
		return value, ok
	}
	inventoryBody := fmt.Sprintf(`{"nodes":[
		{"node_id":"node-a","admin_addr":%q,"bearer_env":"GO_ROUTER_DASHBOARD_KEY_A","desired_revision":"old"},
		{"node_id":"node-b","admin_addr":%q,"bearer_env":"GO_ROUTER_DASHBOARD_KEY_B"}
	]}`, strings.TrimPrefix(a.URL, "http://"), strings.TrimPrefix(b.URL, "http://"))
	candidate := privateFixture(t, preflightCandidate, 0600)
	inventory := privateFixture(t, inventoryBody, 0600)
	t.Cleanup(func() {
		for path, want := range map[string]string{candidate: preflightCandidate, inventory: inventoryBody} {
			body, err := os.ReadFile(path)
			if err != nil || string(body) != want {
				t.Error("preflight changed an input")
			}
		}
	})

	base := []string{"preflight", "-config", inventory, "-candidate", candidate}
	for _, tc := range []struct {
		name, bState, bComparison  string
		mode, code, aCalls, bCalls int
		ids                        []string
		check, converged           bool
	}{
		{"offline-preview", "Not checked", "Unverified", 0, 0, 0, 0, []string{"node-b", "node-a"}, false, false},
		{"same-rev-wrong-bytes", "Not ready", "Drift", 1, 1, 1, 1, []string{"node-b", "node-a"}, true, false},
		{"converged", "Not ready", "Matched", 0, 0, 1, 1, []string{"node-a", "node-b"}, true, true},
		{"legacy-digest", "Not ready", "Unverified", 2, 1, 1, 1, []string{"node-a", "node-b"}, true, false},
		{"offline-node", "Offline", "Unverified", 3, 1, 1, 1, []string{"node-a", "node-b"}, true, false},
		{"untrusted-digest", "Offline", "Unverified", 4, 1, 1, 1, []string{"node-a", "node-b"}, true, false},
		{"selected-only", "", "", 0, 0, 1, 0, []string{"node-a"}, true, true},
		{"unknown-target", "", "", 0, 2, 0, 0, []string{"node-a", "extra"}, true, false},
		{"duplicate-target", "", "", 0, 2, 0, 0, []string{"node-a", "node-a"}, true, false},
		{"no-targets", "", "", 0, 2, 0, 0, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(int32(tc.mode))
			beforeA, beforeB := results[0].Load(), results[1].Load()
			args := append([]string(nil), base...)
			for _, id := range tc.ids {
				args = append(args, "-node", id)
			}
			if tc.check {
				args = append(args, "-check")
			}
			var out, diagnostics bytes.Buffer
			env := lookup
			if !tc.check {
				env = func(string) (string, bool) {
					t.Fatal("offline preview attempted to resolve a bearer")
					return "", false
				}
			}
			if got := dispatch(context.Background(), args, env, &out, &diagnostics); got != tc.code {
				t.Fatalf("preflight exit=%d, want %d; diagnostics: %s", got, tc.code, diagnostics.String())
			}
			if int(results[0].Load()-beforeA) != tc.aCalls || int(results[1].Load()-beforeB) != tc.bCalls {
				t.Fatal("preflight contacted unexpected nodes")
			}
			for _, private := range []string{preflightKeyA, preflightKeyB, a.URL, b.URL, candidate, inventory} {
				if strings.Contains(out.String(), private) || strings.Contains(diagnostics.String(), private) {
					t.Fatal("preflight output included private inventory or path data")
				}
			}
			if tc.code == 2 {
				if out.Len() != 0 || diagnostics.Len() == 0 {
					t.Fatal("invalid preflight output contract")
				}
				return
			}
			var report controlplane.ConfigPreflight
			if err := json.Unmarshal(out.Bytes(), &report); err != nil || diagnostics.Len() != 0 ||
				report.SchemaVersion != 1 || report.Target.Revision != "rev-7" || report.Target.Digest != digest ||
				len(report.Nodes) != len(tc.ids) || (report.Converged != nil) != tc.check ||
				(tc.check && *report.Converged != tc.converged) {
				t.Fatalf("unexpected preflight report %s; diagnostics %s", out.String(), diagnostics.String())
			}
			node := report.Nodes[0]
			if node.NodeID != "node-a" || (tc.check && (node.Comparison != "Matched" ||
				node.Observation != "Not ready" || node.AppliedDigest == nil || *node.AppliedDigest != digest)) ||
				(!tc.check && (node.Observation != "Not checked" || node.AppliedDigest != nil)) {
				t.Fatal("node A receipt not a correct baseline")
			}
			if len(report.Nodes) == 2 {
				node = report.Nodes[1]
				if node.NodeID != "node-b" || node.Observation != tc.bState || node.Comparison != tc.bComparison {
					t.Fatal("node B receipt not kept separate")
				}
			}
		})
	}
}

func TestPreflightRejectsInvalidOrIncompleteArtifacts(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":1,"revision":"rev-7"}`,
		`{"schema_version":1,"revision":"r","providers":[],"api_key":"sensitive-marker"}`,
		`{"schema_version":1,"revision":"r","providers":[{"id":"synthetic","transport":"chat"}]}`,
		`{"schema_version":1,"revision":"r","providers":[],"revision":"different"}`,
		`{"schema_version":2,"revision":"r","providers":[]}`,
		strings.Repeat(" ", 1<<20) + preflightCandidate,
	} {
		candidate := privateFixture(t, body, 0600)
		var out, diagnostic bytes.Buffer
		args := []string{"preflight", "-config", privateFixture(t, inventoryFixture, 0600), "-candidate", candidate, "-node", "node-a", "-check"}
		code := dispatch(context.Background(), args, func(string) (string, bool) {
			t.Fatal("invalid artifact attempted to access a key")
			return "", false
		}, &out, &diagnostic)
		if code != 2 || out.Len() != 0 || diagnostic.Len() == 0 ||
			strings.Contains(diagnostic.String(), candidate) || strings.Contains(diagnostic.String(), "sensitive-marker") {
			t.Fatal("invalid artifact admitted or leaked information")
		}
	}
}

func TestPreflightRejectsCredentialInCandidateIdentity(t *testing.T) {
	inventory := privateFixture(t, inventoryFixture, 0600)
	for _, tc := range []struct{ body, key string }{
		{fmt.Sprintf(`{"schema_version":1,"revision":"r-%s","providers":[]}`, preflightKeyA), preflightKeyA},
		{preflightCandidate, fmt.Sprintf("%x", sha256.Sum256([]byte(preflightCandidate)))},
	} {
		candidate := privateFixture(t, tc.body, 0600)
		var out, diagnostic bytes.Buffer
		args := []string{"preflight", "-config", inventory, "-candidate", candidate, "-node", "node-a", "-check"}
		code := dispatch(context.Background(), args, func(string) (string, bool) { return tc.key, true }, &out, &diagnostic)
		if code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), tc.key) {
			t.Fatal("candidate identity could echo a registered key")
		}
	}
}
