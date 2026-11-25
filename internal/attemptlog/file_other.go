//go:build !linux && !darwin

package attemptlog

import "os"

// No durability/ownership contract has been established on this platform.
func openOwnedFile(string, bool) (*os.File, *os.File, error) { return nil, nil, ErrUnavailable }
