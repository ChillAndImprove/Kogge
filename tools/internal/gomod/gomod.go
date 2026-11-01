// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package gomod finds the Go modules that are compiled into Kogge's programs and recognises their licences. The
// licence check and the third-party notices (tools/golicenses) and the SBOM (tools/sbom) use it, so that both
// describe the same modules with the same licences.
package gomod

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// MainModule is Kogge's own module, which is not a third-party module.
const MainModule = "kogge.de/kogge"

// Build is one compilation: packages with build tags for a platform.
type Build struct {
	Packages []string
	Tags     []string
	// Platform is GOOS/GOARCH, e.g. linux/arm64; empty means the host's.
	Platform string
}

// ServerPlatforms are the platforms of the server binaries and images of a release; AgentPlatforms those of
// the build agent's binaries, which the server offers for download (AGENT_PLATFORMS in the Makefile and in
// deploy/Dockerfile).
var (
	ServerPlatforms = []string{"linux/amd64", "linux/arm64"}
	AgentPlatforms  = []string{"linux/amd64", "linux/arm64", "windows/amd64", "darwin/amd64", "darwin/arm64"}
)

// Shipped returns the builds of the server (with the embedded web application) for the server platforms and of
// the build agent for the agent platforms.
func Shipped(server, agent []string) []Build {
	var builds []Build
	for _, p := range server {
		builds = append(builds, Build{Packages: []string{"./cmd/kogge"}, Tags: []string{"webui"}, Platform: p})
	}
	for _, p := range agent {
		builds = append(builds, Build{Packages: []string{"./cmd/kogge-agent"}, Platform: p})
	}
	return builds
}

// Module is a third-party module compiled into at least one of the builds.
type Module struct {
	Path    string
	Version string
	// Dir is the module's directory in the module cache.
	Dir string
	// Sum is the module's checksum as in go.sum (h1:…).
	Sum string
	// Licence is the SPDX identifier of the licence (LGPL, GPL and AGPL without their version), empty if it was
	// not recognised; LicenceFile is the file it was recognised in.
	Licence, LicenceFile string
	// Imports are the modules (MainModule and third-party ones) whose packages the module's packages import, and
	// Stdlib whether they import packages of the standard library.
	Imports []string
	Stdlib  bool
}

// Graph is the result of List: the third-party modules sorted by path, and the modules the main module's
// packages import directly.
type Graph struct {
	Modules []*Module
	// Direct are the modules that Kogge's own packages import.
	Direct []string
}

type listedPackage struct {
	ImportPath string
	Standard   bool
	Module     *listedModule
	Imports    []string
}

type listedModule struct {
	Path, Version, Dir, Sum string
	Main                    bool
	Replace                 *listedModule
}

// List returns the third-party modules compiled into the builds, with their licences. The go command runs in
// dir (the repository root).
func List(dir string, builds ...Build) (*Graph, error) {
	mods := map[string]*Module{}
	imports := map[string]map[string]bool{} // module path -> imported module paths
	stdlib := map[string]bool{}
	for _, b := range builds {
		pkgs, err := list(dir, b)
		if err != nil {
			return nil, err
		}
		owner := map[string]string{} // package import path -> module path ("" for the standard library)
		for _, p := range pkgs {
			switch {
			case p.Standard || p.Module == nil:
				owner[p.ImportPath] = ""
			case p.Module.Main:
				owner[p.ImportPath] = MainModule
			default:
				owner[p.ImportPath] = p.Module.Path
				if _, ok := mods[p.Module.Path]; !ok {
					// A replaced module keeps its path; its code, version and checksum are the replacement's.
					m := p.Module
					if m.Replace != nil {
						m = m.Replace
					}
					mods[p.Module.Path] = &Module{Path: p.Module.Path, Version: m.Version, Dir: m.Dir, Sum: m.Sum}
				}
			}
		}
		for _, p := range pkgs {
			from, ok := owner[p.ImportPath]
			if !ok || from == "" {
				continue
			}
			for _, imp := range p.Imports {
				to, known := owner[imp]
				switch {
				case !known || to == from:
				case to == "":
					stdlib[from] = true
				default:
					if imports[from] == nil {
						imports[from] = map[string]bool{}
					}
					imports[from][to] = true
				}
			}
		}
	}
	g := &Graph{}
	for _, m := range mods {
		m.Licence, m.LicenceFile = Licence(m.Dir)
		m.Imports = sortedKeys(imports[m.Path])
		m.Stdlib = stdlib[m.Path]
		g.Modules = append(g.Modules, m)
	}
	slices.SortFunc(g.Modules, func(a, b *Module) int { return strings.Compare(a.Path, b.Path) })
	g.Direct = sortedKeys(imports[MainModule])
	return g, nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// list runs go list for a build. With -e, the modules matter, not whether the packages compile: with the tag
// webui, internal/webui embeds the built web application, which a fresh checkout does not have yet.
func list(dir string, b Build) ([]listedPackage, error) {
	args := []string{"list", "-e", "-deps", "-json=ImportPath,Standard,Module,Imports"}
	if len(b.Tags) > 0 {
		args = append(args, "-tags", strings.Join(b.Tags, ","))
	}
	cmd := exec.Command("go", append(args, b.Packages...)...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if b.Platform != "" {
		goos, goarch, ok := strings.Cut(b.Platform, "/")
		if !ok {
			return nil, fmt.Errorf("platform %q is not GOOS/GOARCH", b.Platform)
		}
		cmd.Env = append(cmd.Env, "GOOS="+goos, "GOARCH="+goarch)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %w\n%s", strings.Join(b.Packages, " "), err, stderr.Bytes())
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			return pkgs, nil
		} else if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		pkgs = append(pkgs, p)
	}
}

// Allowed are the licences Kogge allows for libraries: permissive ones, MPL-2.0 and LGPL.
var Allowed = map[string]bool{
	"MIT": true, "Apache-2.0": true, "BSD-2-Clause": true, "BSD-3-Clause": true, "ISC": true,
	"MPL-2.0": true, "LGPL": true, "Unlicense": true, "CC0-1.0": true, "0BSD": true, "Zlib": true,
}

// IsAllowed reports whether a licence from Classify is allowed: all parts of "A AND B" must be.
func IsAllowed(licence string) bool {
	for _, part := range strings.Split(licence, " AND ") {
		if !Allowed[part] {
			return false
		}
	}
	return true
}

// Classify recognises a licence by its text. A text with the Apache License 2.0 and the MIT licence's grant
// covers code under both (e.g. go.yaml.in/yaml/v3, whose C parts came from libyaml under MIT).
func Classify(text string) string {
	t := strings.ToLower(strings.Join(strings.Fields(text), " "))
	// The title decides: other licence texts mention the GNU licences (MPL-2.0 names them as secondary licences).
	head := t
	if len(head) > 300 {
		head = head[:300]
	}
	apache := strings.Contains(t, "apache license") && strings.Contains(t, "version 2.0")
	mit := strings.Contains(t, "permission is hereby granted, free of charge")
	switch {
	case strings.Contains(head, "mozilla public license") && strings.Contains(head, "2.0"):
		return "MPL-2.0"
	case strings.Contains(head, "gnu affero general public license"):
		return "AGPL"
	case strings.Contains(head, "gnu lesser general public license") || strings.Contains(head, "gnu library general public license"):
		return "LGPL"
	case strings.Contains(head, "gnu general public license"):
		return "GPL"
	case apache && mit:
		return "Apache-2.0 AND MIT"
	case apache:
		return "Apache-2.0"
	case mit:
		return "MIT"
	case strings.Contains(t, "redistribution and use in source and binary forms"):
		if strings.Contains(t, "neither the name") || strings.Contains(t, "names of its contributors") {
			return "BSD-3-Clause"
		}
		return "BSD-2-Clause"
	case strings.Contains(t, "permission to use, copy, modify, and/or distribute this software"),
		strings.Contains(t, "permission to use, copy, modify, and distribute this software"):
		return "ISC"
	case strings.Contains(t, "this is free and unencumbered software released into the public domain"):
		return "Unlicense"
	case strings.Contains(t, "creative commons legal code") && strings.Contains(t, "cc0"):
		return "CC0-1.0"
	}
	return ""
}

// Licence recognises the licence of the module in dir from its licence file. Some modules keep the licence one
// level up (e.g. sub-modules of a repository). It returns the licence and the path of the file.
func Licence(dir string) (string, string) {
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if licence, file := licenceFile(d); licence != "" {
			return licence, filepath.Join(d, file)
		}
	}
	return "", ""
}

func licenceFile(dir string) (string, string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		n := strings.ToUpper(e.Name())
		if e.IsDir() || !(strings.HasPrefix(n, "LICENSE") || strings.HasPrefix(n, "LICENCE") || strings.HasPrefix(n, "COPYING")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if l := Classify(string(b)); l != "" {
			return l, e.Name()
		}
	}
	return "", ""
}
