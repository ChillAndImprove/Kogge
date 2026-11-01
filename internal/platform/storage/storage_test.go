// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package storage_test

import (
	"runtime"
	"testing"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
)

func TestReserve(t *testing.T) {
	st := settings.Defaults() // 2048 MiB and 2 %
	for _, c := range []struct {
		total, want uint64
	}{
		{50 << 30, 2 << 30},            // a small disk: the minimum
		{1 << 40, (1 << 40) / 100 * 2}, // a large one: its share
		{0, 2 << 30},                   // unknown size: the minimum
	} {
		if got := storage.Reserve(st, c.total); got != c.want {
			t.Errorf("reserve of %d bytes = %d, want %d", c.total, got, c.want)
		}
	}
	st.MinFreeSpaceMB, st.MinFreeSpacePercent = 0, 0
	if got := storage.Reserve(st, 1<<40); got != 0 {
		t.Errorf("reserve switched off = %d", got)
	}
}

func TestCheck(t *testing.T) {
	st := settings.Defaults()
	dir := t.TempDir()
	defer storage.Fake(10<<30, 100<<30)() // 10 GiB free, 2 GiB kept free
	for _, c := range []struct {
		n   int64
		low bool
	}{{0, false}, {-1, false}, {7 << 30, false}, {8 << 30, false}, {8<<30 + 1, true}, {20 << 30, true}} {
		err := storage.Check(st, dir, c.n)
		if (err != nil) != c.low {
			t.Errorf("adding %d bytes: %v", c.n, err)
		}
		if err != nil && !apperr.Is(err, "storage.low") {
			t.Errorf("adding %d bytes: %v", c.n, err)
		}
	}
	u := storage.Of(st, dir)
	if u.Free != 10<<30 || u.Total != 100<<30 || u.Reserve != 2<<30 || u.Low() {
		t.Errorf("usage = %+v", u)
	}
	restore := storage.Fake(1<<30, 100<<30)
	if err := storage.Check(st, dir, 0); err == nil || !storage.Of(st, dir).Low() {
		t.Error("1 GiB free is not low")
	}
	// Without a reserve, or with a file system of unknown size, nothing is refused.
	st.MinFreeSpaceMB, st.MinFreeSpacePercent = 0, 0
	if err := storage.Check(st, dir, 5<<30); err != nil {
		t.Errorf("without a reserve: %v", err)
	}
	restore()
	restore = storage.Fake(0, 0)
	if err := storage.Check(settings.Defaults(), dir, 5<<30); err != nil || storage.Of(settings.Defaults(), dir).Low() {
		t.Errorf("unknown size: %v", err)
	}
	restore()
}

// TestSpace measures a real file system (the fakes of the other tests restored).
func TestSpace(t *testing.T) {
	free, total := storage.Space(t.TempDir())
	if runtime.GOOS == "windows" {
		return
	}
	if total == 0 || free > total {
		t.Errorf("free %d of %d", free, total)
	}
	if free, total := storage.Space("/does/not/exist"); free != 0 || total != 0 {
		t.Errorf("a missing directory: %d of %d", free, total)
	}
}

func TestFormat(t *testing.T) {
	for n, want := range map[int64]string{0: "0 bytes", 7: "7 bytes", 1024: "1 KiB", 1536: "1.5 KiB", 1 << 20: "1 MiB",
		300 << 20: "300 MiB", 2 << 30: "2 GiB", 5<<30 + 512<<20: "5.5 GiB", 3 << 40: "3 TiB", 5000 << 40: "5000 TiB"} {
		if got := storage.Format(n); got != want {
			t.Errorf("Format(%d) = %q, want %q", n, got, want)
		}
	}
}
