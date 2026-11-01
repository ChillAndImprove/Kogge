// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Command golicenses checks the licences of the Go modules compiled into Kogge: permissive
// licences, MPL-2.0 and LGPL are allowed; GPL, AGPL and modules without a recognisable licence fail. With
// -o it writes the third-party notice file.
//
// It checks the modules of every package of the repository on this platform and those of the binaries Kogge
// ships for its platforms (tools/internal/gomod): some modules are compiled in for one operating system only.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kogge.de/kogge/tools/internal/gomod"
)

func main() {
	out := flag.String("o", "", "write a third-party notice file")
	flag.Parse()
	builds := append([]gomod.Build{{Packages: []string{"./..."}, Tags: []string{"webui"}}},
		gomod.Shipped(gomod.ServerPlatforms, gomod.AgentPlatforms)...)
	g, err := gomod.List(".", builds...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	failed := false
	counts := map[string]int{}
	for _, m := range g.Modules {
		if !gomod.IsAllowed(m.Licence) {
			fmt.Fprintf(os.Stderr, "%s %s: licence %q is not allowed\n", m.Path, m.Version, m.Licence)
			failed = true
		}
		counts[m.Licence]++
	}
	fmt.Printf("%d Go modules: %v\n", len(g.Modules), counts)
	if *out != "" {
		var b strings.Builder
		b.WriteString("# Third-party licences\n\nKogge includes the following Go modules and, in its web application, the npm " +
			"packages listed further below. Their licences and notices follow their lists.\n\n## Go modules\n\n")
		b.WriteString("| Module | Version | Licence |\n|---|---|---|\n")
		for _, m := range g.Modules {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", m.Path, m.Version, m.Licence)
		}
		b.WriteString("\n### Licence texts of the Go modules\n")
		for _, m := range g.Modules {
			text, err := os.ReadFile(m.LicenceFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s %s: %v\n", m.Path, m.Version, err)
				os.Exit(2)
			}
			fmt.Fprintf(&b, "\n#### %s %s\n\n```text\n%s\n```\n", m.Path, m.Version, strings.TrimSpace(string(text)))
		}
		// The notice file may go to a directory that does not exist yet (e.g. /out in the image build).
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if failed {
		os.Exit(1)
	}
}
