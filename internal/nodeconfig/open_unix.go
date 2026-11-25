//go:build unix

package nodeconfig

import (
	"os"
	"syscall"
)

func openConfigFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
