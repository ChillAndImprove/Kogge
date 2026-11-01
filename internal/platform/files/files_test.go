// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package files_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"kogge.de/kogge/internal/platform/files"
)

// TestWriteOnce: writers that start at the same moment (nodes on one data volume) agree on one file, which exists
// complete or not at all; a file that exists is kept.
func TestWriteOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host_key")
	const writers = 16
	contents := make([][]byte, writers)
	created := make([]bool, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := []byte(strings.Repeat(fmt.Sprintf("key of writer %02d\n", i), 1000))
			var err error
			if contents[i], created[i], err = files.WriteOnce(path, data, 0o600); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	n := 0
	for i := range writers {
		if created[i] {
			n++
		}
		if !bytes.Equal(contents[i], contents[0]) {
			t.Fatalf("writer %d sees another file than writer 0", i)
		}
	}
	if n != 1 {
		t.Errorf("%d writers created the file", n)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(onDisk, contents[0]) {
		t.Fatalf("the file differs from what the writers were told: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v %v", info.Mode(), err)
	}
	got, again, err := files.WriteOnce(path, []byte("another key"), 0o600)
	if err != nil || again || !bytes.Equal(got, onDisk) {
		t.Errorf("an existing file: created %v, %v", again, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// TestReplace: concurrent writers leave one complete version, never a mix; a file with the content already is not
// written again.
func TestReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pre-receive")
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := files.Replace(path, []byte(strings.Repeat(fmt.Sprintf("script %02d\n", i), 1000)), 0o750); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first := string(b[:10])
	if !strings.HasPrefix(first, "script ") || string(b) != strings.Repeat(first, 1000) {
		t.Fatalf("a mix of versions: %.40q", b)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("mode: %v %v", info, err)
	}
	if err := files.Replace(path, b, 0o750); err != nil {
		t.Fatal(err)
	}
	if again, err := os.Stat(path); err != nil || !os.SameFile(info, again) {
		t.Errorf("the same content was written again: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
