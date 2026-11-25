//go:build linux || darwin

package attemptlog

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func fixture(id string) Intent {
	return Intent{RequestID: "req-1", AttemptID: id, Provider: "fixture", Model: "demo/chat", ConnectionID: "conn-7", Operation: "inference", BodyDigest: sha256.Sum256([]byte("synthetic body, never store me"))}
}

func TestRecoveryAfterOwnerExit(t *testing.T) {
	// Exit without Close after the public dispatch barrier succeeds. No upstream
	// is contacted. The recovering owner must not authorize the same attempt.
	const key = "GOROUTER_TEST_ATTEMPTLOG_CHILD_DIR"
	if dir := os.Getenv(key); dir != "" {
		j, err := Create(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.RecordIntent(fixture("attempt-1")); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	dir := privateDir(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryAfterOwnerExit$")
	cmd.Env = append(os.Environ(), key+"="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v: %s", err, out)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	in := fixture("attempt-1")
	r, exists, err := j.Lookup(in.AttemptID)
	if err != nil || !exists || r.Intent != in || r.UnixMilli <= 0 {
		t.Fatalf("missing or changed recovered intent: %+v, %v, %v", r, exists, err)
	}
	if err := j.RecordIntent(in); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, filename))
	if err != nil || bytes.Contains(data, []byte("synthetic body")) {
		t.Fatal("body unexpectedly present or journal unreadable")
	}
}

type faultyWriter struct {
	file    *os.File
	partial bool
}

func (w faultyWriter) Write(p []byte) (int, error) {
	if w.partial {
		return w.file.Write(p[:len(p)/2])
	}
	return w.file.Write(p)
}
func (faultyWriter) Sync() error { return errors.New("injected sync error") }

func TestDispatchBarrierFailsClosed(t *testing.T) {
	for _, partial := range []bool{true, false} {
		t.Run(fmt.Sprintf("partial-%t", partial), func(t *testing.T) {
			dir := privateDir(t)
			j, err := Create(dir)
			if err != nil {
				t.Fatal(err)
			}
			j.writer = faultyWriter{j.file, partial}
			calls := 0
			// Synthetic caller: only a successful durable commit opens the barrier.
			dispatch := func(id string) error {
				if err := j.RecordIntent(fixture(id)); err != nil {
					return err
				}
				calls++
				return nil
			}
			if err := dispatch("ambiguous"); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("write or sync: %v", err)
			}
			j.writer = j.file
			if err := dispatch("fresh"); !errors.Is(err, ErrUnavailable) || calls != 0 {
				t.Fatalf("poisoned writer dispatched: %d %v", calls, err)
			}
			if _, _, err := j.Lookup("ambiguous"); !errors.Is(err, ErrUnavailable) {
				t.Fatal("poisoned journal produced a trusted lookup")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			recovered, err := Open(dir)
			if partial {
				if !errors.Is(err, ErrCorrupt) {
					t.Fatalf("torn write: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			if err := recovered.RecordIntent(fixture("ambiguous")); !errors.Is(err, ErrDuplicate) {
				t.Fatalf("complete but unconfirmed record replayed: %v", err)
			}
		})
	}
}

func TestRejectsCorruptLogWithoutRepair(t *testing.T) {
	r := Record{Intent: fixture("a"), UnixMilli: 1}
	line := string(lineFor(r))
	for name, data := range map[string]string{
		"no-header": "", "unknown-format": "v2\n", "bad-digest": header + strings.Repeat("f", 64) + line[64:],
		"torn": header + line[:len(line)-1], "duplicate": header + line + line,
		"oversize": header + strings.Repeat("f", maxLine+1) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateDir(t)
			path := filepath.Join(dir, filename)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("open: %v", err)
			}
			if _, err := Create(dir); err == nil {
				t.Fatal("create overwrote corrupt file")
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != data {
				t.Fatal("failed recovery modified the journal")
			}
		})
	}
}

func TestPathAndOwnerChecks(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, filename)
	if _, err := Open(dir); err == nil {
		t.Fatal("recovery created a missing journal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unexpected path after recovery")
	}
	if _, err := Create("."); err == nil {
		t.Fatal("relative directory accepted")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir); err == nil {
		t.Fatal("insecure directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(privateDir(t), "link-dir")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(linkDir); err == nil {
		t.Fatal("symlink directory accepted")
	}
	other := filepath.Join(privateDir(t), "unrelated")
	if err := os.WriteFile(other, []byte(header), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("symlink journal accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("hardlinked journal accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(header), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("public journal accepted")
	}
}

func TestSerializesAttemptsAndExcludesSecondOwner(t *testing.T) {
	dir := privateDir(t)
	j, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("two writers admitted")
	}
	if _, err := Create(dir); err == nil {
		t.Fatal("existing journal reinitialized")
	}
	var wg sync.WaitGroup
	const count = 12
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := j.RecordIntent(fixture(fmt.Sprintf("id-%d", i))); err != nil {
				t.Errorf("record: %v", err)
			}
		}(i)
	}
	wg.Wait()
	invalid := fixture("bad")
	invalid.ConnectionID = "Bearer fake secret"
	if err := j.RecordIntent(invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid metadata: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordIntent(fixture("after-close")); !errors.Is(err, ErrUnavailable) {
		t.Fatal("closed journal accepted record")
	}
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for i := 0; i < count; i++ {
		if _, exists, err := j.Lookup(fmt.Sprintf("id-%d", i)); err != nil || !exists {
			t.Errorf("lost intent %d: %v", i, err)
		}
	}
}
