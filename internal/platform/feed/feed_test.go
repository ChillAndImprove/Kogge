// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package feed

import (
	"slices"
	"testing"
	"time"

	"kogge.de/kogge/internal/platform/db"
)

func TestCursor(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 30, 0, 123456000, time.UTC)
	c := Cursor{At: at, Source: SourcePush, ID: 42}
	back, err := ParseCursor(c.String())
	if err != nil || !back.At.Equal(at) || back.Source != SourcePush || back.ID != 42 {
		t.Fatalf("round trip of %s: %+v %v", c, back, err)
	}
	for _, bad := range []string{"", "1-2", "a-2-3", "1-9-3", "1-0-3", "1-2-x", "-1-2-3", "1-2-3-4"} {
		if _, err := ParseCursor(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAfter(t *testing.T) {
	var none *Cursor
	var args db.Args
	if none.After(SourceRun, "r.finished_at", "r.id", &args) != "TRUE" || len(args) != 0 {
		t.Error("no cursor")
	}
	c := &Cursor{At: time.Now(), Source: SourcePullRequest, ID: 7}
	cases := map[int]string{
		SourceActivity:    "a.at <= $1",
		SourcePullRequest: "(a.at, a.id) < ($1, $2)",
		SourceRun:         "a.at < $1",
	}
	for source, want := range cases {
		var args db.Args
		if got := c.After(source, "a.at", "a.id", &args); got != want {
			t.Errorf("source %d: %s, want %s", source, got, want)
		}
	}
}

func TestMerge(t *testing.T) {
	base := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	e := func(minute, source int, id int64) Entry {
		return Entry{At: base.Add(time.Duration(minute) * time.Minute), Source: source, ID: id}
	}
	issues := []Entry{e(9, SourceActivity, 30), e(5, SourceActivity, 20), e(5, SourceActivity, 19)}
	pushes := []Entry{e(8, SourcePush, 3), e(5, SourcePush, 2)}
	runs := []Entry{e(7, SourceRun, 11)}
	got, more := Merge([][]Entry{issues, pushes, runs}, 4)
	var order []int64
	for _, x := range got {
		order = append(order, x.ID)
	}
	// At the same time, the later source comes first.
	if want := []int64{30, 3, 11, 2}; !slices.Equal(order, want) || !more {
		t.Fatalf("merged %v (more %v), want %v", order, more, want)
	}
	// The next page starts after the last entry: each source's rows after its key.
	c, err := ParseCursor(got[3].Key)
	if err != nil || c != CursorOf(got[3]) {
		t.Fatalf("key %q: %v", got[3].Key, err)
	}
	rest, more := Merge([][]Entry{issues[1:], nil, nil}, 4)
	if len(rest) != 2 || more {
		t.Errorf("rest %v %v", rest, more)
	}
}
