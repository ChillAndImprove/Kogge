// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

//go:build !webui

package webui

import "io/fs"

func embedded() fs.FS { return nil }
