//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/codeasier/go-router/internal/nodeconfig"
)

var (
	errConfigPersistence = errors.New("could not persist node configuration")
	errConfigDurability  = errors.New("node configuration applied but persistence could not be confirmed")
)

type configPrecondition struct {
	revision string
	digest   [sha256.Size]byte
}

// applyPersisted is not an HTTP endpoint. It is the node-local apply primitive
// for the managed pilot. The parent directory must be private to the node and
// this process must be its only writer. A manually edited file is a conflict,
// not a candidate that this path is allowed to overwrite.
func (c *localConfiguration) applyPersisted(ctx context.Context, expected configPrecondition, body []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(body) > nodeconfig.MaxBytes {
		return false, errInvalidLocalConfig
	}
	ownBody := bytes.Clone(body)
	candidate, err := nodeconfig.Parse(ownBody)
	if err != nil {
		return false, errInvalidLocalConfig
	}
	c.commit.Lock()
	defer c.commit.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	active := c.active.Load()
	if active == nil || active.Revision() != expected.revision || active.Digest() != expected.digest {
		return false, errConfigConflict
	}

	parentPath, base := filepath.Dir(c.path), filepath.Base(c.path)
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return false, errConfigPersistence
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return false, errConfigPersistence
	}
	defer dir.Close()
	parent, err := dir.Stat()
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return false, errConfigPersistence
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return false, errConfigPersistence
	}
	info, err := root.Lstat(base)
	// In particular, do not replace an existing symlink, device or FIFO.
	if err != nil || !info.Mode().IsRegular() {
		return false, errConfigPersistence
	}
	current, err := readManagedConfig(root, base, info)
	if err != nil || current.Revision() != expected.revision || current.Digest() != expected.digest {
		return false, errConfigConflict
	}
	next, err := c.prepareLocked(candidate)
	if err != nil || next == nil {
		return false, err
	}

	name := ".go-router-stage-" + rand.Text()
	temporary, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return false, errConfigPersistence
	}
	defer root.Remove(name)
	_, writeErr := temporary.Write(ownBody)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return false, errConfigPersistence
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Detect a slot replaced during staging before
	// publishing. The node must still be the directory's only writer.
	observedParent, parentErr := os.Stat(parentPath)
	observedFile, fileErr := root.Lstat(base)
	if parentErr != nil || fileErr != nil || !os.SameFile(parent, observedParent) || !os.SameFile(info, observedFile) {
		return false, errConfigConflict
	}
	// This same-directory rename is the point of no return. Keep memory and
	// disk consistent even if the caller disconnects or directory sync fails.
	if err := root.Rename(name, base); err != nil {
		return false, errConfigPersistence
	}
	dirErr := dir.Sync()
	c.publishLocked(next)
	if dirErr != nil {
		return true, errConfigDurability
	}
	return true, nil
}

func readManagedConfig(root *os.Root, name string, info os.FileInfo) (*nodeconfig.Snapshot, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errConfigPersistence
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errConfigConflict
	}
	body, err := io.ReadAll(io.LimitReader(file, nodeconfig.MaxBytes+1))
	if err != nil {
		return nil, errConfigPersistence
	}
	return nodeconfig.Parse(body)
}
