// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package files writes the files that the nodes of an installation share on the data volume: the
// master key, the SSH host keys and the scripts of the Git hooks. Nodes that start at the same time write them at
// the same time, so a reader on another node must never see half of a file, and a file that must be the same on
// every node (a key) is created once.
package files

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteOnce creates path with data unless it exists, and returns the content the file has then: data when this
// call created it, else what another writer created — perhaps a node that started at the same moment. The file
// appears complete under its name or not at all: it is written to a temporary file next to it and linked into
// place, which fails when the name exists (also on NFS).
func WriteOnce(path string, data []byte, perm fs.FileMode) (content []byte, created bool, err error) {
	if b, err := os.ReadFile(path); err == nil {
		return b, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	tmp, err := temporary(path, data, perm)
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(tmp)
	switch err := os.Link(tmp, path); {
	case err == nil:
		syncDir(filepath.Dir(path))
		return data, true, nil
	case errors.Is(err, fs.ErrExist):
		b, err := os.ReadFile(path)
		return b, false, err
	}
	// File systems without hard links: an exclusive create, which a reader that comes at the very same moment may
	// see before it is written.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		b, err := os.ReadFile(path)
		return b, false, err
	}
	if err != nil {
		return nil, false, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, false, err
	}
	syncDir(filepath.Dir(path))
	return data, true, nil
}

// Replace gives path the content data atomically: a temporary file of its own, synced and renamed into place, so
// that writers on several nodes at once never leave a mix and readers see the old or the new file. A file that has
// the content already is left as it is.
func Replace(path string, data []byte, perm fs.FileMode) error {
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, data) {
		return nil
	}
	tmp, err := temporary(path, data, perm)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// temporary writes data to a new file next to path (a name of its own, so that concurrent writers do not share it)
// and returns its name.
func temporary(path string, data []byte, perm fs.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// syncDir makes a new name in dir durable.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
