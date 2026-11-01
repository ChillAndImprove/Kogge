// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The npm packages of the web application are those that a production install of web/package-lock.json contains
// (npm ci --omit=dev): every package the lock file does not mark as needed for development only, except
// platform-specific optional ones (native programs of build tools, never part of the web application) and those
// the web build leaves out. The bundle the browser loads contains a part of them. tools/npm-licenses.mjs lists
// the same packages in the third-party notices; notShipped and knownLicences are its NOT_SHIPPED and KNOWN.

// notShipped are packages that are installed with others but kept out of the web build (vite.config.ts fails
// the build if one gets in).
var notShipped = map[string]bool{"elkjs": true}

// knownLicences are the licences of packages whose manifest does not name their whole licence, as their licence
// files state them: khroma's names none; katex's names MIT, which its fonts are not (the SIL Open Font License).
var knownLicences = map[string]string{"katex@0.19.0": "MIT AND OFL-1.1", "khroma@2.1.0": "MIT"}

type lockFile struct {
	LockfileVersion int                  `json:"lockfileVersion"`
	Packages        map[string]lockEntry `json:"packages"`
}

type lockEntry struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Resolved             string            `json:"resolved"`
	Integrity            string            `json:"integrity"`
	License              json.RawMessage   `json:"license"`
	Dev                  bool              `json:"dev"`
	Optional             bool              `json:"optional"`
	Link                 bool              `json:"link"`
	OS                   []string          `json:"os"`
	CPU                  []string          `json:"cpu"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

// npmPackages returns the shipped packages of the lock file in dir as components, the bom-refs of the web
// application's direct dependencies, and the dependencies between the packages.
func npmPackages(dir string) ([]component, []string, []dependency, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return nil, nil, nil, err
	}
	var lock lockFile
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, nil, nil, fmt.Errorf("package-lock.json: %w", err)
	}
	if lock.LockfileVersion < 2 {
		return nil, nil, nil, fmt.Errorf("package-lock.json has version %d; npm 7 or later writes version 3", lock.LockfileVersion)
	}
	refs := map[string]string{} // path in the lock file -> bom-ref
	var comps []component
	seen := map[string]bool{}
	for path, e := range lock.Packages {
		// The web application itself is "", workspaces have other paths.
		if !strings.HasPrefix(path, "node_modules/") {
			continue
		}
		name := e.Name
		if name == "" {
			name = path[strings.LastIndex(path, "node_modules/")+len("node_modules/"):]
		}
		if !shipped(name, e) {
			continue
		}
		c, err := npmComponent(name, e)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		refs[path] = c.BOMRef
		// The same version can be installed at several places.
		if !seen[c.BOMRef] {
			seen[c.BOMRef] = true
			comps = append(comps, c)
		}
	}
	slices.SortFunc(comps, func(a, b component) int { return strings.Compare(a.BOMRef, b.BOMRef) })

	edges := map[string]map[string]bool{}
	for path, ref := range refs {
		for _, name := range dependencyNames(lock.Packages[path]) {
			if to, ok := resolve(lock.Packages, refs, path, name); ok && to != ref {
				if edges[ref] == nil {
					edges[ref] = map[string]bool{}
				}
				edges[ref][to] = true
			}
		}
	}
	var deps []dependency
	for _, c := range comps {
		deps = append(deps, dependency{Ref: c.BOMRef, DependsOn: sortedSet(edges[c.BOMRef])})
	}
	var direct []string
	for _, name := range dependencyNames(lock.Packages[""]) {
		if to, ok := resolve(lock.Packages, refs, "", name); ok {
			direct = append(direct, to)
		}
	}
	slices.Sort(direct)
	return comps, slices.Compact(direct), deps, nil
}

func shipped(name string, e lockEntry) bool {
	return !e.Dev && !e.Link && !notShipped[name] && !(e.Optional && (len(e.OS) > 0 || len(e.CPU) > 0))
}

func dependencyNames(e lockEntry) []string {
	var names []string
	for _, m := range []map[string]string{e.Dependencies, e.OptionalDependencies, e.PeerDependencies} {
		for name := range m {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// resolve finds the package that a package at path gets for require(name), as Node.js does: in the
// node_modules directory of the package, then in those of the directories above it.
func resolve(packages map[string]lockEntry, refs map[string]string, path, name string) (string, bool) {
	dir := path
	for {
		candidate := "node_modules/" + name
		if dir != "" {
			candidate = dir + "/" + candidate
		}
		if _, ok := packages[candidate]; ok {
			ref, shipped := refs[candidate]
			return ref, shipped
		}
		if dir == "" {
			return "", false
		}
		dir = strings.TrimSuffix(dir[:strings.LastIndex(dir, "node_modules/")], "/")
	}
}

func npmComponent(name string, e lockEntry) (component, error) {
	group, local := "", name
	if strings.HasPrefix(name, "@") {
		var ok bool
		if group, local, ok = strings.Cut(name, "/"); !ok {
			return component{}, fmt.Errorf("package name %q", name)
		}
	}
	p := purl("npm", group, local, e.Version)
	c := component{Type: "library", BOMRef: p, Group: group, Name: local, Version: e.Version, PURL: p}
	lic := knownLicences[name+"@"+e.Version]
	if lic == "" {
		var err error
		if lic, err = npmLicence(e.License); err != nil {
			return component{}, err
		}
	}
	c.Licenses = licences(lic, "declared")
	for _, sri := range strings.Fields(e.Integrity) {
		h, err := integrityHash(sri)
		if err != nil {
			return component{}, err
		}
		c.Hashes = append(c.Hashes, h)
	}
	if e.Resolved != "" {
		c.ExternalReferences = []reference{{Type: "distribution", URL: e.Resolved}}
	}
	return c, nil
}

// npmLicence reads the licence of a lock file entry: an SPDX expression, or the type of an old-style licence
// object.
func npmLicence(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var o struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return "", fmt.Errorf("licence %s: %w", raw, err)
	}
	return o.Type, nil
}

// integrityHash turns a subresource integrity value (sha512-BASE64) into a CycloneDX hash.
func integrityHash(sri string) (hash, error) {
	algs := map[string]string{"sha1": "SHA-1", "sha256": "SHA-256", "sha384": "SHA-384", "sha512": "SHA-512"}
	name, value, ok := strings.Cut(sri, "-")
	alg, known := algs[name]
	if !ok || !known {
		return hash{}, fmt.Errorf("integrity %q", sri)
	}
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return hash{}, fmt.Errorf("integrity %q: %w", sri, err)
	}
	return hash{Alg: alg, Content: hex.EncodeToString(b)}, nil
}

func sortedSet(m map[string]bool) []string {
	list := []string{}
	for k := range m {
		list = append(list, k)
	}
	slices.Sort(list)
	return list
}
