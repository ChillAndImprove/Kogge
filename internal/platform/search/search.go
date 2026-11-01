// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package search holds what the searches of the products share: the parsed text of the
// quick search and the search page, the things found and how they rank. Each product searches its own tables with
// its own permissions; package links asks them all and merges what they find.
package search

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/platform/db"
)

// MaxLength is the longest text searched for, in characters; longer texts are cut.
const MaxLength = 200

// maxWords is the number of words a search uses; more words find nothing more.
const maxWords = 8

// Query is a text to search for: as typed (trimmed, runs of white space as one space) and as words.
type Query struct {
	Text  string
	Words []string // lower case, letters and digits only, in order, without repetitions
}

// Parse prepares a text for searching.
func Parse(text string) Query {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > MaxLength {
		text = strings.TrimSpace(string([]rune(text)[:MaxLength]))
	}
	q := Query{Text: text}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !slices.Contains(q.Words, w) && len(q.Words) < maxWords {
			q.Words = append(q.Words, w)
		}
	}
	return q
}

// Empty reports whether there is nothing to search for.
func (q Query) Empty() bool { return q.Text == "" }

// Lower is the text in lower case, to compare with lower(title).
func (q Query) Lower() string { return strings.ToLower(q.Text) }

// Prefix returns a text search query (for the simple configuration) that matches texts in which every word of the
// query begins a word: "deploy conf" becomes "deploy:* & conf:*". Words of one letter begin too many words to
// narrow anything down; they are left out unless all words are that short, when Prefix returns "".
func (q Query) Prefix() string {
	var parts []string
	for _, w := range q.PrefixWords() {
		parts = append(parts, w+":*")
	}
	return strings.Join(parts, " & ")
}

// PrefixWords returns the words that Prefix matches the beginnings of.
func (q Query) PrefixWords() []string {
	var out []string
	for _, w := range q.Words {
		if utf8.RuneCountInString(w) > 1 {
			out = append(out, w)
		}
	}
	return out
}

// ShortWords reports whether the words of Prefix all have at most three letters: such beginnings are shared by a
// large part of all titles.
func (q Query) ShortWords() bool {
	short := false
	for _, w := range q.Words {
		switch n := utf8.RuneCountInString(w); {
		case n > 3:
			return false
		case n > 1:
			short = true
		}
	}
	return short
}

// StartsWith returns a LIKE pattern (ESCAPE '\') for lower-case texts that begin with the text.
func (q Query) StartsWith() string { return db.EscapeLike(q.Lower()) + "%" }

// Contains returns a LIKE pattern (ESCAPE '\') for lower-case texts that contain the text.
func (q Query) Contains() string { return "%" + db.EscapeLike(q.Lower()) + "%" }

// Tiers rank how well something matches; lower tiers come first.
const (
	TierExact  = iota // the key, or the whole title
	TierPrefix        // the title begins with the text
	TierWords         // every word begins a word of the title
	TierText          // the text is elsewhere: descriptions, bodies, comments (the search page)
)

// TitleTier rates a title against the query: the whole title, its beginning, or its words.
func TitleTier(q Query, title string) int {
	t := strings.ToLower(strings.Join(strings.Fields(title), " "))
	switch l := q.Lower(); {
	case t == l:
		return TierExact
	case strings.HasPrefix(t, l):
		return TierPrefix
	}
	return TierWords
}

// Kinds of things found.
const (
	KindIssue       = "issue"
	KindProject     = "project"
	KindPage        = "page"
	KindBlog        = "blog"
	KindSpace       = "space"
	KindCodeProject = "code-project"
	KindRepository  = "repository"
	KindPullRequest = "pull-request"
	KindPipelines   = "pipelines"
	KindRun         = "run"
	KindUser        = "user"
)

// Hit is something found.
type Hit struct {
	Kind    string     `json:"kind" enum:"issue,project,page,blog,space,code-project,repository,pull-request,pipelines,run,user"`
	ID      int64      `json:"id"`
	Key     string     `json:"key,omitempty" doc:"Issue key, project or space key, repository path, number of a pull request or run (#12), user name"`
	Title   string     `json:"title"`
	Context string     `json:"context,omitempty" doc:"Where it is: the project, space or repository"`
	URL     string     `json:"url"`
	State   string     `json:"state,omitempty" doc:"Pull requests: draft, open, merged or declined; runs and pipelines: the state of the (newest) run"`
	Status  *Status    `json:"status,omitempty" doc:"The status of an issue"`
	Type    *Type      `json:"type,omitempty" doc:"The type of an issue"`
	Excerpt string     `json:"excerpt,omitempty" doc:"Search page: text around the matches, with matches between \u0001 and \u0002, and … where it is cut"`
	Person  *HitPerson `json:"person,omitempty" doc:"Search page: the assignee of an issue, who changed a page or blog post last, the author of a pull request"`
	Updated *time.Time `json:"updated,omitempty"`
	// Archived things are found after the others.
	Archived bool `json:"archived,omitempty"`
	// Tier and Score rank the hit (higher scores first within a tier); they stay on the server.
	Tier  int     `json:"-"`
	Score float64 `json:"-"`
}

// HitPerson is the person a hit names (HitPerson.Active tells whether they can still sign in).
type HitPerson struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
}

// CollectPeople reads rows of ids and the people of the hits with them: user id, user name, display name and
// whether the user is active.
func CollectPeople(rows pgx.Rows) (map[int64]*HitPerson, error) {
	out := map[int64]*HitPerson{}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p HitPerson
		if err := rows.Scan(&id, &p.ID, &p.Username, &p.DisplayName, &p.Active); err != nil {
			return nil, err
		}
		out[id] = &p
	}
	return out, rows.Err()
}

// Status is the status of an issue.
type Status struct {
	Name     string `json:"name"`
	Category string `json:"category" enum:"todo,inprogress,done"`
}

// Type is the type of an issue.
type Type struct {
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

// Compare orders hits: by tier, archived ones after the others, by score, the more recently updated first, and by
// title.
func Compare(a, b Hit) int {
	if c := cmp.Compare(a.Tier, b.Tier); c != 0 {
		return c
	}
	if a.Archived != b.Archived {
		if a.Archived {
			return 1
		}
		return -1
	}
	if c := cmp.Compare(b.Score, a.Score); c != 0 {
		return c
	}
	var at, bt time.Time
	if a.Updated != nil {
		at = *a.Updated
	}
	if b.Updated != nil {
		bt = *b.Updated
	}
	if c := bt.Compare(at); c != 0 {
		return c
	}
	if c := cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// Sort orders hits best first.
func Sort(hits []Hit) { slices.SortStableFunc(hits, Compare) }

// Request is a search for one kind of thing.
type Request struct {
	Query Query
	// Text searches the texts too (descriptions, bodies, comments), as the search page does; the quick search
	// matches titles and keys only.
	Text   bool
	Limit  int
	Offset int
	// Count asks for the number of all matches.
	Count bool
}

// Result is a page of hits of one kind.
type Result struct {
	Hits  []Hit
	Total int // with Count
}

// Candidates is how many of the matching titles a quick search ranks: the most recently updated ones, so that a
// word that begins many titles costs no more than a rare one.
const Candidates = 200

// Split lays out a page of the search page, whose list is the ranked title hits (n of them) followed by the other
// text hits, newest first: the page shows titles[from:to] and then textLimit text hits from textOffset on. Ranking
// every match of a text by relevance would cost as much as there are matches; the titles are what people look
// for, and the rest is listed as the issue navigator lists it.
func Split(n, offset, limit int) (from, to, textOffset, textLimit int) {
	if offset < n {
		from, to = offset, min(offset+limit, n)
		return from, to, 0, limit - (to - from)
	}
	return n, n, offset - n, limit
}

// HeadlineOptions are the options of ts_headline for excerpts: matches between \u0001 and \u0002, as the web
// application shows them, and the parts of the text around them separated by an ellipsis.
const HeadlineOptions = "StartSel=\u0001, StopSel=\u0002, MaxWords=35, MinWords=15, MaxFragments=2, FragmentDelimiter=\" … \""

// HeadlineEnds returns the select list that follows the ts_headline of a text column for CollectExcerpts: the
// text's beginning, in which Headline finds the parts of the excerpt, and its last characters.
func HeadlineEnds(column string) string {
	return "left(" + column + ", " + strconv.Itoa(headLength) + "), right(" + column + ", " + strconv.Itoa(tailLength) + ")"
}

// headLength and tailLength are how many characters of the beginning and the end of a text Headline reads.
const (
	headLength = 4000
	tailLength = 80
)

// CollectExcerpts reads rows of ids, ts_headlines and the ends of their texts (HeadlineEnds) into finished
// excerpts (Headline).
func CollectExcerpts(rows pgx.Rows) (map[int64]string, error) {
	out := map[int64]string{}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var headline, head, tail string
		if err := rows.Scan(&id, &headline, &head, &tail); err != nil {
			return nil, err
		}
		out[id] = Headline(headline, head, tail)
	}
	return out, rows.Err()
}

// fragmentDelimiter separates the fragments of a headline (HeadlineOptions).
const fragmentDelimiter = " " + docmodel.Ellipsis + " "

var unmark = strings.NewReplacer("\u0001", "", "\u0002", "")

// Headline finishes an excerpt that ts_headline took from a text, of which head is the beginning (headLength
// characters, the whole text when shorter) and tail the end: words that ts_headline cut where they are joined by a
// hyphen ("re-encrypts") are completed, the lines of the text, its blocks, become clauses that do not run
// together, and an ellipsis marks where the excerpt begins after the beginning of the text or ends before its
// end.
func Headline(headline, head, tail string) string {
	if strings.TrimSpace(headline) == "" {
		return ""
	}
	whole := utf8.RuneCountInString(head) < headLength
	frags := strings.Split(headline, fragmentDelimiter)
	// Whether the excerpt begins and ends where the text does; unknown (nil) for parts beyond head.
	var begins, ends *bool
	for i, f := range frags {
		plain := unmark.Replace(f)
		at := strings.Index(head, plain)
		if plain == "" || at < 0 {
			continue
		}
		before, after := head[:at], head[at+len(plain):]
		pre, post := wordBefore(before, plain), wordAfter(after, plain)
		frags[i] = pre + f + post
		if i == 0 {
			// Only white space and punctuation before it: it begins the text.
			b := strings.IndexFunc(before[:len(before)-len(pre)], isWordRune) < 0
			begins = &b
		}
		if i == len(frags)-1 {
			switch more := strings.IndexFunc(after[len(post):], isWordRune) >= 0; {
			case more:
				e := false
				ends = &e
			case whole:
				e := true
				ends = &e
			}
		}
	}
	lines := strings.Split(strings.Join(frags, fragmentDelimiter), "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	out := docmodel.JoinClauses(lines)
	if out == "" {
		return ""
	}
	// A delimiter between fragments that fell on the end of a line is no clause of its own.
	out = strings.ReplaceAll(out, " · "+docmodel.Ellipsis+" · ", " "+docmodel.Ellipsis+" ")
	// Parts beyond head are compared with the ends of the text; ts_headline leaves out the punctuation around its
	// fragments, so only letters and digits count.
	if begins == nil {
		b := sameRunes(letters(frags[0]), letters(head), false)
		begins = &b
	}
	if ends == nil {
		e := sameRunes(letters(frags[len(frags)-1]), letters(tail), true)
		ends = &e
	}
	if !*begins {
		out = docmodel.Ellipsis + out
	}
	if !*ends {
		out += docmodel.Ellipsis
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordBefore returns the part of a word that ends text when part, which follows it, begins within the word:
// letters and digits joined by hyphens or apostrophes.
func wordBefore(text, part string) string {
	first, _ := utf8.DecodeRuneInString(part)
	if !isWordRune(first) {
		return ""
	}
	i := len(text)
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(text[:i])
		if isWordRune(r) {
			i -= n
			continue
		}
		if r == '-' || r == '\'' {
			// A joiner belongs to the word if a letter or digit comes before it.
			if p, _ := utf8.DecodeLastRuneInString(text[:i-n]); i-n > 0 && isWordRune(p) {
				i -= n
				continue
			}
		}
		break
	}
	return text[i:]
}

// wordAfter returns the rest of a word that begins text when part, which comes before it, ends within the word.
func wordAfter(text, part string) string {
	last, _ := utf8.DecodeLastRuneInString(unmark.Replace(part))
	if !isWordRune(last) {
		return ""
	}
	i := 0
	for i < len(text) {
		r, n := utf8.DecodeRuneInString(text[i:])
		if isWordRune(r) {
			i += n
			continue
		}
		if r == '-' || r == '\'' {
			if next, _ := utf8.DecodeRuneInString(text[i+n:]); i+n < len(text) && isWordRune(next) {
				i += n
				continue
			}
		}
		break
	}
	return text[:i]
}

// letters returns the letters and digits of a text, without the marks of matches.
func letters(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if isWordRune(r) {
			out = append(out, r)
		}
	}
	return out
}

// sameRunes reports whether a and b begin (or end, with fromEnd) alike, compared over the shorter of them and at
// most tailLength/2 characters.
func sameRunes(a, b []rune, fromEnd bool) bool {
	n := min(len(a), len(b), tailLength/2)
	if fromEnd {
		return string(a[len(a)-n:]) == string(b[len(b)-n:])
	}
	return string(a[:n]) == string(b[:n])
}

// LimitOr returns the request's limit, def when it has none, and at most most.
func (r Request) LimitOr(def, most int) int {
	if r.Limit <= 0 {
		return def
	}
	return min(r.Limit, most)
}
