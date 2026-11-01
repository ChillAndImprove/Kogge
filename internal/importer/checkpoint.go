// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sync"
	"time"
)

// An extraction of a big instance takes hours and may be interrupted (a network failure, a restart of the
// source, Ctrl-C). Its checkpoint lets the next run resume where it stopped: extraction.json in the product's
// directory describes the run, extraction.log lists the parts that are complete (one JSON line each, appended
// as they finish). A run that finished leaves the state finished; the next run then starts afresh — a delta
// extraction before the cut-over must read everything again — but keeps the files that do not change, such as
// attachments already downloaded completely.

// ExtractionState describes the latest extraction of a product in a work directory.
type ExtractionState struct {
	Started  time.Time      `json:"started"`
	Finished *time.Time     `json:"finished,omitempty"`
	Resumed  []time.Time    `json:"resumed,omitempty"`
	Options  map[string]any `json:"options,omitempty" doc:"Options that change what the parts hold, e.g. whether attachment files were extracted"`
}

// ExtractionNote is something an extraction could not read, or found inconsistent; it went on without it.
type ExtractionNote struct {
	Kind string `json:"kind" doc:"attachment, workflow, listing, …"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Note string `json:"note"`
}

type checkpoint struct {
	w       Workdir
	product string
	state   ExtractionState
	done    map[string]string
	mu      sync.Mutex
	log     *os.File
	notes   *os.File
}

// readExtractionNotes returns the notes of the latest extraction of a product.
func readExtractionNotes(w Workdir, product string) ([]ExtractionNote, error) {
	f, err := os.Open(w.Path(product, "extraction-notes.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []ExtractionNote
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var n ExtractionNote
		if json.Unmarshal(sc.Bytes(), &n) == nil && n.Kind != "" {
			out = append(out, n)
		}
	}
	return out, sc.Err()
}

// note records something the extraction could not read (it is shown by the analysis).
func (c *checkpoint) note(n ExtractionNote) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := json.Marshal(n)
	_, err := c.notes.Write(append(b, '\n'))
	return err
}

type checkpointLine struct {
	Key   string `json:"k"`
	Value string `json:"v,omitempty"`
}

// readExtractionState returns the state of the latest extraction of a product, if any.
func readExtractionState(w Workdir, product string) (*ExtractionState, error) {
	var st ExtractionState
	ok, err := w.Read(&st, product, "extraction.json")
	if err != nil || !ok {
		return nil, err
	}
	return &st, nil
}

// openCheckpoint resumes an unfinished extraction with the same options, or begins a new one (restart forces
// that). options are what changes the content of the parts (e.g. whether attachment files are extracted).
func openCheckpoint(w Workdir, product string, restart bool, options map[string]any, log *slog.Logger) (*checkpoint, error) {
	c := &checkpoint{w: w, product: product, done: map[string]string{}}
	prev, err := readExtractionState(w, product)
	if err != nil {
		return nil, err
	}
	logPath, notesPath := w.Path(product, "extraction.log"), w.Path(product, "extraction-notes.log")
	switch {
	case prev != nil && prev.Finished == nil && !restart && sameOptions(prev.Options, options):
		c.state = *prev
		c.state.Resumed = append(c.state.Resumed, time.Now())
		if err := c.readLog(logPath); err != nil {
			return nil, err
		}
		if log != nil {
			log.Info(fmt.Sprintf("resuming the extraction started at %s: %d parts are complete", prev.Started.Format(time.RFC3339),
				len(c.done)))
		}
	default:
		if prev != nil && prev.Finished == nil && log != nil {
			reason := "on request"
			if !restart {
				reason = "with other options"
			}
			log.Info("the unfinished extraction of " + prev.Started.Format(time.RFC3339) + " is started again " + reason)
		}
		c.state = ExtractionState{Started: time.Now(), Options: options}
		for _, p := range []string{logPath, notesPath} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	if err := c.saveState(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	c.log = f
	if c.notes, err = os.OpenFile(notesPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640); err != nil {
		f.Close()
		return nil, err
	}
	return c, nil
}

func sameOptions(a, b map[string]any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb) || (len(a) == 0 && len(b) == 0)
}

func (c *checkpoint) readLog(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var l checkpointLine
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.Key != "" {
			c.done[l.Key] = l.Value // a line cut off by a crash is skipped: its part is done again
		}
	}
	return sc.Err()
}

func (c *checkpoint) saveState() error {
	b, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return err
	}
	return c.w.Write(b, c.product, "extraction.json")
}

// resumed reports whether this run continues an interrupted one.
func (c *checkpoint) resumed() bool { return c != nil && len(c.state.Resumed) > 0 }

// isDone reports whether a part is complete in this run, with the value it was marked with.
func (c *checkpoint) isDone(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.done[key]
	return v, ok
}

// markDone records a complete part.
func (c *checkpoint) markDone(key, value string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := json.Marshal(checkpointLine{Key: key, Value: value})
	if _, err := c.log.Write(append(b, '\n')); err != nil {
		return err
	}
	c.done[key] = value
	return nil
}

// part runs fn unless the part is complete, and marks it complete when fn succeeds.
func (c *checkpoint) part(key string, fn func() error) error {
	if _, ok := c.isDone(key); ok {
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	return c.markDone(key, "")
}

// doneKeys returns a copy of the complete parts.
func (c *checkpoint) doneKeys() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.done)
}

// finish marks the extraction as complete.
func (c *checkpoint) finish() error {
	now := time.Now()
	c.state.Finished = &now
	if err := c.saveState(); err != nil {
		return err
	}
	return c.close()
}

func (c *checkpoint) close() error {
	if c == nil || c.log == nil {
		return nil
	}
	err := c.log.Close()
	if nerr := c.notes.Close(); err == nil {
		err = nerr
	}
	c.log, c.notes = nil, nil
	return err
}
