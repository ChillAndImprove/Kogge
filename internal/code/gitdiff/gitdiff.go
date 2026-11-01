// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package gitdiff parses the output of git diff (unified format with git's extended headers) into files, hunks
// and lines. It is pure: no file system, no processes.
package gitdiff

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// File statuses.
const (
	Added    = "added"
	Deleted  = "deleted"
	Modified = "modified"
	Renamed  = "renamed"
	Copied   = "copied"
)

// Line kinds.
const (
	Context  = "context"
	Addition = "add"
	Deletion = "delete"
)

// Line is a line of a hunk. Old and New are line numbers (0 where the line does not exist on that side).
type Line struct {
	Kind      string `json:"kind" enum:"context,add,delete"`
	Old       int    `json:"old,omitempty"`
	New       int    `json:"new,omitempty"`
	Text      string `json:"text"`
	HTML      string `json:"html,omitempty" doc:"The text with syntax highlighting (escaped HTML with token classes)"`
	NoNewline bool   `json:"noNewline,omitempty" doc:"The file does not end with a newline after this line"`
}

// Hunk is a contiguous change with context.
type Hunk struct {
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
	Header   string `json:"header" doc:"Text after the @@ range, usually the enclosing function"`
	Lines    []Line `json:"lines"`
}

// File is the change of one file.
type File struct {
	OldPath    string `json:"oldPath,omitempty"`
	NewPath    string `json:"newPath,omitempty"`
	Status     string `json:"status" enum:"added,deleted,modified,renamed,copied"`
	OldMode    string `json:"oldMode,omitempty"`
	NewMode    string `json:"newMode,omitempty"`
	Similarity int    `json:"similarity,omitempty"`
	Binary     bool   `json:"binary"`
	Additions  int    `json:"additions"`
	Deletions  int    `json:"deletions"`
	Hunks      []Hunk `json:"hunks"`
	Truncated  bool   `json:"truncated" doc:"The file has more changed lines than shown"`
}

// Path returns the new path, or the old one for deleted files.
func (f *File) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Diff is a parsed diff.
type Diff struct {
	Files     []File `json:"files"`
	Truncated bool   `json:"truncated" doc:"More files changed than shown"`
}

// Limits keep diffs fast to show.
type Limits struct {
	Files         int // files shown; the rest is counted only
	LinesPerFile  int
	TotalLines    int
	LineMaxLength int
}

// DefaultLimits are used when a limit is zero.
var DefaultLimits = Limits{Files: 500, LinesPerFile: 5000, TotalLines: 50000, LineMaxLength: 2000}

// Parse parses a patch. Lines over the limits are counted but not kept.
func Parse(patch []byte, lim Limits) *Diff {
	if lim.Files <= 0 {
		lim.Files = DefaultLimits.Files
	}
	if lim.LinesPerFile <= 0 {
		lim.LinesPerFile = DefaultLimits.LinesPerFile
	}
	if lim.TotalLines <= 0 {
		lim.TotalLines = DefaultLimits.TotalLines
	}
	if lim.LineMaxLength <= 0 {
		lim.LineMaxLength = DefaultLimits.LineMaxLength
	}
	d := &Diff{Files: []File{}}
	var f *File
	var h *Hunk
	oldNo, newNo, total, fileLines := 0, 0, 0, 0
	sc := bufio.NewScanner(bytes.NewReader(patch))
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	finish := func() {
		if f == nil {
			return
		}
		if f.Hunks == nil {
			f.Hunks = []Hunk{}
		}
		if len(d.Files) < lim.Files {
			d.Files = append(d.Files, *f)
		} else {
			d.Truncated = true
		}
		f, h = nil, nil
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			finish()
			f = &File{Status: Modified}
			fileLines = 0
			f.OldPath, f.NewPath = splitHeader(strings.TrimPrefix(line, "diff --git "))
			continue
		case f == nil:
			continue
		}
		if h == nil {
			// Extended header lines.
			switch {
			case strings.HasPrefix(line, "new file mode "):
				f.Status, f.NewMode, f.OldPath = Added, strings.TrimPrefix(line, "new file mode "), ""
			case strings.HasPrefix(line, "deleted file mode "):
				f.Status, f.OldMode, f.NewPath = Deleted, strings.TrimPrefix(line, "deleted file mode "), ""
			case strings.HasPrefix(line, "old mode "):
				f.OldMode = strings.TrimPrefix(line, "old mode ")
			case strings.HasPrefix(line, "new mode "):
				f.NewMode = strings.TrimPrefix(line, "new mode ")
			case strings.HasPrefix(line, "similarity index "):
				f.Similarity, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "similarity index "), "%"))
			case strings.HasPrefix(line, "rename from "):
				f.Status, f.OldPath = Renamed, unquote(strings.TrimPrefix(line, "rename from "))
			case strings.HasPrefix(line, "rename to "):
				f.Status, f.NewPath = Renamed, unquote(strings.TrimPrefix(line, "rename to "))
			case strings.HasPrefix(line, "copy from "):
				f.Status, f.OldPath = Copied, unquote(strings.TrimPrefix(line, "copy from "))
			case strings.HasPrefix(line, "copy to "):
				f.Status, f.NewPath = Copied, unquote(strings.TrimPrefix(line, "copy to "))
			case strings.HasPrefix(line, "index "):
				if i := strings.LastIndexByte(line, ' '); i > len("index ") && f.NewMode == "" && f.Status == Modified {
					f.NewMode = line[i+1:]
				}
			case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
				f.Binary = true
			case strings.HasPrefix(line, "--- "):
				if p := strings.TrimPrefix(line, "--- "); p != "/dev/null" {
					f.OldPath = stripPrefix(unquote(p), "a/")
				}
			case strings.HasPrefix(line, "+++ "):
				if p := strings.TrimPrefix(line, "+++ "); p != "/dev/null" {
					f.NewPath = stripPrefix(unquote(p), "b/")
				}
			case strings.HasPrefix(line, "@@ "):
				h = startHunk(f, line, &oldNo, &newNo)
			}
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			h = startHunk(f, line, &oldNo, &newNo)
			continue
		}
		if line == `\ No newline at end of file` {
			if n := len(f.Hunks); n > 0 {
				hk := &f.Hunks[n-1]
				if m := len(hk.Lines); m > 0 {
					hk.Lines[m-1].NoNewline = true
				}
			}
			continue
		}
		if line == "" {
			line = " "
		}
		var l Line
		switch line[0] {
		case '+':
			f.Additions++
			l = Line{Kind: Addition, New: newNo, Text: line[1:]}
			newNo++
		case '-':
			f.Deletions++
			l = Line{Kind: Deletion, Old: oldNo, Text: line[1:]}
			oldNo++
		case ' ':
			l = Line{Kind: Context, Old: oldNo, New: newNo, Text: line[1:]}
			oldNo++
			newNo++
		default:
			continue
		}
		if fileLines >= lim.LinesPerFile || total >= lim.TotalLines {
			f.Truncated = true
			continue
		}
		if len(l.Text) > lim.LineMaxLength {
			l.Text = l.Text[:lim.LineMaxLength]
		}
		hk := &f.Hunks[len(f.Hunks)-1]
		hk.Lines = append(hk.Lines, l)
		fileLines++
		total++
	}
	finish()
	return d
}

func startHunk(f *File, line string, oldNo, newNo *int) *Hunk {
	h := Hunk{Lines: []Line{}}
	rest := strings.TrimPrefix(line, "@@ ")
	ranges, header, _ := strings.Cut(rest, " @@")
	h.Header = strings.TrimPrefix(header, " ")
	for _, r := range strings.Fields(ranges) {
		start, count := parseRange(r[1:])
		switch r[0] {
		case '-':
			h.OldStart, h.OldLines = start, count
		case '+':
			h.NewStart, h.NewLines = start, count
		}
	}
	*oldNo, *newNo = h.OldStart, h.NewStart
	f.Hunks = append(f.Hunks, h)
	return &f.Hunks[len(f.Hunks)-1]
}

func parseRange(s string) (int, int) {
	a, b, ok := strings.Cut(s, ",")
	start, _ := strconv.Atoi(a)
	count := 1
	if ok {
		count, _ = strconv.Atoi(b)
	}
	return start, count
}

func stripPrefix(p, prefix string) string { return strings.TrimPrefix(p, prefix) }

// splitHeader splits "a/x b/y" of a diff header. Paths with spaces are ambiguous unless quoted; the old and new
// paths are usually equal, which resolves the split (renames take their paths from later header lines).
func splitHeader(s string) (string, string) {
	if strings.HasPrefix(s, `"`) {
		old, rest := readQuoted(s)
		rest = strings.TrimPrefix(rest, " ")
		var nw string
		if strings.HasPrefix(rest, `"`) {
			nw, _ = readQuoted(rest)
		} else {
			nw = rest
		}
		return stripPrefix(old, "a/"), stripPrefix(nw, "b/")
	}
	if i := strings.Index(s, ` "b/`); i >= 0 {
		nw, _ := readQuoted(s[i+1:])
		return stripPrefix(s[:i], "a/"), stripPrefix(nw, "b/")
	}
	// a/P b/P: try the split in the middle first.
	if len(s)%2 == 1 {
		half := len(s) / 2
		if s[half] == ' ' && strings.HasPrefix(s, "a/") && s[half+1:half+3] == "b/" && s[2:half] == s[half+3:] {
			return s[2:half], s[half+3:]
		}
	}
	if i := strings.Index(s, " b/"); i >= 0 {
		return stripPrefix(s[:i], "a/"), s[i+3:]
	}
	return s, s
}

// unquote decodes git's C-style quoted paths.
func unquote(s string) string {
	if !strings.HasPrefix(s, `"`) {
		return s
	}
	v, _ := readQuoted(s)
	return v
}

func readQuoted(s string) (string, string) {
	var b []byte
	i := 1
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			return string(b), s[i+1:]
		case c == '\\' && i+1 < len(s):
			i++
			switch e := s[i]; e {
			case 'n':
				b = append(b, '\n')
			case 't':
				b = append(b, '\t')
			case 'r':
				b = append(b, '\r')
			case 'a':
				b = append(b, 7)
			case 'b':
				b = append(b, 8)
			case 'f':
				b = append(b, 12)
			case 'v':
				b = append(b, 11)
			case '0', '1', '2', '3':
				if i+2 < len(s) {
					if n, err := strconv.ParseUint(s[i:i+3], 8, 8); err == nil {
						b = append(b, byte(n))
						i += 2
						break
					}
				}
				b = append(b, e)
			default:
				b = append(b, e)
			}
		default:
			b = append(b, c)
		}
		i++
	}
	return string(b), ""
}
