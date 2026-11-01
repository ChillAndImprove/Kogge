// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package storage keeps the data volume from filling up: what adds files there (pushes, uploads, artifacts, LFS
// objects, attachments) is refused while the file system has less free space than the administrators keep free
// (Administration → Settings → Limits). A full disk would take every product down, and PostgreSQL with it if it
// shares the volume.
package storage

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/settings"
)

// Usage is the state of the file system of a directory.
type Usage struct {
	Free    uint64 `json:"free" doc:"Bytes available to Kogge"`
	Total   uint64 `json:"total" doc:"Size of the file system in bytes"`
	Reserve uint64 `json:"reserve" doc:"Free space the settings keep: below it, pushes and uploads are refused"`
}

// Low reports whether the free space is below the reserve. A file system whose size is unknown is never low.
func (u Usage) Low() bool { return u.Total > 0 && u.Free < u.Reserve }

// space measures the file system of a directory; tests replace it (Fake).
var space atomic.Pointer[func(dir string) (free, total uint64)]

// Space returns the bytes available to unprivileged users and the size of the file system of dir (0, 0 if it cannot
// be measured).
func Space(dir string) (free, total uint64) {
	if f := space.Load(); f != nil {
		return (*f)(dir)
	}
	return statfs(dir)
}

// Fake makes Space report free and total bytes for every directory until the returned function restores what it
// reported before: API tests do not depend on the disk of the machine they run on, and tests of a full disk make
// one.
func Fake(free, total uint64) (restore func()) {
	f := func(string) (uint64, uint64) { return free, total }
	before := space.Swap(&f)
	return func() { space.Store(before) }
}

// Reserve returns the free space the settings keep on a file system of total bytes: the larger of the minimum in MiB
// and the share of the size.
func Reserve(st settings.Instance, total uint64) uint64 {
	reserve := uint64(max(st.MinFreeSpaceMB, 0)) << 20
	if share := total / 100 * uint64(max(st.MinFreeSpacePercent, 0)); share > reserve {
		reserve = share
	}
	return reserve
}

// Of returns the usage of the file system of dir under the settings.
func Of(st settings.Instance, dir string) Usage {
	free, total := Space(dir)
	return Usage{Free: free, Total: total, Reserve: Reserve(st, total)}
}

// Check refuses (507, storage.low) to add n more bytes (0 when unknown) to dir when that leaves less free space than
// the settings keep.
func Check(st settings.Instance, dir string, n int64) error {
	u := Of(st, dir)
	if u.Total == 0 || u.Reserve == 0 {
		return nil
	}
	if u.Free < u.Reserve || uint64(max(n, 0)) > u.Free-u.Reserve {
		return LowError()
	}
	return nil
}

// LowError is the error of additions refused for want of space.
func LowError() *apperr.Error {
	return apperr.New(http.StatusInsufficientStorage, "storage.low",
		"The server is running out of disk space, so it accepts no more files for now. Please tell an administrator.")
}

// Format writes a size in bytes for people: 1.5 GiB, 300 MiB, 12 KiB, 7 bytes.
func Format(n int64) string {
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	if n < 1024 && n > -1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	v, unit := float64(n)/1024, units[0]
	for _, u := range units[1:] {
		if v < 1024 && v > -1024 {
			break
		}
		v, unit = v/1024, u
	}
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d %s", int64(v), unit)
	}
	return fmt.Sprintf("%.1f %s", v, unit)
}
