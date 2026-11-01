// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"kogge.de/kogge/internal/builds/protocol"
)

// jobLog collects the output of a job, masks secret values and sends it to the server in chunks. Complete lines
// are masked; an unfinished line is held back briefly so that a secret is not split between two chunks.
type jobLog struct {
	job  *JobClient
	mask []string

	mu      sync.Mutex
	partial []byte // the unfinished last line
	pending []byte // masked, not sent yet
	offset  int64  // size of the log on the server
	broken  bool   // the server no longer accepts the log
	kick    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
}

const (
	logFlushEvery = 500 * time.Millisecond
	logHoldLine   = 8 << 10 // longer unfinished lines are sent anyway
	logKickSize   = 64 << 10
)

func newJobLog(job *JobClient, offset int64, secrets []string) *jobLog {
	l := &jobLog{job: job, mask: maskList(secrets), offset: offset, kick: make(chan struct{}, 1), stop: make(chan struct{}),
		stopped: make(chan struct{})}
	go l.loop()
	return l
}

// maskList returns the values to mask, longest first: the secrets and their encoded forms (protocol.MaskForms);
// secrets with several lines are masked line by line, too.
func maskList(secrets []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		for _, f := range protocol.MaskForms(strings.TrimSpace(s)) {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	for _, s := range secrets {
		add(s)
		if strings.Contains(s, "\n") {
			for _, line := range strings.Split(s, "\n") {
				if len(strings.TrimSpace(line)) >= 8 {
					add(line)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func (l *jobLog) masked(b []byte) []byte {
	for _, s := range l.mask {
		if bytes.Contains(b, []byte(s)) {
			b = bytes.ReplaceAll(b, []byte(s), []byte("****"))
		}
	}
	return b
}

// Write implements io.Writer for the output of steps.
func (l *jobLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.partial = append(l.partial, p...)
	if i := bytes.LastIndexByte(l.partial, '\n'); i >= 0 {
		l.pending = append(l.pending, l.masked(append([]byte(nil), l.partial[:i+1]...))...)
		l.partial = append(l.partial[:0], l.partial[i+1:]...)
	}
	if len(l.partial) > logHoldLine {
		// A long line goes out unfinished, except its end: a secret that is not complete yet can only be there
		// (masking already replaced the complete ones).
		m := l.masked(l.partial)
		keep := 0
		if len(l.mask) > 0 {
			keep = min(len(l.mask[0])-1, len(m))
		}
		l.pending = append(l.pending, m[:len(m)-keep]...)
		l.partial = append(l.partial[:0], m[len(m)-keep:]...)
	}
	big := len(l.pending) >= logKickSize
	l.mu.Unlock()
	if big {
		select {
		case l.kick <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

// Step writes a step header.
func (l *jobLog) Step(name string) { l.line(protocol.MarkStep, name) }

// Info writes a message of the agent.
func (l *jobLog) Info(format string, args ...any) {
	l.line(protocol.MarkInfo, fmt.Sprintf(format, args...))
}

// Error writes an error of the agent.
func (l *jobLog) Error(format string, args ...any) {
	l.line(protocol.MarkError, fmt.Sprintf(format, args...))
}

func (l *jobLog) line(mark, text string) {
	l.mu.Lock()
	if len(l.partial) > 0 { // a step's last line without newline
		l.pending = append(l.pending, l.masked(append(l.partial, '\n'))...)
		l.partial = l.partial[:0]
	}
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		l.pending = append(l.pending, l.masked([]byte(mark+line+"\n"))...)
	}
	l.mu.Unlock()
}

func (l *jobLog) loop() {
	defer close(l.stopped)
	t := time.NewTicker(logFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
		case <-l.kick:
		}
		l.flush(context.Background(), false)
	}
}

// flush sends what is pending; with final, also the unfinished line, and it retries harder.
func (l *jobLog) flush(ctx context.Context, final bool) {
	l.mu.Lock()
	if final && len(l.partial) > 0 {
		l.pending = append(l.pending, l.masked(l.partial)...)
		l.partial = l.partial[:0]
	}
	l.mu.Unlock()
	for {
		l.mu.Lock()
		if l.broken || len(l.pending) == 0 {
			l.mu.Unlock()
			return
		}
		chunk := l.pending[:min(len(l.pending), protocol.MaxLogChunk)]
		offset := l.offset
		l.mu.Unlock()

		attempts := 3
		if final {
			attempts = 8
		}
		var size int64
		err := retry(ctx, attempts, func() error {
			var err error
			size, err = l.job.Log(ctx, offset, chunk)
			if errors.Is(err, errOffset) {
				return nil
			}
			return err
		})
		l.mu.Lock()
		switch {
		case err != nil && permanent(err):
			l.broken = true // the job ended on the server
		case err != nil:
			l.mu.Unlock()
			return // try again later
		default:
			// The chunk is done when the server's size did not stay behind the offset: it may store less (masking)
			// or nothing (size limit), and a mismatch with a larger size means an earlier try was stored. A smaller
			// size means the server lost data: send again from there.
			if size >= offset {
				l.pending = l.pending[len(chunk):]
			}
			l.offset = size
		}
		l.mu.Unlock()
	}
}

// Close sends the rest of the log.
func (l *jobLog) Close() {
	close(l.stop)
	<-l.stopped
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l.flush(ctx, true)
}
