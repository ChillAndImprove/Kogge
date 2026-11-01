// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The Alpine Linux packages of an image, from the file system of the image's final stage: apk's database of
// installed packages (lib/apk/db/installed), the packages installed on purpose (etc/apk/world) and the release
// (etc/os-release, or usr/lib/os-release, which Alpine's /etc/os-release links to).

type apkPackage struct {
	name, version, arch, licence, origin, url, checksum, commit string
	depends, provides                                           []string
}

type osRelease struct {
	id, version, name string
}

// apkPackages returns the operating system and its packages as components, the bom-refs of the operating
// system and of the packages installed on purpose, and the dependencies between the packages.
func apkPackages(root string) ([]component, []string, []dependency, error) {
	release, err := readOSRelease(filepath.Join(root, "etc/os-release"))
	if errors.Is(err, fs.ErrNotExist) {
		release, err = readOSRelease(filepath.Join(root, "usr/lib/os-release"))
	}
	if err != nil {
		return nil, nil, nil, err
	}
	pkgs, err := readInstalled(filepath.Join(root, "lib/apk/db/installed"))
	if err != nil {
		return nil, nil, nil, err
	}
	world, err := os.ReadFile(filepath.Join(root, "etc/apk/world"))
	if err != nil {
		return nil, nil, nil, err
	}
	distro := release.id + "-" + release.version
	osRef := "os:" + distro
	comps := []component{{Type: "operating-system", BOMRef: osRef, Name: release.id, Version: release.version,
		Description: release.name}}
	refs := map[string]string{} // package or provided name -> bom-ref
	for _, p := range pkgs {
		upstream := ""
		if p.origin != p.name {
			upstream = p.origin
		}
		ref := purl("apk", "alpine", p.name, p.version, "arch", p.arch, "distro", distro, "upstream", upstream)
		c := component{Type: "library", BOMRef: ref, Name: p.name, Version: p.version, PURL: ref,
			Licenses: licences(p.licence, "declared")}
		if p.url != "" {
			c.ExternalReferences = []reference{{Type: "website", URL: p.url}}
		}
		// The checksum identifies the package file: apk's Q1 prefix and the base64 of the SHA-1 of its control data.
		c.Properties = []property{{Name: "kogge:apk:checksum", Value: p.checksum}, {Name: "kogge:apk:origin", Value: p.origin}}
		if p.commit != "" {
			c.Properties = append(c.Properties, property{Name: "kogge:apk:aports-commit", Value: p.commit})
		}
		comps = append(comps, c)
		refs[p.name] = ref
	}
	for _, p := range pkgs {
		for _, name := range p.provides {
			if _, ok := refs[name]; !ok {
				refs[name] = refs[p.name]
			}
		}
	}
	slices.SortFunc(comps[1:], func(a, b component) int { return strings.Compare(a.BOMRef, b.BOMRef) })

	deps := []dependency{{Ref: osRef, DependsOn: []string{}}}
	for _, p := range pkgs {
		on := map[string]bool{}
		for _, d := range p.depends {
			if to, ok := refs[d]; ok && to != refs[p.name] {
				on[to] = true
			}
		}
		deps = append(deps, dependency{Ref: refs[p.name], DependsOn: sortedSet(on)})
	}
	slices.SortFunc(deps[1:], func(a, b dependency) int { return strings.Compare(a.Ref, b.Ref) })
	direct := []string{osRef}
	for _, name := range strings.Fields(string(world)) {
		if ref, ok := refs[name]; ok {
			direct = append(direct, ref)
		}
	}
	return comps, direct, deps, nil
}

// readInstalled reads apk's database: one block of "X:value" lines per package, separated by empty lines.
func readInstalled(path string) ([]apkPackage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pkgs []apkPackage
	var p apkPackage
	flush := func() error {
		if p.name == "" {
			return nil
		}
		if p.version == "" || p.arch == "" {
			return fmt.Errorf("%s: package %s has no version or architecture", path, p.name)
		}
		pkgs = append(pkgs, p)
		p = apkPackage{}
		return nil
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || len(key) != 1 {
			continue
		}
		switch key {
		case "P":
			p.name = value
		case "V":
			p.version = value
		case "A":
			p.arch = value
		case "L":
			p.licence = value
		case "o":
			p.origin = value
		case "U":
			p.url = value
		case "C":
			p.checksum = value
		case "c":
			p.commit = value
		case "D":
			// Dependencies with version constraints (name>=1.0); conflicts (!name) are none.
			for _, d := range strings.Fields(value) {
				if !strings.HasPrefix(d, "!") {
					p.depends = append(p.depends, d[:nameEnd(d)])
				}
			}
		case "p":
			// What the package provides besides itself: commands, shared libraries, files (name=version).
			for _, d := range strings.Fields(value) {
				p.provides = append(p.provides, d[:nameEnd(d)])
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%s lists no packages", path)
	}
	return pkgs, nil
}

// nameEnd is where a dependency's name ends and its version constraint begins.
func nameEnd(d string) int {
	if i := strings.IndexAny(d, "=<>~"); i > 0 {
		return i
	}
	return len(d)
}

func readOSRelease(path string) (osRelease, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return osRelease{}, err
	}
	var r osRelease
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "ID":
			r.id = value
		case "VERSION_ID":
			r.version = value
		case "PRETTY_NAME":
			r.name = value
		}
	}
	if r.id == "" || r.version == "" {
		return osRelease{}, fmt.Errorf("%s names no ID and VERSION_ID", path)
	}
	return r, nil
}
