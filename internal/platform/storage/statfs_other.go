// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

//go:build !unix

package storage

// statfs cannot measure file systems here (the server is built for Linux; the packages still build elsewhere):
// nothing is ever refused for want of space.
func statfs(string) (free, total uint64) { return 0, 0 }
