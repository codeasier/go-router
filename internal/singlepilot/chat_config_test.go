package singlepilot

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codeasier/go-router/internal/attemptlog"
)

func TestChatRejectsUnjournalableRoutesAtStartup(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		models         []string
	}{
		{"provider", "provider@revision", []string{"a"}},
		{"second model", "provider", []string{"a", "b@revision"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := attemptlog.Create(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = journal.Close() })
			h, err := routes(tc.provider, tc.models, "https://provider.invalid/v1", "test-key", journal,
				roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network during startup"); return nil, nil }), time.Second)
			if err == nil || h != nil {
				t.Fatal("unrecordable route must prevent startup")
			}
			raw, err := os.ReadFile(filepath.Join(dir, "dispatch-intents.v1"))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != "go-router/dispatch-intents/v1\n" {
				t.Fatal("configuration validation appended a probe record")
			}
		})
	}
}
