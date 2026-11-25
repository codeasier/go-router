//go:build linux || darwin

package nodeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Record the pre-open file information and replace the path exactly before the
// open, instead of relying on a timing-sensitive rename goroutine.
func TestOpenRegularRejectsRacedReplacement(t *testing.T) {
	for _, tc := range []struct {
		name string
		pipe bool
	}{{"regular file", false}, {"named pipe", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path, replacement := filepath.Join(dir, "node.json"), filepath.Join(dir, "replacement")
			if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.pipe {
				err = syscall.Mkfifo(replacement, 0o600)
			} else {
				err = os.WriteFile(replacement, []byte("replacement"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}

			result := make(chan error, 1)
			go func() {
				f, err := openRegular(path, before)
				if f != nil {
					f.Close()
				}
				result <- err
			}()
			select {
			case err := <-result:
				if !errors.Is(err, ErrRead) {
					t.Fatalf("raced replacement must fail closed, got %v", err)
				}
			case <-time.After(2 * time.Second):
				// Unblock a regressed blocking open, if any, before exiting.
				if tc.pipe {
					f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
					if err == nil {
						defer f.Close()
						select {
						case <-result:
						case <-time.After(time.Second):
						}
					}
				}
				t.Fatal("opening a raced replacement blocked")
			}
		})
	}
}
