//go:build linux || darwin

package attemptlog

import (
	"os"
	"path/filepath"
	"syscall"
)

func privateOwned(st os.FileInfo) bool {
	x, ok := st.Sys().(*syscall.Stat_t)
	return ok && x.Uid == uint32(os.Geteuid()) && st.Mode().Perm()&0077 == 0
}

func openOwnedFile(dir string, create bool) (*os.File, *os.File, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, nil, ErrUnavailable
	}
	d, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	st, err := d.Stat()
	if err != nil || !st.IsDir() || !privateOwned(st) {
		_ = d.Close()
		return nil, nil, ErrUnavailable
	}
	flags := os.O_RDWR | os.O_APPEND | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(filepath.Join(dir, filename), flags, 0600)
	if err != nil {
		_ = d.Close()
		return nil, nil, ErrUnavailable
	}
	st, err = f.Stat()
	if err != nil || !st.Mode().IsRegular() || !privateOwned(st) || st.Sys().(*syscall.Stat_t).Nlink != 1 || syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		_ = f.Close()
		_ = d.Close()
		return nil, nil, ErrUnavailable
	}
	return d, f, nil
}
