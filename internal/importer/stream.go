// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// Listings of big instances do not fit into memory: a space with 50,000 pages holds gigabytes of page bodies.
// The extraction stores a listing in chunks (one file per page of the source's answers) and the readers below
// go through them element by element; what a load needs to keep is the place of an element, to read it again
// when its turn comes.

// itemAt is the place of an element of a stored JSON array.
type itemAt struct {
	file   string
	offset int64
	length int64
}

// listingFiles returns the files of a stored listing: the chunks of the directory parts (in order), or the
// single file parts + ".json" of earlier extractions.
func (w Workdir) listingFiles(parts ...string) []string {
	dir := w.Path(parts...)
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		var files []string
		for _, name := range w.List(parts...) {
			if strings.HasSuffix(name, ".json") {
				files = append(files, dir+string(os.PathSeparator)+name)
			}
		}
		slices.Sort(files)
		return files
	}
	last := len(parts) - 1
	single := append(slices.Clone(parts[:last]), parts[last]+".json")
	if w.Exists(single...) {
		return []string{w.Path(single...)}
	}
	return nil
}

// eachItem calls fn with every element of the JSON arrays in the files, in order. A file that holds null or
// is empty has no elements.
func eachItem(files []string, fn func(raw json.RawMessage, at itemAt) error) error {
	for _, file := range files {
		if err := eachItemOf(file, fn); err != nil {
			return err
		}
	}
	return nil
}

func eachItemOf(file string, fn func(raw json.RawMessage, at itemAt) error) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	tok, err := dec.Token()
	if errors.Is(err, io.EOF) || tok == nil {
		return nil // empty or null
	}
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("%s: not a JSON array", file)
	}
	for dec.More() {
		start := dec.InputOffset()
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if err := fn(raw, itemAt{file: file, offset: start, length: dec.InputOffset() - start}); err != nil {
			return err
		}
	}
	return nil
}

// readItem reads an element again.
func readItem(at itemAt) (json.RawMessage, error) {
	f, err := os.Open(at.file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, at.length)
	if _, err := f.ReadAt(b, at.offset); err != nil {
		return nil, fmt.Errorf("%s: %w", at.file, err)
	}
	// The place starts after the token before the element: the opening bracket or a comma.
	b = bytes.TrimLeft(b, " \t\r\n,[")
	if !json.Valid(b) {
		return nil, fmt.Errorf("%s: no JSON element at %d", at.file, at.offset)
	}
	return b, nil
}

// countItems counts the elements of a stored listing.
func countItems(files []string) (int, error) {
	n := 0
	err := eachItem(files, func(json.RawMessage, itemAt) error {
		n++
		return nil
	})
	return n, err
}
