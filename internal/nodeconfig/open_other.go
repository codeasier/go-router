//go:build !unix

package nodeconfig

import "os"

// The caller must keep the operator-selected path in a trusted directory.
// Use atomic renames of regular files; no nonblocking open is assumed here.
func openConfigFile(path string) (*os.File, error) { return os.Open(path) }
