// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"bufio"
	"bytes"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"kogge.de/kogge/tools/internal/gomod"
)

// The repository's root, from tools/sbom.
const repo = "../.."

// schema compiles CycloneDX's JSON schema of version 1.6 (testdata, from the specification's tag 1.6.2,
// Apache-2.0) without fetching anything.
func schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	for url, file := range map[string]string{
		"http://cyclonedx.org/schema/bom-1.6.schema.json":  "testdata/bom-1.6.schema.json",
		"http://cyclonedx.org/schema/spdx.schema.json":     "spdx.schema.json",
		"http://cyclonedx.org/schema/jsf-0.82.schema.json": "testdata/jsf-0.82.schema.json",
	} {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := c.AddResource(url, f); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	s, err := c.Compile("http://cyclonedx.org/schema/bom-1.6.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, doc []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatal(err)
	}
	if err := schema(t).Validate(v); err != nil {
		t.Fatalf("the SBOM does not follow CycloneDX 1.6: %#v", err)
	}
}

func generated(t *testing.T, o options) (*bom, []byte) {
	t.Helper()
	doc, err := generate(o)
	if err != nil {
		t.Fatal(err)
	}
	var b bom
	if err := json.Unmarshal(doc, &b); err != nil {
		t.Fatal(err)
	}
	return &b, doc
}

func (b *bom) component(ref string) *component {
	for i := range b.Components {
		if b.Components[i].BOMRef == ref {
			return &b.Components[i]
		}
	}
	return nil
}

func (b *bom) dependsOn(ref string) []string {
	for _, d := range b.Dependencies {
		if d.Ref == ref {
			return d.DependsOn
		}
	}
	return nil
}

func (c *component) licence() string {
	if c == nil || len(c.Licenses) != 1 {
		return ""
	}
	if l := c.Licenses[0]; l.License != nil {
		return l.License.ID + l.License.Name
	}
	return c.Licenses[0].Expression
}

func (c *component) property(name string) string {
	for _, p := range c.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// goSum reads the checksums of the modules' contents from go.sum.
func goSum(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(repo, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 3 && !strings.HasSuffix(f[1], "/go.mod") {
			sums[f[0]+"@"+f[1]] = f[2]
		}
	}
	return sums
}

func lockEntries(t *testing.T) map[string]lockEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repo, "web", "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock lockFile
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	return lock.Packages
}

var release = options{Name: "kogge", Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567",
	Source: "https://example.com/kogge.git", Date: time.Date(2026, 10, 20, 8, 15, 0, 0, time.UTC),
	Server: gomod.ServerPlatforms, Agent: gomod.AgentPlatforms, Web: "web", Dir: repo}

// TestReleaseSBOM: the SBOM of a release follows CycloneDX 1.6 and lists the Go modules with the licences that
// tools/golicenses finds and the checksums of go.sum, the standard library, and the npm packages of the web
// application with their licences and integrity hashes, but no packages for development.
func TestReleaseSBOM(t *testing.T) {
	b, doc := generated(t, release)
	validate(t, doc)
	if again, _ := generate(release); !bytes.Equal(doc, again) {
		t.Error("the same build gave two different SBOMs")
	}
	m := b.Metadata
	if m.Timestamp != "2026-10-20T08:15:00Z" || m.Component.Name != "kogge" || m.Component.Version != "1.2.3" ||
		m.Component.licence() != "EUPL-1.2" || m.Component.property("kogge:commit") != release.Commit ||
		len(m.Component.ExternalReferences) != 1 || m.Component.ExternalReferences[0].URL != release.Source {
		t.Errorf("metadata = %+v", m)
	}
	if !regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(b.SerialNumber) {
		t.Errorf("serial number %s", b.SerialNumber)
	}

	// Go modules: the licence tools/golicenses recognises, and the checksum of go.sum.
	sums := goSum(t)
	for _, want := range []struct{ module, licence string }{
		{"github.com/jackc/pgx/v5", "MIT"}, {"github.com/riverqueue/river", "MPL-2.0"},
		{"golang.org/x/crypto", "BSD-3-Clause"}, {"github.com/prometheus/client_golang", "Apache-2.0"},
		{"go.yaml.in/yaml/v3", "Apache-2.0 AND MIT"}, {"github.com/danielgtaylor/huma/v2", "MIT"},
	} {
		version := moduleVersion(t, want.module)
		ref := "pkg:golang/" + want.module + "@" + version
		c := b.component(ref)
		if c == nil {
			t.Errorf("%s is missing", ref)
			continue
		}
		if c.Name != want.module || c.Version != version || c.PURL != ref || c.licence() != want.licence ||
			c.property("kogge:go:sum") != sums[want.module+"@"+version] || c.property("kogge:go:sum") == "" {
			t.Errorf("%s = %+v", ref, *c)
		}
	}
	std := "pkg:golang/stdlib@" + strings.TrimPrefix(goVersion(t), "go")
	if c := b.component(std); c == nil || c.CPE == "" || c.licence() != "BSD-3-Clause" {
		t.Errorf("standard library %s = %+v", std, c)
	}
	pgx := "pkg:golang/github.com/jackc/pgx/v5@" + moduleVersion(t, "github.com/jackc/pgx/v5")
	if on := b.dependsOn("kogge"); !slices.Contains(on, pgx) || !slices.Contains(on, std) {
		t.Errorf("kogge depends on %v", on)
	}
	if on := b.dependsOn(pgx); !slices.Contains(on, "pkg:golang/github.com/jackc/puddle/v2@"+moduleVersion(t, "github.com/jackc/puddle/v2")) ||
		!slices.Contains(on, std) {
		t.Errorf("pgx depends on %v", on)
	}

	// npm packages: those of a production install, with the declared licence and the integrity hash.
	lock := lockEntries(t)
	for _, want := range []struct{ path, ref, licence string }{
		{"node_modules/react", "pkg:npm/react@", "MIT"},
		{"node_modules/@mantine/core", "pkg:npm/%40mantine/core@", "MIT"},
		{"node_modules/dompurify", "pkg:npm/dompurify@", "(MPL-2.0 OR Apache-2.0)"},
		// The licence file states it; the manifest does not (KNOWN in tools/npm-licenses.mjs).
		{"node_modules/khroma", "pkg:npm/khroma@", "MIT"},
		// The manifest names the code's licence, not that of the fonts the package ships (KNOWN, too).
		{"node_modules/katex", "pkg:npm/katex@", "MIT AND OFL-1.1"},
		// An optional dependency of @tiptap/react that the link editor's bubble menu ships.
		{"node_modules/@tiptap/extension-bubble-menu", "pkg:npm/%40tiptap/extension-bubble-menu@", "MIT"},
	} {
		e, ok := lock[want.path]
		if !ok {
			t.Errorf("%s is not in package-lock.json", want.path)
			continue
		}
		ref := want.ref + e.Version
		c := b.component(ref)
		if c == nil {
			t.Errorf("%s is missing", ref)
			continue
		}
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(e.Integrity, "sha512-"))
		if c.licence() != want.licence || len(c.Hashes) != 1 || c.Hashes[0].Alg != "SHA-512" ||
			c.Hashes[0].Content != hex.EncodeToString(raw) || len(c.ExternalReferences) != 1 || c.ExternalReferences[0].URL != e.Resolved {
			t.Errorf("%s = %+v", ref, *c)
		}
	}
	if on := b.dependsOn("kogge"); !slices.Contains(on, "pkg:npm/react@"+lock["node_modules/react"].Version) {
		t.Errorf("kogge does not depend on react: %v", on)
	}
	// Every package of a production install is there, and no package for development (unless the same version
	// is also installed for production elsewhere in the tree).
	production := map[string]bool{}
	for path, e := range lock {
		if !strings.HasPrefix(path, "node_modules/") {
			continue
		}
		name := e.Name
		if name == "" {
			name = path[strings.LastIndex(path, "node_modules/")+len("node_modules/"):]
		}
		group, local := "", name
		if strings.HasPrefix(name, "@") {
			group, local, _ = strings.Cut(name, "/")
		}
		ref := purl("npm", group, local, e.Version)
		platformSpecific := e.Optional && (len(e.OS) > 0 || len(e.CPU) > 0)
		switch in := b.component(ref) != nil; {
		case name == "elkjs" && in:
			t.Errorf("elkjs is in the SBOM, but the web build leaves it out")
		case !e.Dev && !platformSpecific && name != "elkjs":
			production[ref] = true
			if !in {
				t.Errorf("%s, a package of a production install, is missing", path)
			}
		}
	}
	npm := 0
	for _, c := range b.Components {
		if strings.HasPrefix(c.PURL, "pkg:npm/") {
			npm++
			if !production[c.BOMRef] {
				t.Errorf("%s is in the SBOM but not a package of a production install", c.BOMRef)
			}
		}
	}
	if npm < 100 || npm != len(production) {
		t.Errorf("%d npm packages in the SBOM, %d in a production install", npm, len(production))
	}
}

func moduleVersion(t *testing.T, path string) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Version}}", path)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

func goVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(out))[0]
}

// TestLinkedModules: the SBOM's Go modules are exactly those the linker puts into kogge and kogge-agent, with
// the same versions and checksums (Go records them in every binary; `go version -m` shows them).
func TestLinkedModules(t *testing.T) {
	if testing.Short() {
		t.Skip("builds kogge")
	}
	o := release
	o.Server, o.Agent, o.Web = []string{runtime.GOOS + "/" + runtime.GOARCH}, []string{runtime.GOOS + "/" + runtime.GOARCH}, ""
	b, _ := generated(t, o)
	inSBOM := map[string]string{}
	for _, c := range b.Components {
		if c.Name != "stdlib" {
			inSBOM[c.Name+"@"+c.Version] = c.property("kogge:go:sum")
		}
	}
	linked := map[string]string{}
	dir := t.TempDir()
	for _, cmd := range []string{"kogge", "kogge-agent"} {
		bin := filepath.Join(dir, cmd)
		// Without the tag webui, which needs the built web application; it adds no modules.
		build := exec.Command("go", "build", "-o", bin, "./cmd/"+cmd)
		build.Dir = repo
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", cmd, err, out)
		}
		info, err := buildinfo.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range info.Deps {
			if d.Replace != nil {
				d = d.Replace
			}
			linked[d.Path+"@"+d.Version] = d.Sum
		}
	}
	if len(linked) == 0 || len(linked) != len(inSBOM) {
		t.Errorf("%d modules linked, %d in the SBOM", len(linked), len(inSBOM))
	}
	for mod, sum := range linked {
		if inSBOM[mod] != sum {
			t.Errorf("linked %s %s, in the SBOM %q", mod, sum, inSBOM[mod])
		}
	}
}

// TestImageSBOM: an image's SBOM also lists the operating system and its Alpine packages, with package URLs
// that name the architecture, the release and the source package.
func TestImageSBOM(t *testing.T) {
	o := release
	o.Name, o.Server, o.Agent, o.Web, o.RootFS = "kogge-agent", nil, []string{"linux/amd64"}, "", "testdata/alpine"
	b, doc := generated(t, o)
	validate(t, doc)
	if b.Metadata.Component.Name != "kogge-agent" {
		t.Errorf("component = %+v", b.Metadata.Component)
	}
	for _, c := range b.Components {
		if strings.HasPrefix(c.PURL, "pkg:npm/") || c.Name == "github.com/jackc/pgx/v5" {
			t.Errorf("%s is not part of the agent", c.PURL)
		}
	}
	if c := b.component("pkg:golang/go.yaml.in/yaml/v3@" + moduleVersion(t, "go.yaml.in/yaml/v3")); c == nil {
		t.Error("the agent's YAML module is missing")
	}
	if c := b.component("os:alpine-3.22.6"); c == nil || c.Type != "operating-system" || c.Version != "3.22.6" {
		t.Errorf("operating system = %+v", c)
	}
	git := "pkg:apk/alpine/git@2.49.1-r0?arch=x86_64&distro=alpine-3.22.6"
	c := b.component(git)
	if c == nil || c.licence() != "GPL-2.0-only" || c.property("kogge:apk:checksum") == "" ||
		!strings.HasPrefix(c.property("kogge:apk:checksum"), "Q1") {
		t.Fatalf("git = %+v", c)
	}
	// libcrypto3 comes from the source package openssl.
	var crypto *component
	for i := range b.Components {
		if b.Components[i].Name == "libcrypto3" {
			crypto = &b.Components[i]
		}
	}
	if crypto == nil || !strings.Contains(crypto.PURL, "upstream=openssl") || crypto.licence() != "Apache-2.0" {
		t.Errorf("libcrypto3 = %+v", crypto)
	}
	// git needs curl's library through its shared library, and the agent installed git on purpose.
	if on := b.dependsOn(git); !slices.ContainsFunc(on, func(r string) bool { return strings.Contains(r, "/libcurl@") }) {
		t.Errorf("git depends on %v", on)
	}
	if on := b.dependsOn("kogge-agent"); !slices.Contains(on, git) || !slices.Contains(on, "os:alpine-3.22.6") {
		t.Errorf("kogge-agent depends on %v", on)
	}
	// Licences that are not SPDX identifiers keep their name.
	for _, c := range b.Components {
		if l := c.Licenses; len(l) == 1 && l[0].License != nil && l[0].License.Name != "" && l[0].License.Name != "Public-Domain" {
			t.Errorf("%s: licence %q given by name", c.Name, l[0].License.Name)
		}
	}
}

// TestMerge: the SBOMs of an image's platforms combine into one for the multi-platform image, with the packages
// of both architectures and the modules once.
func TestMerge(t *testing.T) {
	// The fixture's packages again, as those of an arm64 image.
	arm := t.TempDir()
	for _, f := range []string{"lib/apk/db/installed", "etc/apk/world", "usr/lib/os-release"} {
		raw, err := os.ReadFile(filepath.Join("testdata/alpine", f))
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.ReplaceAll(raw, []byte("A:x86_64"), []byte("A:aarch64"))
		raw = bytes.ReplaceAll(raw, []byte("musl-x86_64"), []byte("musl-aarch64"))
		if err := os.MkdirAll(filepath.Dir(filepath.Join(arm, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(arm, f), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	var files []string
	for platform, root := range map[string]string{"linux/amd64": "testdata/alpine", "linux/arm64": arm} {
		o := release
		o.Name, o.Server, o.Agent, o.Web, o.RootFS = "kogge-agent", nil, []string{platform}, "", root
		_, doc := generated(t, o)
		file := filepath.Join(dir, strings.ReplaceAll(platform, "/", "-")+".cdx.json")
		if err := os.WriteFile(file, doc, 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	slices.Sort(files)
	doc, err := mergeFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	validate(t, doc)
	var b bom
	if err := json.Unmarshal(doc, &b); err != nil {
		t.Fatal(err)
	}
	amd, arm64 := "pkg:apk/alpine/git@2.49.1-r0?arch=x86_64&distro=alpine-3.22.6", "pkg:apk/alpine/git@2.49.1-r0?arch=aarch64&distro=alpine-3.22.6"
	yaml := 0
	for _, c := range b.Components {
		if c.Name == "go.yaml.in/yaml/v3" {
			yaml++
		}
	}
	if b.component(amd) == nil || b.component(arm64) == nil || yaml != 1 {
		t.Errorf("merged: git for amd64 %v, for arm64 %v, %d YAML modules", b.component(amd) != nil, b.component(arm64) != nil, yaml)
	}
	if on := b.dependsOn("kogge-agent"); !slices.Contains(on, amd) || !slices.Contains(on, arm64) {
		t.Errorf("kogge-agent depends on %v", on)
	}
	if _, err := mergeFiles([]string{files[0], filepath.Join(repo, "web", "package.json")}); err == nil {
		t.Error("merged a file that is no SBOM")
	}
}

// TestSchemaRejects: the schema check would notice mistakes: an unknown licence identifier, a hash that is not
// hexadecimal, a component type that does not exist.
func TestSchemaRejects(t *testing.T) {
	o := release
	o.Server = nil
	_, doc := generated(t, o)
	s := schema(t)
	for _, damage := range [][2]string{
		{`"id": "BSD-3-Clause"`, `"id": "BSD-3-Clause-Kogge"`},
		{`"type": "library"`, `"type": "libary"`},
		{`"bomFormat": "CycloneDX"`, `"bomFormat": "SPDX"`},
		{`"alg": "SHA-512",
          "content": "`, `"alg": "SHA-512",
          "content": "x`},
	} {
		bad := bytes.Replace(doc, []byte(damage[0]), []byte(damage[1]), 1)
		if bytes.Equal(bad, doc) {
			t.Fatalf("%s is not in the SBOM", damage[0])
		}
		var v any
		if err := json.Unmarshal(bad, &v); err != nil {
			t.Fatal(err)
		}
		if err := s.Validate(v); err == nil {
			t.Errorf("the schema accepts %s", damage[1])
		}
	}
}

func TestLicences(t *testing.T) {
	for in, want := range map[string]licence{
		"MIT":                  {License: &licenceRef{ID: "MIT", Acknowledgement: "declared"}},
		"zlib":                 {License: &licenceRef{ID: "Zlib", Acknowledgement: "declared"}},
		"(Apache-2.0)":         {License: &licenceRef{ID: "Apache-2.0", Acknowledgement: "declared"}},
		"Public-Domain":        {License: &licenceRef{Name: "Public-Domain", Acknowledgement: "declared"}},
		"MIT AND BSD-2-Clause": {Expression: "MIT AND BSD-2-Clause", Acknowledgement: "declared"},
		"(MIT OR CC0-1.0)":     {Expression: "(MIT OR CC0-1.0)", Acknowledgement: "declared"},
	} {
		got := licences(in, "declared")
		if len(got) != 1 || got[0].Expression != want.Expression ||
			(got[0].License == nil) != (want.License == nil) || (want.License != nil && *got[0].License != *want.License) {
			t.Errorf("licences(%q) = %+v", in, got)
		}
	}
	if got := licences("", "declared"); got != nil {
		t.Errorf("no licence: %+v", got)
	}
}

func TestPURL(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{purl("npm", "@tiptap", "react", "3.1.0"), "pkg:npm/%40tiptap/react@3.1.0"},
		{purl("golang", "github.com/docker", "docker", "v28.0.0+incompatible"), "pkg:golang/github.com/docker/docker@v28.0.0%2Bincompatible"},
		{purl("apk", "alpine", "libcrypto3", "3.5.4-r0", "arch", "aarch64", "distro", "alpine-3.22.6", "upstream", "openssl"),
			"pkg:apk/alpine/libcrypto3@3.5.4-r0?arch=aarch64&distro=alpine-3.22.6&upstream=openssl"},
		{purl("apk", "alpine", "git", "2.49.1-r0", "arch", "x86_64", "upstream", ""), "pkg:apk/alpine/git@2.49.1-r0?arch=x86_64"},
	} {
		if c.got != c.want {
			t.Errorf("purl = %s, want %s", c.got, c.want)
		}
	}
}

// TestNPMResolution: dependencies resolve like Node.js does, nested copies first; packages for development and
// platform-specific optional ones are not shipped.
func TestNPMResolution(t *testing.T) {
	dir := t.TempDir()
	lock := `{"lockfileVersion": 3, "packages": {
		"": {"dependencies": {"a": "^1", "c": "^1"}, "devDependencies": {"d": "^1"}},
		"node_modules/a": {"version": "1.0.0", "license": "MIT", "dependencies": {"b": "^2"}},
		"node_modules/a/node_modules/b": {"version": "2.0.0", "license": "ISC"},
		"node_modules/b": {"version": "1.0.0", "license": "ISC"},
		"node_modules/c": {"version": "1.0.0", "license": {"type": "BSD-3-Clause"}, "dependencies": {"b": "^1"},
			"optionalDependencies": {"e": "^1", "f": "^1"}},
		"node_modules/d": {"version": "1.0.0", "license": "MIT", "dev": true},
		"node_modules/e": {"version": "1.0.0", "license": "MIT", "optional": true},
		"node_modules/f": {"version": "1.0.0", "license": "MIT", "optional": true, "os": ["darwin"]}
	}}`
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	comps, direct, deps, err := npmPackages(dir)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, c := range comps {
		refs = append(refs, c.BOMRef)
	}
	if want := []string{"pkg:npm/a@1.0.0", "pkg:npm/b@1.0.0", "pkg:npm/b@2.0.0", "pkg:npm/c@1.0.0", "pkg:npm/e@1.0.0"}; !slices.Equal(refs, want) {
		t.Errorf("packages = %v", refs)
	}
	if !slices.Equal(direct, []string{"pkg:npm/a@1.0.0", "pkg:npm/c@1.0.0"}) {
		t.Errorf("direct = %v", direct)
	}
	b := &bom{Dependencies: deps, Components: comps}
	if on := b.dependsOn("pkg:npm/a@1.0.0"); !slices.Equal(on, []string{"pkg:npm/b@2.0.0"}) {
		t.Errorf("a depends on %v", on)
	}
	if on := b.dependsOn("pkg:npm/c@1.0.0"); !slices.Equal(on, []string{"pkg:npm/b@1.0.0", "pkg:npm/e@1.0.0"}) {
		t.Errorf("c depends on %v", on)
	}
	if c := b.component("pkg:npm/c@1.0.0"); c.licence() != "BSD-3-Clause" {
		t.Errorf("c's licence %q", c.licence())
	}
}

// TestPlatforms: the Makefile and the image build ship the agent for the platforms the SBOM and the licence
// check cover.
func TestPlatforms(t *testing.T) {
	want := strings.Join(gomod.AgentPlatforms, " ")
	for _, file := range []string{"Makefile", "deploy/Dockerfile"} {
		raw, err := os.ReadFile(filepath.Join(repo, file))
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`AGENT_PLATFORMS ?\??=\s*"?([^"\n]+)"?`).FindSubmatch(raw)
		if m == nil || strings.TrimSpace(string(m[1])) != want {
			t.Errorf("%s: AGENT_PLATFORMS %q, want %q", file, m, want)
		}
	}
}
