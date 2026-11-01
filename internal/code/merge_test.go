// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/testutil/apitest"
)

// logEntry is a commit as git log shows it.
type logEntry struct {
	SHA, Parents, AuthorName, AuthorEmail, AuthorDate, CommitterName, CommitterEmail, Message string
}

// history lists the commits of rev that are not in exclude, children before their parents (merges write commits
// in the same second).
func (g *gitClient) history(dir, rev, exclude string) []logEntry {
	g.t.Helper()
	out := g.must(dir, "log", "--topo-order", "--date=raw", "--format=%H%x00%P%x00%an%x00%ae%x00%ad%x00%cn%x00%ce%x00%B%x1e", rev, "^"+exclude, "--")
	var list []logEntry
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x00")
		if len(f) != 8 {
			continue
		}
		list = append(list, logEntry{f[0], f[1], f[2], f[3], f[4], f[5], f[6], strings.TrimRight(f[7], "\n")})
	}
	return list
}

// hookPayload is what the tests read of webhook deliveries.
type hookPayload struct {
	Event       string `json:"event"`
	PullRequest *struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	} `json:"pullRequest"`
	Push *struct {
		Updates []struct {
			Ref    string `json:"ref"`
			Before string `json:"before"`
			After  string `json:"after"`
		} `json:"updates"`
	} `json:"push"`
}

type hookReceiver struct {
	got chan hookPayload
}

func (h *hookReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var p hookPayload
	_ = json.NewDecoder(r.Body).Decode(&p)
	h.got <- p
	w.WriteHeader(http.StatusOK)
}

// drain returns the deliveries received so far.
func (h *hookReceiver) drain() []hookPayload {
	var out []hookPayload
	for {
		select {
		case p := <-h.got:
			out = append(out, p)
		default:
			return out
		}
	}
}

type strategyCheck struct {
	Strategy string   `json:"strategy"`
	OK       bool     `json:"ok"`
	Reason   string   `json:"reason"`
	Commit   string   `json:"commit"`
	Subject  string   `json:"subject"`
	Paths    []string `json:"paths"`
	Message  string   `json:"message"`
}

type mergeView struct {
	Number     int             `json:"number"`
	State      string          `json:"state"`
	MergeSHA   string          `json:"mergeSha"`
	Version    int             `json:"version"`
	Strategies []strategyCheck `json:"strategies"`
	Checks     []struct {
		Key    string `json:"key"`
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	} `json:"checks"`
	MergeMessages *struct {
		Merge  string `json:"merge"`
		Squash string `json:"squash"`
	} `json:"mergeMessages"`
}

func (v mergeView) strategy(name string) strategyCheck {
	for _, s := range v.Strategies {
		if s.Strategy == name {
			return s
		}
	}
	return strategyCheck{}
}

// TestMergeStrategies merges a pull request with every strategy and checks the history each makes, who authored and
// committed its commits and their messages, that the source branch goes when asked, and that the merge is processed
// like a push for every strategy: webhooks, and the issue the commits name.
func TestMergeStrategies(t *testing.T) {
	env := apitest.New(t, func(c *config.Config) { c.WebhookLoopback = true })
	admin := env.Admin()
	dev, mia := env.CreateUser("dev"), env.CreateUser("mia")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"admin": admin, "dev": dev, "mia": mia} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	// The issue the commits name.
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "kanban"}, nil)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/SHOP/issue-types", &types)
	var issue struct {
		Key string `json:"key"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": types[0].ID, "summary": "Strategies"}, &issue)

	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "MRG", "name": "Merges"}, nil)
	var repo repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/MRG/repos", map[string]any{"name": "app", "readme": true}, &repo)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/MRG/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": ids["admin"]}},
		{"permission": "repo.write", "holder": map[string]any{"type": "user", "id": ids["dev"]}},
		{"permission": "repo.write", "holder": map[string]any{"type": "user", "id": ids["mia"]}},
	}, nil)
	settings := map[string]any{"mergeStrategies": []string{"merge", "squash", "fast-forward", "rebase", "rebase-merge"},
		"requiredApprovals": 0, "noNeedsWork": true, "tasksResolved": true, "requiredBuilds": 0, "resetApprovals": false,
		"deleteSourceBranch": false, "defaultReviewers": []int64{}}
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/MRG/app", map[string]any{"settings": settings}, nil)
	if r := admin.Do(http.MethodPatch, "/api/v1/code/repos/MRG/app", map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"octopus"}, "defaultReviewers": []int64{}}}); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("an unknown strategy: %d %s", r.Status, r.Body)
	}
	rcv := &hookReceiver{got: make(chan hookPayload, 200)}
	target := httptest.NewServer(rcv)
	defer target.Close()
	admin.MustDo(http.MethodPost, "/api/v1/webhooks?scope=repository&key=MRG/app", map[string]any{"name": "CI", "url": target.URL,
		"events": []string{"repository.pushed", "pull-request.merged"}, "active": true}, nil)

	var token struct {
		Token string `json:"token"`
	}
	dev.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read", "write"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("dev", token.Token)
	// Anna wrote the commits (dev pushes them): authors must stay, dates with their zone.
	anna := newGit(t, "GIT_AUTHOR_NAME=Anna Author", "GIT_AUTHOR_EMAIL=anna@example.com", "GIT_AUTHOR_DATE=1767261600 +0200")
	work := t.TempDir()
	anna.must(work, "clone", u.String(), "app")
	dir := filepath.Join(work, "app")

	for i, c := range []struct {
		name, strategy string
		message        string // given in the merge; "" for the default
		deleteSource   bool
		moveTarget     bool // the target gets a commit of its own first
	}{
		{name: "merge", strategy: "merge", message: "Merge it\n\nBecause.\n", moveTarget: true},
		{name: "squash", strategy: "squash", deleteSource: true, moveTarget: true},
		{name: "squash-edited", strategy: "squash", message: "Squashed in one\n\nAs written.\n", moveTarget: true},
		{name: "fast-forward", strategy: "fast-forward", deleteSource: true},
		{name: "rebase", strategy: "rebase", deleteSource: true, moveTarget: true},
		{name: "rebase-merge", strategy: "rebase-merge", moveTarget: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			branch := "feature/" + c.name
			anna.must(dir, "fetch", "origin")
			anna.must(dir, "checkout", "-q", "-B", branch, "origin/main")
			write(t, filepath.Join(dir, c.name+"-1.txt"), "one\n")
			anna.must(dir, "add", ".")
			anna.must(dir, "commit", "-q", "-m", "First "+c.name+"\n\nWith a body.")
			write(t, filepath.Join(dir, c.name+"-2.txt"), "two\n")
			anna.must(dir, "add", ".")
			anna.must(dir, "commit", "-q", "-m", issue.Key+" Second "+c.name)
			anna.must(dir, "push", "-q", "origin", branch)
			if c.moveTarget {
				admin.MustDo(http.MethodPut, "/api/v1/code/repos/MRG/app/files", map[string]any{"branch": "main", "path": "main-" + c.name + ".txt",
					"content": "main\n"}, nil)
			}
			var pr mergeView
			dev.MustDo(http.MethodPost, "/api/v1/code/repos/MRG/app/pull-requests", map[string]any{"title": issue.Key + " Feature " + c.name,
				"source": branch}, &pr)
			env.WorkJobs()
			prPath := apitest.Fmt("/api/v1/code/repos/MRG/app/pull-requests/%d", pr.Number)
			mia.Get(prPath, &pr)
			if ff := pr.strategy("fast-forward"); ff.OK == c.moveTarget || !c.moveTarget && ff.Reason != "ok" || c.moveTarget && ff.Reason != "diverged" {
				t.Errorf("fast-forward check = %+v", ff)
			}
			for _, s := range []string{"merge", "squash", "rebase", "rebase-merge"} {
				if !pr.strategy(s).OK {
					t.Errorf("%s check = %+v", s, pr.strategy(s))
				}
			}
			if pr.MergeMessages == nil || !strings.HasPrefix(pr.MergeMessages.Merge, apitest.Fmt("Merge pull request #%d from %s into main", pr.Number, branch)) ||
				!strings.Contains(pr.MergeMessages.Squash, "* "+issue.Key+" Second "+c.name+"\n") ||
				!strings.Contains(pr.MergeMessages.Squash, "Co-authored-by: Anna Author <anna@example.com>") {
				t.Errorf("merge messages = %+v", pr.MergeMessages)
			}
			anna.must(dir, "fetch", "origin")
			oldMain := strings.TrimSpace(anna.must(dir, "rev-parse", "origin/main"))
			source := strings.TrimSpace(anna.must(dir, "rev-parse", "origin/"+branch))
			rcv.drain()

			var merged mergeView
			mia.MustDo(http.MethodPost, prPath+"/merge", map[string]any{"strategy": c.strategy, "message": c.message,
				"deleteSource": c.deleteSource, "version": pr.Version}, &merged)
			env.WorkJobs()
			anna.must(dir, "fetch", "-q", "--prune", "origin")
			tip := strings.TrimSpace(anna.must(dir, "rev-parse", "origin/main"))
			if merged.State != "merged" || merged.MergeSHA != tip {
				t.Fatalf("merged = %+v, main at %s", merged, tip)
			}
			_, err := anna.run(dir, "rev-parse", "--verify", "-q", "origin/"+branch)
			if gone := err != nil; gone != c.deleteSource {
				t.Errorf("source branch deleted = %v, want %v", gone, c.deleteSource)
			}
			log := anna.history(dir, tip, oldMain)
			parents := func(e logEntry) []string { return strings.Fields(e.Parents) }
			byAnna := func(e logEntry) bool {
				return e.AuthorName == "Anna Author" && e.AuthorEmail == "anna@example.com" && e.AuthorDate == "1767261600 +0200"
			}
			byMia := func(e logEntry) bool { return e.CommitterName == "Mia" && e.CommitterEmail == "mia@example.com" }
			switch c.name {
			case "merge":
				if len(log) != 3 || !slices.Equal(parents(log[0]), []string{oldMain, source}) || log[0].Message != "Merge it\n\nBecause." ||
					log[0].AuthorName != "Mia" || !byMia(log[0]) || !byAnna(log[1]) || log[1].SHA != source {
					t.Errorf("merge commit = %+v", log)
				}
			case "squash":
				if len(log) != 1 || !slices.Equal(parents(log[0]), []string{oldMain}) || log[0].AuthorName != "Dev" ||
					log[0].AuthorEmail != "dev@example.com" || !byMia(log[0]) ||
					log[0].Message != apitest.Fmt("%s Feature squash (#%d)\n\n* First squash\n* %s Second squash\n\nCo-authored-by: Anna Author <anna@example.com>",
						issue.Key, pr.Number, issue.Key) {
					t.Errorf("squash = %+v", log)
				}
			case "squash-edited":
				if len(log) != 1 || log[0].Message != "Squashed in one\n\nAs written." || log[0].AuthorName != "Dev" || !byMia(log[0]) {
					t.Errorf("squash with its own message = %+v", log)
				}
			case "fast-forward":
				if tip != source || len(log) != 2 || !byAnna(log[0]) || log[0].CommitterName != "Dev" {
					t.Errorf("fast-forward = %+v", log)
				}
			case "rebase":
				if len(log) != 2 || !slices.Equal(parents(log[1]), []string{oldMain}) || !slices.Equal(parents(log[0]), []string{log[1].SHA}) ||
					!byAnna(log[0]) || !byAnna(log[1]) || !byMia(log[0]) || !byMia(log[1]) || log[0].SHA == source ||
					log[1].Message != "First rebase\n\nWith a body." || log[0].Message != issue.Key+" Second rebase" {
					t.Errorf("rebase = %+v", log)
				}
			case "rebase-merge":
				if len(log) != 3 || len(parents(log[0])) != 2 || parents(log[0])[0] != oldMain || parents(log[0])[1] != log[1].SHA ||
					!slices.Equal(parents(log[2]), []string{oldMain}) || !byMia(log[0]) || log[0].AuthorName != "Mia" ||
					!strings.HasPrefix(log[0].Message, apitest.Fmt("Merge pull request #%d from %s into main", pr.Number, branch)) ||
					!byAnna(log[1]) || !byMia(log[1]) || !byAnna(log[2]) || log[1].SHA == source {
					t.Errorf("rebase and merge commit = %+v", log)
				}
			}
			if got := strings.TrimSpace(anna.must(dir, "show", tip+":"+c.name+"-2.txt")); got != "two" {
				t.Errorf("the change is not in main: %q", got)
			}

			// The merge is processed like a push, whatever the strategy.
			var pushed, prMerged bool
			for _, p := range rcv.drain() {
				switch {
				case p.Event == "pull-request.merged" && p.PullRequest != nil && p.PullRequest.Number == pr.Number && p.PullRequest.State == "merged":
					prMerged = true
				case p.Event == "repository.pushed" && p.Push != nil:
					for _, up := range p.Push.Updates {
						if up.Ref == "refs/heads/main" && up.Before == oldMain && up.After == tip {
							pushed = true
						}
					}
				}
			}
			if !pushed || !prMerged {
				t.Errorf("webhooks: pushed %v, merged %v", pushed, prMerged)
			}
			var d struct {
				Commits []struct {
					SHA string `json:"sha"`
				} `json:"commits"`
				PullRequests []struct {
					Number int    `json:"number"`
					State  string `json:"state"`
				} `json:"pullRequests"`
			}
			admin.Get("/api/v1/issues/"+issue.Key+"/development", &d)
			// Every commit the merge brought to main that names the issue is linked to it (new ones too).
			naming, linked := 0, 0
			for _, e := range log {
				if !strings.Contains(e.Message, issue.Key) {
					continue
				}
				naming++
				for _, cm := range d.Commits {
					if cm.SHA == e.SHA {
						linked++
					}
				}
			}
			prLinked := false
			for _, p := range d.PullRequests {
				prLinked = prLinked || p.Number == pr.Number && p.State == "merged"
			}
			// (A message of one's own need not name the issue; the generated ones and the commits do.)
			if naming == 0 && c.name != "squash-edited" || linked != naming || !prLinked {
				t.Errorf("development of %s: %d of %d commits linked, the pull request %v (%+v)", issue.Key, linked, naming, prLinked, d)
			}
			var timeline []struct {
				Kind string         `json:"kind"`
				Data map[string]any `json:"data"`
			}
			dev.Get(prPath+"/timeline", &timeline)
			found := false
			for _, a := range timeline {
				found = found || a.Kind == "merged" && a.Data["strategy"] == c.strategy && a.Data["commit"] == tip
			}
			if !found {
				t.Errorf("no merge in the timeline: %+v", timeline)
			}
		})
		if t.Failed() {
			t.Fatalf("strategy %d failed", i)
		}
	}
}

// TestRebaseConflicts: a rebase whose commits do not apply one by one is refused — although the branches merge
// cleanly — with a message that names the commit and its files, in the reader's language, and leaves the
// repository as it was; the pull request can still be merged otherwise. Merge checks that fail are worded in German
// too.
func TestRebaseConflicts(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &me)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "RBC", "name": "Rebases"}, nil)
	var repo repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/RBC/repos", map[string]any{"name": "app", "readme": true}, &repo)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/RBC/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": me.User.ID}}}, nil)
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/RBC/app", map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"rebase", "rebase-merge", "merge"}, "requiredApprovals": 1, "noNeedsWork": true, "tasksResolved": true,
		"requiredBuilds": 0, "resetApprovals": false, "deleteSourceBranch": true, "defaultReviewers": []int64{}}}, nil)
	files := "/api/v1/code/repos/RBC/app/files"
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "path": "rate.txt", "content": "rate = 1\n"}, nil)
	// The branch changes the line and changes it back: merged as a whole it changes nothing there, but its first
	// commit does not apply once main changed the line.
	var first struct {
		SHA string `json:"sha"`
	}
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "newBranch": "flip", "path": "rate.txt", "content": "rate = 2\n",
		"message": "Try another rate"}, &first)
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "flip", "path": "rate.txt", "content": "rate = 1\n", "message": "Back"}, nil)
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "flip", "path": "notes.txt", "content": "notes\n"}, nil)
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "path": "rate.txt", "content": "rate = 3\n"}, nil)
	var pr mergeView
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/RBC/app/pull-requests", map[string]any{"title": "Flip", "source": "flip"}, &pr)
	env.WorkJobs()
	prPath := apitest.Fmt("/api/v1/code/repos/RBC/app/pull-requests/%d", pr.Number)

	// Merge checks that fail, in English and in German.
	r := admin.Do(http.MethodPost, prPath+"/merge", map[string]any{"strategy": "merge"})
	if r.Code() != "pull-request.checks" || !strings.Contains(string(r.Body), "The pull request cannot be merged: 0 of 1 required approval.") {
		t.Errorf("merge without approval: %d %s", r.Status, r.Body)
	}
	r = admin.Do(http.MethodPost, prPath+"/merge", map[string]any{"strategy": "merge"}, "Accept-Language", "de")
	if !strings.Contains(string(r.Body), "Der Pull-Request kann nicht gemergt werden: 0 von 1 benötigten Genehmigung.") {
		t.Errorf("merge without approval, in German: %d %s", r.Status, r.Body)
	}
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/RBC/app", map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"rebase", "rebase-merge", "merge"}, "requiredApprovals": 0, "noNeedsWork": true, "tasksResolved": true,
		"requiredBuilds": 0, "resetApprovals": false, "deleteSourceBranch": true, "defaultReviewers": []int64{}}}, nil)

	admin.Get(prPath, &pr)
	for _, c := range pr.Checks {
		if c.Key == "conflicts" && !c.OK {
			t.Fatalf("the branches should merge cleanly: %+v", pr.Checks)
		}
	}
	for _, s := range []string{"rebase", "rebase-merge"} {
		if c := pr.strategy(s); c.OK || c.Reason != "conflicted" || c.Commit != first.SHA || c.Subject != "Try another rate" ||
			!slices.Equal(c.Paths, []string{"rate.txt"}) {
			t.Errorf("%s check = %+v", s, c)
		}
	}
	if c := pr.strategy("merge"); !c.OK {
		t.Errorf("merge check = %+v", c)
	}
	refs := func() string {
		var branches struct {
			Items []struct {
				Name   string `json:"name"`
				Commit struct {
					SHA string `json:"sha"`
				} `json:"commit"`
			} `json:"items"`
		}
		admin.Get("/api/v1/code/repos/RBC/app/branches", &branches)
		var out []string
		for _, b := range branches.Items {
			out = append(out, b.Name+"="+b.Commit.SHA)
		}
		slices.Sort(out)
		return strings.Join(out, " ")
	}
	before := refs()
	for _, s := range []string{"rebase", "rebase-merge"} {
		r := admin.Do(http.MethodPost, prPath+"/merge", map[string]any{"strategy": s})
		if r.Status != http.StatusConflict || r.Code() != "pull-request.rebase-conflict" ||
			!strings.Contains(string(r.Body), fmt.Sprintf("The commit %s (Try another rate) cannot be rebased onto main: it conflicts in rate.txt.", first.SHA[:10])) {
			t.Errorf("%s merge: %d %s", s, r.Status, r.Body)
		}
	}
	r = admin.Do(http.MethodPost, prPath+"/merge", map[string]any{"strategy": "rebase"}, "Accept-Language", "de")
	if !strings.Contains(string(r.Body), fmt.Sprintf("Der Commit %s (Try another rate) lässt sich nicht auf main rebasen: Er hat Konflikte in rate.txt.", first.SHA[:10])) {
		t.Errorf("rebase conflict in German: %s", r.Body)
	}
	if after := refs(); after != before {
		t.Errorf("the refused rebase changed branches:\n%s\n%s", before, after)
	}
	admin.Get(prPath, &pr)
	if pr.State != "open" {
		t.Fatalf("state after the refused rebases = %s", pr.State)
	}
	// A merge commit merges what the rebase could not.
	var merged mergeView
	admin.MustDo(http.MethodPost, prPath+"/merge", map[string]any{"strategy": "merge"}, &merged)
	if merged.State != "merged" {
		t.Errorf("merge after the refused rebase: %+v", merged)
	}
}

// looseObjects counts the objects of a repository that are not in packs: what rebases, merges and checks write.
func looseObjects(t *testing.T, repoDir string) int {
	t.Helper()
	dirs, err := os.ReadDir(filepath.Join(repoDir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range dirs {
		if !d.IsDir() || len(d.Name()) != 2 {
			continue // info, pack
		}
		files, err := os.ReadDir(filepath.Join(repoDir, "objects", d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		n += len(files)
	}
	return n
}

// TestRebaseCheckOnce: whether a pull request's commits rebase is found out once for the branches as they are,
// not on every view of it — a rebase run again would write commits of its own, dated anew — and merging still
// rebases for real, signed by who merges.
func TestRebaseCheckOnce(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	var me struct {
		User struct {
			ID          int64  `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &me)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "RCO", "name": "Once"}, nil)
	var repo repository
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/RCO/repos", map[string]any{"name": "app", "readme": true}, &repo)
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/RCO/permissions", []map[string]any{
		{"permission": "codeproject.admin", "holder": map[string]any{"type": "user", "id": me.User.ID}}}, nil)
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/RCO/app", map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"rebase", "rebase-merge"}, "requiredApprovals": 0, "noNeedsWork": true, "tasksResolved": true,
		"requiredBuilds": 0, "resetApprovals": false, "deleteSourceBranch": false, "defaultReviewers": []int64{}}}, nil)
	files := "/api/v1/code/repos/RCO/app/files"
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "newBranch": "feature", "path": "a.txt", "content": "a\n"}, nil)
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "feature", "path": "b.txt", "content": "b\n"}, nil)
	// main moves on: rebasing the two commits makes new ones.
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "path": "m.txt", "content": "m\n"}, nil)
	var pr mergeView
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/RCO/app/pull-requests", map[string]any{"title": "Feature", "source": "feature"}, &pr)
	env.WorkJobs()
	prPath := apitest.Fmt("/api/v1/code/repos/RCO/app/pull-requests/%d", pr.Number)
	view := func() {
		t.Helper()
		var got mergeView
		admin.Get(prPath, &got)
		for _, s := range []string{"rebase", "rebase-merge"} {
			if c := got.strategy(s); !c.OK {
				t.Fatalf("%s check = %+v", s, c)
			}
		}
	}
	dir := code.RepoDir(env.App.Cfg.DataDir, repo.ID)
	view()
	before := looseObjects(t, dir)
	// Commits are dated to the second: a rebase run again from now on would write other commits.
	time.Sleep(1100 * time.Millisecond)
	view()
	view()
	if after := looseObjects(t, dir); after != before {
		t.Errorf("views of the pull request wrote %d objects: the rebase ran again", after-before)
	}

	// The merge rebases anew: its commits are the merger's, not those of the check.
	var merged mergeView
	admin.MustDo(http.MethodPost, prPath+"/merge", map[string]any{"strategy": "rebase"}, &merged)
	var log struct {
		Commits []struct {
			Subject   string `json:"subject"`
			Committer struct {
				Name string `json:"name"`
			} `json:"committer"`
		} `json:"commits"`
	}
	admin.Get("/api/v1/code/repos/RCO/app/commits?ref=main&limit=2", &log)
	if merged.State != "merged" || len(log.Commits) != 2 || log.Commits[0].Subject != "Update b.txt" || log.Commits[1].Subject != "Update a.txt" {
		t.Fatalf("merged = %+v, main = %+v", merged, log.Commits)
	}
	for _, c := range log.Commits {
		if c.Committer.Name != me.User.DisplayName {
			t.Errorf("%s committed by %q, want the merger %q", c.Subject, c.Committer.Name, me.User.DisplayName)
		}
	}
}
