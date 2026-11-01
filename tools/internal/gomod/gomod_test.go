// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package gomod

import (
	"slices"
	"testing"
)

const (
	mitText    = "Permission is hereby granted, free of charge, to any person obtaining a copy of this software"
	apacheText = "Apache License\nVersion 2.0, January 2004\nhttp://www.apache.org/licenses/"
)

func TestClassify(t *testing.T) {
	for text, want := range map[string]string{
		"MIT License\n\nCopyright (c) 2020 X\n\n" + mitText: "MIT",
		apacheText: "Apache-2.0",
		"This project is covered by two licenses: MIT and Apache.\n" + mitText + "\n" + apacheText: "Apache-2.0 AND MIT",
		// MPL-2.0 names the GNU licences as secondary licences: its title decides.
		"Mozilla Public License Version 2.0\n... GNU General Public License ...": "MPL-2.0",
		"GNU LESSER GENERAL PUBLIC LICENSE Version 3":                            "LGPL",
		"GNU AFFERO GENERAL PUBLIC LICENSE":                                      "AGPL",
		"Redistribution and use in source and binary forms ... Neither the name": "BSD-3-Clause",
		"Redistribution and use in source and binary forms, with or without":     "BSD-2-Clause",
		"All rights reserved.": "",
	} {
		if got := Classify(text); got != want {
			t.Errorf("Classify(%.40q) = %q, want %q", text, got, want)
		}
	}
}

func TestIsAllowed(t *testing.T) {
	for licence, want := range map[string]bool{"MIT": true, "Apache-2.0 AND MIT": true, "MPL-2.0": true, "LGPL": true,
		"GPL": false, "AGPL": false, "": false, "MIT AND GPL": false} {
		if got := IsAllowed(licence); got != want {
			t.Errorf("IsAllowed(%q) = %v", licence, got)
		}
	}
}

// TestShipped: the server is built with the web application for its platforms, the agent for its own.
func TestShipped(t *testing.T) {
	builds := Shipped([]string{"linux/amd64"}, []string{"linux/arm64", "windows/amd64"})
	var got []string
	for _, b := range builds {
		got = append(got, b.Platform+" "+b.Packages[0]+" "+slices.Concat(b.Tags, []string{""})[0])
	}
	want := []string{"linux/amd64 ./cmd/kogge webui", "linux/arm64 ./cmd/kogge-agent ", "windows/amd64 ./cmd/kogge-agent "}
	if !slices.Equal(got, want) {
		t.Errorf("Shipped = %q", got)
	}
}
