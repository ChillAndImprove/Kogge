// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Command sbom writes the software bill of materials (CycloneDX 1.6, JSON) of what Kogge ships:
//
//   - the Go modules compiled into kogge and kogge-agent for the platforms of a release, with the licence that
//     tools/golicenses recognises and the checksum of go.sum, and the Go standard library of the toolchain;
//   - the npm packages of the web application that kogge embeds: those of a production install of
//     web/package-lock.json, with their declared licence and the lock file's integrity hash;
//   - with -rootfs, the Alpine Linux packages of a container image.
//
// The image build (deploy/Dockerfile) writes it to /usr/share/doc/kogge/sbom.cdx.json in both images, `make sbom`
// to bin/sbom.cdx.json, and a release attaches it. Run it in the repository's root:
//
//	go run ./tools/sbom -version 1.2.0 -o bin/sbom.cdx.json
//	go run ./tools/sbom -name kogge-agent -server "" -web "" -agent linux/arm64 -rootfs /tmp/agent-root
//
// -merge combines SBOMs, e.g. those of an image's platforms into the SBOM of the multi-platform image:
//
//	go run ./tools/sbom -merge -o kogge-image.cdx.json linux-amd64.cdx.json linux-arm64.cdx.json
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"kogge.de/kogge/tools/internal/gomod"
)

// options say what the SBOM covers.
type options struct {
	// Name is the program the SBOM is about: kogge (with the agents for download and the web application) or
	// kogge-agent.
	Name, Version, Commit, Source string
	// Date is the timestamp of the SBOM: the build's date.
	Date time.Time
	// Server and Agent are the platforms of the binaries (GOOS/GOARCH); none leaves the program out.
	Server, Agent []string
	// Web is the directory of the web application's package-lock.json; empty leaves it out.
	Web string
	// RootFS is a directory with the Alpine package database of an image; empty leaves it out.
	RootFS string
	// Dir is the repository's root.
	Dir string
}

func main() {
	o := options{Dir: "."}
	out := flag.String("o", "", "write the SBOM to this file instead of standard output")
	flag.StringVar(&o.Name, "name", "kogge", "the program the SBOM describes: kogge or kogge-agent")
	flag.StringVar(&o.Version, "version", "dev", "Kogge's version")
	flag.StringVar(&o.Commit, "commit", "", "the Git commit of the build")
	flag.StringVar(&o.Source, "source", "", "the URL of Kogge's source repository")
	date := flag.String("date", "", "the build's date (RFC 3339; default: now)")
	server := flag.String("server", strings.Join(gomod.ServerPlatforms, " "), "platforms of the server binary")
	agent := flag.String("agent", strings.Join(gomod.AgentPlatforms, " "), "platforms of the agent binaries")
	flag.StringVar(&o.Web, "web", "web", "the web application's directory (empty: leave it out)")
	flag.StringVar(&o.RootFS, "rootfs", "", "a directory with an image's lib/apk/db/installed, etc/apk/world and etc/os-release")
	merge := flag.Bool("merge", false, "combine the SBOM files named as arguments instead")
	flag.Parse()
	o.Server, o.Agent = platforms(*server), platforms(*agent)
	o.Date = time.Now()
	if *date != "" {
		t, err := time.Parse(time.RFC3339, *date)
		if err != nil {
			fail(fmt.Errorf("-date: %w", err))
		}
		o.Date = t
	}
	var doc []byte
	var err error
	if *merge {
		doc, err = mergeFiles(flag.Args())
	} else {
		doc, err = generate(o)
	}
	if err != nil {
		fail(err)
	}
	if *out == "" {
		_, err = os.Stdout.Write(doc)
	} else if err = os.MkdirAll(filepath.Dir(*out), 0o755); err == nil {
		err = os.WriteFile(*out, doc, 0o644)
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sbom:", err)
	os.Exit(1)
}

func platforms(s string) []string {
	return strings.Fields(strings.ReplaceAll(s, ",", " "))
}

// generate writes the SBOM as indented JSON.
func generate(o options) ([]byte, error) {
	b, err := build(o)
	if err != nil {
		return nil, err
	}
	return encode(b)
}

// mergeFiles combines SBOMs of the same program: the metadata of the first, the components of all (by bom-ref)
// and the dependencies of all.
func mergeFiles(files []string) ([]byte, error) {
	if len(files) == 0 {
		return nil, errors.New("-merge needs SBOM files")
	}
	var merged *bom
	components := map[string]bool{}
	dependencies := map[string]int{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var b bom
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if merged == nil {
			merged = &bom{Schema: b.Schema, BOMFormat: b.BOMFormat, SpecVersion: b.SpecVersion, Version: 1, Metadata: b.Metadata,
				Components: []component{}, Dependencies: []dependency{}}
		} else if b.Metadata.Component.BOMRef != merged.Metadata.Component.BOMRef || b.SpecVersion != merged.SpecVersion {
			return nil, fmt.Errorf("%s describes %s (CycloneDX %s), not %s", file, b.Metadata.Component.BOMRef, b.SpecVersion,
				merged.Metadata.Component.BOMRef)
		}
		for _, p := range b.Metadata.Properties {
			if !slices.Contains(merged.Metadata.Properties, p) {
				merged.Metadata.Properties = append(merged.Metadata.Properties, p)
			}
		}
		for _, c := range b.Components {
			if !components[c.BOMRef] {
				components[c.BOMRef] = true
				merged.Components = append(merged.Components, c)
			}
		}
		for _, d := range b.Dependencies {
			i, ok := dependencies[d.Ref]
			if !ok {
				dependencies[d.Ref] = len(merged.Dependencies)
				merged.Dependencies = append(merged.Dependencies, dependency{Ref: d.Ref, DependsOn: append([]string{}, d.DependsOn...)})
				continue
			}
			for _, on := range d.DependsOn {
				if !slices.Contains(merged.Dependencies[i].DependsOn, on) {
					merged.Dependencies[i].DependsOn = append(merged.Dependencies[i].DependsOn, on)
				}
			}
		}
	}
	if err := checkReferences(merged); err != nil {
		return nil, err
	}
	return encode(merged)
}

// encode writes the SBOM as indented JSON with a serial number derived from its content, so that the same build
// gives the same SBOM.
func encode(b *bom) ([]byte, error) {
	b.SerialNumber = ""
	content, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	sum[6] = sum[6]&0x0f | 0x50 // a name-based UUID (version 5) from SHA-256 instead of SHA-1
	sum[8] = sum[8]&0x3f | 0x80
	b.SerialNumber = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func build(o options) (*bom, error) {
	if o.Name != "kogge" && o.Name != "kogge-agent" {
		return nil, fmt.Errorf("-name must be kogge or kogge-agent, not %q", o.Name)
	}
	authors := &entity{Name: "The Kogge authors"}
	root := component{Type: "application", BOMRef: o.Name, Supplier: authors, Manufacturer: authors, Name: o.Name,
		Version: o.Version, Licenses: licences("EUPL-1.2", "declared")}
	if o.Name == "kogge" {
		root.Description = "Kogge: issues and boards, wiki, Git hosting and pipelines on your own servers"
	} else {
		root.Description = "The build agent of Kogge, which runs pipeline jobs"
	}
	if o.Commit != "" {
		root.Properties = append(root.Properties, property{Name: "kogge:commit", Value: o.Commit})
	}
	if o.Source != "" {
		root.ExternalReferences = []reference{{Type: "vcs", URL: o.Source, Comment: strings.TrimSpace("commit " + o.Commit)}}
	}
	b := &bom{Schema: "http://cyclonedx.org/schema/bom-1.6.schema.json", BOMFormat: "CycloneDX", SpecVersion: "1.6",
		Version: 1, Components: []component{}, Dependencies: []dependency{}}
	b.Metadata = metadata{Timestamp: o.Date.UTC().Format(time.RFC3339), Lifecycles: []lifecycle{{Phase: "build"}},
		Tools: tools{Components: []component{{Type: "application", Name: "kogge-sbom", Version: o.Version,
			Description: "tools/sbom of Kogge", Supplier: authors}}},
		Component: root, Supplier: authors}
	direct := []string{}

	// Go: the modules of the builds and the standard library.
	var builds []gomod.Build
	var covered []string
	for _, p := range o.Server {
		builds = append(builds, gomod.Shipped([]string{p}, nil)...)
		covered = append(covered, "kogge "+p)
	}
	for _, p := range o.Agent {
		builds = append(builds, gomod.Shipped(nil, []string{p})...)
		covered = append(covered, "kogge-agent "+p)
	}
	if len(builds) > 0 {
		comps, refs, deps, err := goModules(o.Dir, builds)
		if err != nil {
			return nil, err
		}
		b.Components = append(b.Components, comps...)
		b.Dependencies = append(b.Dependencies, deps...)
		direct = append(direct, refs...)
		b.Metadata.Properties = append(b.Metadata.Properties, property{Name: "kogge:sbom:go",
			Value: "the Go modules and the standard library compiled into " + strings.Join(covered, ", ")})
	}

	// npm: the packages of the web application.
	if o.Web != "" {
		comps, refs, deps, err := npmPackages(filepath.Join(o.Dir, o.Web))
		if err != nil {
			return nil, err
		}
		b.Components = append(b.Components, comps...)
		b.Dependencies = append(b.Dependencies, deps...)
		direct = append(direct, refs...)
		b.Metadata.Properties = append(b.Metadata.Properties, property{Name: "kogge:sbom:npm",
			Value: "the npm packages of a production install of " + filepath.ToSlash(filepath.Join(o.Web, "package-lock.json")) +
				" (npm ci --omit=dev), from which the web application embedded in kogge is built"})
	}

	// apk: the packages of the image.
	if o.RootFS != "" {
		comps, refs, deps, err := apkPackages(o.RootFS)
		if err != nil {
			return nil, err
		}
		b.Components = append(b.Components, comps...)
		b.Dependencies = append(b.Dependencies, deps...)
		direct = append(direct, refs...)
		b.Metadata.Properties = append(b.Metadata.Properties, property{Name: "kogge:sbom:apk",
			Value: "the Alpine Linux packages installed in the container image (" + comps[0].Description + ")"})
	}
	b.Dependencies = append([]dependency{{Ref: root.BOMRef, DependsOn: direct}}, b.Dependencies...)
	return b, checkReferences(b)
}

// goModules describes the modules of the builds and the standard library.
func goModules(dir string, builds []gomod.Build) ([]component, []string, []dependency, error) {
	g, err := gomod.List(dir, builds...)
	if err != nil {
		return nil, nil, nil, err
	}
	stdlib, err := stdlibComponent(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	refs := map[string]string{}
	comps := []component{stdlib}
	for _, m := range g.Modules {
		if m.Version == "" || m.Sum == "" {
			return nil, nil, nil, fmt.Errorf("module %s has no version or checksum (replaced by a directory?)", m.Path)
		}
		namespace, name := "", m.Path
		if i := strings.LastIndex(m.Path, "/"); i >= 0 {
			namespace, name = m.Path[:i], m.Path[i+1:]
		}
		p := purl("golang", namespace, name, m.Version)
		refs[m.Path] = p
		comps = append(comps, component{Type: "library", BOMRef: p, Name: m.Path, Version: m.Version, PURL: p,
			Licenses:   licences(m.Licence, "concluded"),
			Properties: []property{{Name: "kogge:go:sum", Value: m.Sum}}})
	}
	var deps []dependency
	on := func(imports []string, std bool) []string {
		list := []string{}
		if std {
			list = append(list, stdlib.BOMRef)
		}
		for _, path := range imports {
			if ref, ok := refs[path]; ok {
				list = append(list, ref)
			}
		}
		return list
	}
	deps = append(deps, dependency{Ref: stdlib.BOMRef, DependsOn: []string{}})
	for _, m := range g.Modules {
		deps = append(deps, dependency{Ref: refs[m.Path], DependsOn: on(m.Imports, m.Stdlib)})
	}
	return comps, on(g.Direct, true), deps, nil
}

// stdlibComponent describes the Go standard library and runtime of the toolchain that builds Kogge, with the
// package URL and CPE that vulnerability databases use for Go.
func stdlibComponent(dir string) (component, error) {
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return component{}, fmt.Errorf("go env GOVERSION: %w", err)
	}
	toolchain := strings.Fields(string(out))
	if len(toolchain) == 0 || !strings.HasPrefix(toolchain[0], "go1.") {
		return component{}, fmt.Errorf("unexpected Go version %q", out)
	}
	v := strings.TrimPrefix(toolchain[0], "go")
	p := purl("golang", "", "stdlib", v)
	return component{Type: "library", BOMRef: p, Name: "stdlib", Version: v, PURL: p,
		CPE:         "cpe:2.3:a:golang:go:" + v + ":*:*:*:*:*:*:*",
		Description: "The Go standard library and runtime of " + toolchain[0],
		Licenses:    licences("BSD-3-Clause", "declared")}, nil
}

// checkReferences makes sure that the dependencies name components of the SBOM and that the bom-refs are unique.
func checkReferences(b *bom) error {
	refs := map[string]bool{b.Metadata.Component.BOMRef: true}
	for _, c := range b.Components {
		if refs[c.BOMRef] {
			return fmt.Errorf("two components are %s", c.BOMRef)
		}
		refs[c.BOMRef] = true
	}
	var errs []error
	for _, d := range b.Dependencies {
		for _, r := range append([]string{d.Ref}, d.DependsOn...) {
			if !refs[r] {
				errs = append(errs, fmt.Errorf("dependency on %s, which is not a component", r))
			}
		}
	}
	return errors.Join(errs...)
}
