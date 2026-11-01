// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

//go:build webui

package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func embedded() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}
