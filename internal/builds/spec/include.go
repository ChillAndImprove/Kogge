// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Pipeline files can include other files (include): files of the same repository at the same commit, and files of
// other repositories of the same code project at a ref. The caller reads them (Reader) and decides who may read
// what. Files are merged predictably:
//
//   - A pipeline is defined in one file, with its stages and jobs; the same pipeline in two files is a mistake.
//   - variables and defaults apply to all files. A file's values override those of the files it includes; the same
//     value set by two files of which neither includes the other is a mistake.
//   - The files that an included file of another repository includes without naming a repository are of that
//     repository, at the same commit.
//
// Includes are bounded: at most MaxIncludes files, nested at most MaxIncludeDepth deep, each at most
// MaxIncludeSize, together at most MaxIncludeTotal; a file that includes itself, also indirectly, is a mistake.

// Limits of includes.
const (
	MaxIncludes     = 25
	MaxIncludeDepth = 3
	MaxIncludeSize  = 256 << 10
	MaxIncludeTotal = 1 << 20
)

// Reader reads the files that pipeline files include.
type Reader interface {
	// Read returns a file and the commit it was read at. Without repository, the file is one of the pipeline's
	// repository at the commit of the run; with a repository (KEY/slug), one of that repository at ref (a branch, tag
	// or commit; empty: its default branch). Errors of type *IncludeError are told to people as mistakes of the file;
	// other errors end the reading.
	Read(ctx context.Context, repository, ref, path string) (data []byte, commit string, err error)
}

// IncludeError is why a file cannot be included, for people.
type IncludeError struct {
	Message string
}

func (e *IncludeError) Error() string { return e.Message }

// Included is a file that was included, as it was read.
type Included struct {
	Repository string `json:"repository,omitempty" doc:"KEY/slug of another repository; empty: the pipeline's repository"`
	Path       string `json:"path"`
	Ref        string `json:"ref,omitempty" doc:"The branch, tag or commit that the include names"`
	Commit     string `json:"commit,omitempty" doc:"The commit the file was read at"`
}

// Name is how errors and the run's page name the file: its path, with the repository if it is another one.
func (i Included) Name() string {
	if i.Repository == "" {
		return i.Path
	}
	return i.Repository + ":" + i.Path
}

// Parse reads a pipeline file without the files it includes (File.Unresolved): their pipelines, variables and
// defaults are missing, the rest is checked.
func Parse(data []byte) (*File, error) {
	return Load(context.Background(), data, nil)
}

// Load reads a pipeline file and, through r, the files it includes (r nil: as Parse).
func Load(ctx context.Context, data []byte, r Reader) (*File, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Errors{yamlError("", err)}
	}
	if len(doc.Content) == 0 {
		return nil, Errors{{Message: "the pipeline file is empty"}}
	}
	l := &loader{ctx: ctx, r: r, p: &parser{}, ids: map[string]int{}}
	main := &source{root: doc.Content[0]}
	l.sources = []*source{main}
	l.top(main)
	f := &File{Variables: map[string]string{}, Defaults: Defaults{Timeout: DefaultTimeout}}
	if v := main.version; v != nil {
		f.Version = l.p.integer(v, "version", 1, 1)
	}
	if f.Version != 1 && main.root.Kind == yaml.MappingNode {
		l.p.errorf(main.root, "version: 1 is required")
	}
	if main.include != nil {
		if r == nil {
			f.Unresolved = true
			l.entries(main)
		} else if err := l.include(0, nil); err != nil {
			return nil, err
		}
	}
	for _, s := range l.sources[1:] {
		f.Includes = append(f.Includes, s.inc)
	}
	l.reachable()
	l.merge(f)
	l.pipelines(f)
	if len(l.p.errs) > 0 {
		return nil, l.p.errs
	}
	f.Warnings = l.p.warnings
	return f, nil
}

// source is the pipeline file or a file it includes, with the nodes of its top level.
type source struct {
	inc      Included // zero for the pipeline file
	name     string   // Included.Name; empty for the pipeline file
	depth    int
	root     *yaml.Node
	children []int // the sources it includes
	reach    map[int]bool

	version, include, variables, defaults, pipelines *yaml.Node
}

type loader struct {
	ctx     context.Context
	r       Reader
	p       *parser
	sources []*source
	ids     map[string]int // repository, commit and path of a source → its index
	total   int            // bytes read
}

// display names a source in messages about another one.
func (l *loader) display(i int) string {
	if i == 0 {
		return Path
	}
	return l.sources[i].name
}

func (l *loader) top(s *source) {
	l.p.file = s.name
	known := map[string]func(*yaml.Node){
		"version":   func(v *yaml.Node) { s.version = v },
		"include":   func(v *yaml.Node) { s.include = v },
		"variables": func(v *yaml.Node) { s.variables = v },
		"defaults":  func(v *yaml.Node) { s.defaults = v },
		"pipelines": func(v *yaml.Node) { s.pipelines = v },
	}
	l.p.fields(s.root, "the file", known)
}

// includeRef is an entry of include as written.
type includeRef struct {
	repo, ref, path string
	node            *yaml.Node
}

// includePath is the path of an included file: relative to the top of the repository, a YAML file (validPath also
// refuses . and .. segments).
var includePath = regexp.MustCompile(`^[A-Za-z0-9._@+-][A-Za-z0-9._@+ -]*(?:/[A-Za-z0-9._@+-][A-Za-z0-9._@+ -]*)*\.ya?ml$`)

func validPath(p string) bool {
	if len(p) > 256 || !includePath.MatchString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// refName is a branch, tag or commit that an include names.
var refName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/@+-]{0,254}$`)

// entries reads the entries of a file's include: paths, or mappings {file, repository, ref}.
func (l *loader) entries(s *source) []includeRef {
	p := l.p
	p.file = s.name
	n := s.include
	items := []*yaml.Node{n}
	switch {
	case n.Kind == yaml.SequenceNode:
		items = n.Content
	case n.Kind != yaml.ScalarNode || n.Tag == "!!null":
		p.errorf(n, "include must be a list of files")
		return nil
	}
	var out []includeRef
	for _, it := range items {
		e := includeRef{node: it}
		switch it.Kind {
		case yaml.ScalarNode:
			e.path = p.str(it, "include")
		case yaml.MappingNode:
			p.fields(it, "include", map[string]func(*yaml.Node){
				"file":       func(v *yaml.Node) { e.path = p.str(v, "include.file") },
				"repository": func(v *yaml.Node) { e.repo = p.str(v, "include.repository") },
				"ref":        func(v *yaml.Node) { e.ref = p.str(v, "include.ref") },
			})
		default:
			p.errorf(it, "include must be a list of files")
			continue
		}
		switch {
		case e.path == "":
			p.errorf(it, "an entry of include names its file (file)")
			continue
		case !validPath(e.path):
			p.errorf(it, "include: %q is not the path of a YAML file (.yml or .yaml) from the top of the repository", e.path)
			continue
		case e.repo != "" && !repoPattern.MatchString(e.repo):
			p.errorf(it, "include.repository %q is not a repository (KEY/slug)", e.repo)
			continue
		case e.ref != "" && e.repo == "":
			p.errorf(it, "include.ref needs include.repository: the files of this repository are read at the commit of the run")
			continue
		case e.ref != "" && (!refName.MatchString(e.ref) || strings.Contains(e.ref, "..")):
			p.errorf(it, "include.ref %q is not a branch, tag or commit", e.ref)
			continue
		}
		if slices.ContainsFunc(out, func(o includeRef) bool { return o.repo == e.repo && o.ref == e.ref && o.path == e.path }) {
			p.errorf(it, "%s is included twice", e.path)
			continue
		}
		out = append(out, e)
	}
	return out
}

// include reads the files that source i includes, and theirs; path are the sources that include i.
func (l *loader) include(i int, path []int) error {
	s := l.sources[i]
	for _, e := range l.entries(s) {
		l.p.file = s.name
		inc := Included{Repository: e.repo, Path: e.path, Ref: e.ref}
		ref := e.ref
		if e.repo == "" && s.inc.Repository != "" {
			// A file of another repository includes a file of its own repository: at the same commit.
			inc.Repository, inc.Ref, ref = s.inc.Repository, s.inc.Ref, s.inc.Commit
		}
		if e.repo == "" && s.inc.Repository == "" && e.path == Path {
			l.p.errorf(e.node, "the pipeline file cannot include itself")
			continue
		}
		if s.depth >= MaxIncludeDepth {
			l.p.errorf(e.node, "include: files may include others at most %d deep", MaxIncludeDepth)
			continue
		}
		if len(l.sources) > MaxIncludes {
			l.p.errorf(e.node, "include: a pipeline file may include at most %d files", MaxIncludes)
			return nil
		}
		data, commit, err := l.r.Read(l.ctx, inc.Repository, ref, inc.Path)
		if err != nil {
			if ie, ok := err.(*IncludeError); ok {
				l.p.errorf(e.node, "include %s: %s", inc.Name(), ie.Message)
				continue
			}
			return err
		}
		inc.Commit = commit
		id := inc.Repository + "\x00" + commit + "\x00" + inc.Path
		if j, ok := l.ids[id]; ok {
			if j == i || slices.Contains(path, j) {
				chain := []string{}
				for _, k := range append(append(slices.Clone(path), i), j) {
					chain = append(chain, l.display(k))
				}
				l.p.errorf(e.node, "include: the files include each other: %s", strings.Join(chain, " → "))
				continue
			}
			s.children = append(s.children, j)
			continue
		}
		if len(data) > MaxIncludeSize {
			l.p.errorf(e.node, "include %s: the file has more than %d KiB", inc.Name(), MaxIncludeSize>>10)
			continue
		}
		if l.total += len(data); l.total > MaxIncludeTotal {
			l.p.errorf(e.node, "include %s: the included files have more than %d MiB together", inc.Name(), MaxIncludeTotal>>20)
			return nil
		}
		child := &source{inc: inc, name: inc.Name(), depth: s.depth + 1}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			l.p.add(&l.p.errs, yamlError(child.name, err))
			continue
		}
		if len(doc.Content) == 0 {
			l.p.errorf(e.node, "include %s: the file is empty", inc.Name())
			continue
		}
		child.root = doc.Content[0]
		index := len(l.sources)
		l.ids[id] = index
		l.sources = append(l.sources, child)
		s.children = append(s.children, index)
		l.top(child)
		if child.version != nil {
			l.p.integer(child.version, "version", 1, 1)
		}
		if child.include != nil {
			if err := l.include(index, append(slices.Clone(path), i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// reachable works out which sources each one includes, also indirectly.
func (l *loader) reachable() {
	var visit func(i int, reach map[int]bool)
	visit = func(i int, reach map[int]bool) {
		for _, c := range l.sources[i].children {
			if !reach[c] {
				reach[c] = true
				visit(c, reach)
			}
		}
	}
	for i, s := range l.sources {
		s.reach = map[int]bool{}
		visit(i, s.reach)
		delete(s.reach, i)
	}
}

// setting is a value of variables or defaults with the source that sets it.
type setting struct {
	src        int
	key, value *yaml.Node
}

// settle records a value: a file's values override those of the files it includes, others conflict.
func (l *loader) settle(all map[string]setting, what, name string, s setting) {
	prev, ok := all[name]
	switch {
	case !ok, l.sources[s.src].reach[prev.src]:
		all[name] = s
	case l.sources[prev.src].reach[s.src]:
	default:
		l.p.file = l.sources[s.src].name
		l.p.errorf(s.key, "%s %s is set in %s (line %d) too; a file overrides only the values of the files it includes", what, name,
			l.display(prev.src), prev.key.Line)
	}
}

// merge combines the variables and defaults of the sources.
func (l *loader) merge(f *File) {
	vars := map[string]setting{}
	defaults := map[string]setting{}
	var order []string // keys of defaults in the order they were first set
	for i, s := range l.sources {
		l.p.file = s.name
		if s.variables != nil {
			values := l.p.variables(s.variables, "variables")
			for k := 0; k+1 < len(s.variables.Content); k += 2 {
				key, value := s.variables.Content[k], s.variables.Content[k+1]
				if _, ok := values[key.Value]; ok {
					l.settle(vars, "variables:", key.Value, setting{i, key, value})
				}
			}
		}
		if s.defaults != nil {
			capture := func(name string) func(*yaml.Node) {
				return func(v *yaml.Node) {
					var key *yaml.Node
					for k := 0; k+1 < len(s.defaults.Content); k += 2 {
						if s.defaults.Content[k+1] == v {
							key = s.defaults.Content[k]
						}
					}
					if !contains(order, name) {
						order = append(order, name)
					}
					l.settle(defaults, "defaults:", name, setting{i, key, v})
				}
			}
			l.p.fields(s.defaults, "defaults", map[string]func(*yaml.Node){
				"image": capture("image"), "runs-on": capture("runs-on"), "timeout": capture("timeout"), "retry": capture("retry"),
				"resources": capture("resources"),
			})
		}
	}
	for name, s := range vars {
		f.Variables[name] = s.value.Value
	}
	for _, name := range order {
		s := defaults[name]
		l.p.file = l.sources[s.src].name
		switch name {
		case "image":
			f.Defaults.Image = l.p.image(s.value, "defaults.image")
		case "runs-on":
			f.Defaults.RunsOn = l.p.strList(s.value, "defaults.runs-on")
		case "timeout":
			if f.Defaults.Timeout = l.p.duration(s.value, "defaults.timeout"); f.Defaults.Timeout > MaxTimeout {
				l.p.errorf(s.value, "defaults.timeout may be at most 24h")
			}
		case "retry":
			f.Defaults.Retry = l.p.integer(s.value, "defaults.retry", 0, 2)
		case "resources":
			f.Defaults.Resources = l.p.resources(s.value, "defaults.resources", Resources{})
		}
	}
}

// pipelines reads the pipelines of the sources: those of the pipeline file first, then those of the included files
// in the order they were read.
func (l *loader) pipelines(f *File) {
	type defined struct {
		src  int
		line int
	}
	ids := map[string]defined{}
	main := l.sources[0]
	for i, s := range l.sources {
		l.p.file = s.name
		n := s.pipelines
		if n == nil {
			continue
		}
		if n.Kind != yaml.MappingNode {
			l.p.errorf(n, "pipelines must be a mapping of ids to pipelines")
			continue
		}
		for k := 0; k+1 < len(n.Content); k += 2 {
			key, v := n.Content[k], n.Content[k+1]
			if !idPattern.MatchString(key.Value) {
				l.p.errorf(key, "the pipeline id %q may only have lower-case letters, digits and -", key.Value)
			}
			if prev, ok := ids[key.Value]; ok {
				l.p.errorf(key, "pipeline %s is defined in %s (line %d) too; a pipeline is defined in one file", key.Value, l.display(prev.src),
					prev.line)
				continue
			}
			ids[key.Value] = defined{i, key.Line}
			f.Pipelines = append(f.Pipelines, l.p.pipeline(f, key.Value, v))
		}
	}
	l.p.file = ""
	switch {
	case len(f.Pipelines) > 0 || f.Unresolved || main.root.Kind != yaml.MappingNode:
	case main.pipelines != nil && main.pipelines.Kind == yaml.MappingNode:
		l.p.errorf(main.pipelines, "define at least one pipeline")
	case main.pipelines == nil:
		l.p.errorf(main.root, "pipelines are missing")
	}
}

// IncludeName names an included file for an error of it.
func IncludeName(repository, path string) string {
	return Included{Repository: repository, Path: path}.Name()
}

// String describes the include for logs.
func (i Included) String() string {
	if i.Commit == "" {
		return i.Name()
	}
	return fmt.Sprintf("%s@%s", i.Name(), short(i.Commit))
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
