package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const inventoryFixture = `{"nodes":[
	{"node_id":"node-a","admin_addr":"127.0.0.1:20181","bearer_env":"GO_ROUTER_DASHBOARD_KEY_A","desired_revision":null},
	{"node_id":"node-b","admin_addr":"127.0.0.1:20182","bearer_env":"GO_ROUTER_DASHBOARD_KEY_B","desired_revision":"rev-1","desired_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
]}`

func privateFixture(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local-inventory.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func noLookup(_ string) (string, bool) { return "", false }

func TestInventoryReferencesEnvironmentWithoutReadingOtherKeys(t *testing.T) {
	var lookedUp []string
	lookup := func(key string) (string, bool) {
		lookedUp = append(lookedUp, key)
		return "synthetic-only-" + key, true
	}
	nodes, err := loadConfig(privateFixture(t, inventoryFixture, 0600), lookup)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("valid private inventory returned %v", err)
	}
	if strings.Join(lookedUp, ",") != "GO_ROUTER_DASHBOARD_KEY_A,GO_ROUTER_DASHBOARD_KEY_B" {
		t.Fatal("unexpected environmental scope")
	}
	if nodes[0].NodeID != "node-a" || nodes[0].DesiredRevision != nil || nodes[1].DesiredRevision == nil || *nodes[1].DesiredRevision != "rev-1" ||
		nodes[0].DesiredDigest != nil || nodes[1].DesiredDigest == nil || *nodes[1].DesiredDigest != strings.Repeat("a", 64) ||
		nodes[0].BearerKey != "synthetic-only-GO_ROUTER_DASHBOARD_KEY_A" || nodes[1].BearerKey != "synthetic-only-GO_ROUTER_DASHBOARD_KEY_B" {
		t.Fatal("private inventory was not mapped correctly")
	}
	if _, err := loadConfig(privateFixture(t, inventoryFixture, 0600), noLookup); !errors.Is(err, errMissingKey) {
		t.Fatalf("missing credential: %v", err)
	}
}

func TestInventoryRejectsUnsafeInputsWithoutEcho(t *testing.T) {
	marker := "synthetic-secret-should-not-appear"
	testCases := []struct {
		name, content string
		mode          os.FileMode
		want          error
	}{
		{"public-file", inventoryFixture, 0644, errPrivateFile},
		{"too-large", strings.Repeat(" ", maxConfigBytes+1), 0600, errReadConfig},
		{"trailing", inventoryFixture + "{}\n", 0600, errInvalidJSON},
		{"empty", `{"nodes":[]}`, 0600, errInvalidJSON},
		{"direct-secret", strings.Replace(inventoryFixture, `"desired_revision":null`, `"bearer_key":"`+marker+`"`, 1), 0600, errInvalidJSON},
		{"other-env", strings.Replace(inventoryFixture, "GO_ROUTER_DASHBOARD_KEY_A", "AWS_SECRET_ACCESS_KEY", 1), 0600, errMissingKey},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			path := privateFixture(t, tc.content, tc.mode)
			_, err := loadConfig(path, func(string) (string, bool) {
				t.Fatal("invalid inventory tried to read from environment")
				return "", false
			})
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), path) {
				t.Fatalf("unexpected inventory error: %v", err)
			}
		})
	}
	private := privateFixture(t, inventoryFixture, 0600)
	symlink := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(private, symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := loadConfig(symlink, noLookup); !errors.Is(err, errPrivateFile) {
		t.Fatalf("symlink was not rejected: %v", err)
	}
}

func TestExplicitOptInAndRedactedArgs(t *testing.T) {
	config := privateFixture(t, inventoryFixture, 0600)
	for _, args := range [][]string{
		nil,
		{"-bearer=synthetic-secret-argument"},
		{"-config", config, "-addr", "0.0.0.0:20129"},
		{"-config", config, "-addr", "localhost:20129"},
		{"-config", config, "-addr", "127.0.0.1:0"},
	} {
		var out bytes.Buffer
		if got := run(context.Background(), args, noLookup, &out); got != 2 || strings.Contains(out.String(), "synthetic-secret-argument") || strings.Contains(out.String(), config) {
			t.Fatalf("unexpected or unredacted argument error: %d %q", got, out.String())
		}
	}
}

func TestNumericHostBoundary(t *testing.T) {
	calls := 0
	h := localHostOnly("127.0.0.1:20129", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	for _, host := range []string{"localhost:20129", "example.invalid:20129", "127.0.0.1:80", "127.0.0.1:20129"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		h.ServeHTTP(w, r)
		want := http.StatusMisdirectedRequest
		if host == "127.0.0.1:20129" {
			want = http.StatusOK
		}
		if got := w.Code; got != want {
			t.Fatalf("host %q got %d, want %d", host, got, want)
		}
	}
	if calls != 1 {
		t.Fatalf("wrapped handler called %d times", calls)
	}
}
