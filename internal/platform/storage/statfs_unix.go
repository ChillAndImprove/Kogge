// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

//go:build unix

package storage

import "syscall"

// statfs measures the file system of dir: Bavail counts the blocks unprivileged processes may use (the blocks
// reserved for root are not Kogge's).
func statfs(dir string) (free, total uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize)
}
