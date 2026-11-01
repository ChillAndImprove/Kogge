// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/testutil/apitest"
)

const bbToken = "bitbucket-test-token"

// lfsContent is the file the source keeps in Git LFS.
const lfsContent = "LFS content of the logo\n"

func bbGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Jane Doe", "GIT_AUTHOR_EMAIL=jdoe@example.org", "GIT_COMMITTER_NAME=Jane Doe",
		"GIT_COMMITTER_EMAIL=jdoe@example.org", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bbSource is a Bitbucket Data Center as far as the migration sees it: the REST API, Git over HTTP (git
// http-backend) and LFS.
type bbSource struct {
	*httptest.Server
	work, bare string
	mu         sync.Mutex
	sha        map[string]string // commits by name
}

func (s *bbSource) commit(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sha[name]
}

func newBitbucket(t *testing.T) *bbSource {
	root := t.TempDir()
	s := &bbSource{work: filepath.Join(root, "work"), bare: filepath.Join(root, "git", "shop", "web.git"), sha: map[string]string{}}
	w := s.work
	if err := os.MkdirAll(filepath.Join(w, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(w, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte(lfsContent))
	oid := hex.EncodeToString(sum[:])
	bbGit(t, root, "init", "-q", "-b", "main", w)
	write("README.md", "# Web\n")
	write("src/app.go", "package app\n\nfunc A() {}\n")
	write("assets/logo.bin", "version https://git-lfs.github.com/spec/v1\noid sha256:"+oid+"\nsize "+strconv.Itoa(len(lfsContent))+"\n")
	bbGit(t, w, "add", ".")
	bbGit(t, w, "commit", "-q", "-m", "Start")
	s.sha["c1"] = bbGit(t, w, "rev-parse", "HEAD")
	bbGit(t, w, "checkout", "-q", "-b", "feature")
	write("src/app.go", "package app\n\nfunc A() {}\n\nfunc B() {}\n")
	bbGit(t, w, "commit", "-q", "-am", "Add B")
	s.sha["c2"] = bbGit(t, w, "rev-parse", "HEAD")
	bbGit(t, w, "checkout", "-q", "main")
	bbGit(t, w, "merge", "-q", "--no-ff", "-m", "Merge feature", "feature")
	s.sha["c3"] = bbGit(t, w, "rev-parse", "HEAD")
	bbGit(t, w, "tag", "v1.0")
	bbGit(t, w, "checkout", "-q", "-b", "fix")
	write("README.md", "# Web\n\nFixed.\n")
	bbGit(t, w, "commit", "-q", "-am", "Fix README\n\nDetails")
	s.sha["c4"] = bbGit(t, w, "rev-parse", "HEAD")
	bbGit(t, w, "checkout", "-q", "-b", "old-idea", s.sha["c1"])
	write("idea.txt", "idea\n")
	bbGit(t, w, "add", ".")
	bbGit(t, w, "commit", "-q", "-m", "An idea")
	s.sha["c5"] = bbGit(t, w, "rev-parse", "HEAD")
	bbGit(t, w, "checkout", "-q", "-b", "fork-work", s.sha["c3"])
	write("fork.txt", "fork\n")
	bbGit(t, w, "add", ".")
	bbGit(t, w, "commit", "-q", "-m", "Work in a fork")
	s.sha["c6"] = bbGit(t, w, "rev-parse", "HEAD")
	bbGit(t, w, "checkout", "-q", "main")
	bbGit(t, root, "clone", "-q", "--mirror", w, s.bare)
	// The source keeps the commits of pull requests under refs/pull-requests/, also of deleted branches and forks.
	for n, c := range map[string]string{"1": "c2", "2": "c4", "3": "c5", "4": "c6"} {
		bbGit(t, s.bare, "update-ref", "refs/pull-requests/"+n+"/from", s.sha[c])
	}
	bbGit(t, s.bare, "branch", "-D", "old-idea", "fork-work")
	s.sha["fix"] = s.sha["c4"]

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: gitBin, Args: []string{"http-backend"}, Root: "/scm",
		Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(root, "git"), "GIT_HTTP_EXPORT_ALL=1"}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+bbToken {
			http.Error(w, `{"errors":[{"message":"Authentication failed"}]}`, http.StatusUnauthorized)
			return
		}
		s.serve(t, w, r, backend, oid)
	}))
	t.Cleanup(s.Close)
	return s
}

const (
	bbJdoe    = `{"name":"jdoe","slug":"jdoe","displayName":"Jane Doe","emailAddress":"jane.doe@example.org","active":true,"type":"NORMAL"}`
	bbMmuster = `{"name":"mmuster","slug":"mmuster","displayName":"Max Muster","emailAddress":"mmuster@example.com","active":true,"type":"NORMAL"}`
	bbBot     = `{"name":"ci-bot","slug":"ci-bot","displayName":"CI","active":true,"type":"SERVICE"}`
)

func (s *bbSource) serve(t *testing.T, w http.ResponseWriter, r *http.Request, backend http.Handler, oid string) {
	page := func(values ...string) {
		// Two values per page, whatever the client asks for: the extraction must follow the pages.
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		start = min(start, len(values))
		end := min(start+2, len(values))
		fmt.Fprintf(w, `{"size":%d,"limit":2,"start":%d,"isLastPage":%v,"nextPageStart":%d,"values":[%s]}`, end-start, start,
			end == len(values), end, strings.Join(values[start:end], ","))
	}
	ms := func(day int) string { return strconv.FormatInt(int64(1767225600000+day*86400000), 10) } // 2026-01-01 + days
	ref := func(branch, commit string) string {
		return `{"id":"refs/heads/` + branch + `","displayId":"` + branch + `","latestCommit":"` + s.commit(commit) +
			`","repository":{"id":7,"slug":"web","project":{"key":"SHOP"}}}`
	}
	r2 := strings.NewReplacer("{{JDOE}}", bbJdoe, "{{MMUSTER}}", bbMmuster)
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/scm/shop/web.git/info/lfs/objects/batch"):
		var req struct {
			Objects []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var out []string
		for _, o := range req.Objects {
			if o.OID == oid {
				out = append(out, fmt.Sprintf(`{"oid":%q,"size":%d,"actions":{"download":{"href":"%s/lfs/%s","header":{"X-Ticket":"t1"}}}}`,
					o.OID, o.Size, s.URL, o.OID))
			} else {
				out = append(out, fmt.Sprintf(`{"oid":%q,"size":%d,"error":{"code":404,"message":"Object does not exist"}}`, o.OID, o.Size))
			}
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		fmt.Fprintf(w, `{"transfer":"basic","objects":[%s]}`, strings.Join(out, ","))
	case p == "/lfs/"+oid:
		if r.Header.Get("X-Ticket") != "t1" {
			http.Error(w, "no ticket", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(lfsContent))
	case strings.HasPrefix(p, "/scm/"):
		backend.ServeHTTP(w, r)
	case p == "/rest/api/1.0/application-properties":
		fmt.Fprint(w, `{"version":"8.19.1","buildNumber":"8019001","displayName":"Bitbucket"}`)
	case p == "/rest/api/1.0/users":
		page(bbJdoe, bbMmuster, bbBot)
	case p == "/rest/api/1.0/projects":
		page(`{"id":1,"key":"SHOP","name":"Shop","description":"The web shop","public":false,"type":"NORMAL"}`,
			`{"id":2,"key":"~JDOE","name":"Jane Doe","type":"PERSONAL","owner":`+bbJdoe+`}`)
	case p == "/rest/api/1.0/projects/SHOP/permissions/users":
		page(r2.Replace(`{"user":{{MMUSTER}},"permission":"PROJECT_WRITE"}`))
	case p == "/rest/api/1.0/projects/SHOP/permissions/groups":
		page(`{"group":{"name":"shop-devs"},"permission":"PROJECT_READ"}`)
	case p == "/rest/branch-permissions/2.0/projects/SHOP/restrictions":
		page(r2.Replace(`{"id":10,"type":"read-only","matcher":{"id":"refs/heads/main","displayId":"main","type":{"id":"BRANCH","name":"Branch"},"active":true},"users":[{{MMUSTER}}],"groups":[],"accessKeys":[]}`),
			`{"id":11,"type":"no-deletes","matcher":{"id":"release/*","displayId":"release/*","type":{"id":"PATTERN","name":"Pattern"},"active":true},"users":[],"groups":["release-managers"],"accessKeys":[]}`,
			`{"id":12,"type":"pull-request-only","matcher":{"id":"production","displayId":"Production","type":{"id":"MODEL_BRANCH","name":"Branching model branch"},"active":true},"users":[],"groups":[],"accessKeys":[]}`)
	case p == "/rest/default-reviewers/1.0/projects/SHOP/conditions":
		fmt.Fprint(w, r2.Replace(`[{"id":4,"scope":{"resourceId":1,"type":"PROJECT"},"sourceRefMatcher":{"id":"ANY_REF_MATCHER_ID","displayId":"ANY_REF_MATCHER_ID","type":{"id":"ANY_REF","name":"Any branch"}},"targetRefMatcher":{"id":"ANY_REF_MATCHER_ID","displayId":"ANY_REF_MATCHER_ID","type":{"id":"ANY_REF","name":"Any branch"}},"reviewers":[{{MMUSTER}}],"requiredApprovals":1}]`))
	case p == "/rest/api/1.0/projects/SHOP/repos":
		page(`{"id":7,"slug":"web","name":"Web","description":"The shop front","scmId":"git","public":false,"archived":false,"project":{"key":"SHOP"}}`)
	case p == "/rest/api/1.0/projects/SHOP/repos/web/branches/default":
		fmt.Fprint(w, `{"id":"refs/heads/main","displayId":"main","type":"BRANCH","isDefault":true}`)
	case p == "/rest/api/1.0/projects/SHOP/repos/web/permissions/users":
		page(r2.Replace(`{"user":{{JDOE}},"permission":"REPO_ADMIN"}`))
	case p == "/rest/api/1.0/projects/SHOP/repos/web/permissions/groups":
		page()
	case p == "/rest/branch-permissions/2.0/projects/SHOP/repos/web/restrictions":
		page(`{"id":13,"type":"fast-forward-only","matcher":{"id":"stable/","displayId":"stable/","type":{"id":"PATTERN","name":"Pattern"},"active":true},"users":[],"groups":[],"accessKeys":[{"key":{"id":1}}]}`)
	case p == "/rest/api/1.0/projects/SHOP/repos/web/settings/pull-requests":
		fmt.Fprint(w, `{"mergeConfig":{"defaultStrategy":{"id":"squash","enabled":true},"strategies":[{"id":"no-ff","enabled":true},
			{"id":"squash","enabled":true},{"id":"rebase-no-ff","enabled":false},{"id":"rebase-ff-only","enabled":true}],"type":"repository"},"requiredAllApprovers":false,
			"requiredAllTasksComplete":true,"requiredApprovers":2,"requiredSuccessfulBuilds":0,
			"com.atlassian.bitbucket.server.bitbucket-build.requiredBuildsMergeCheck":{"enabled":true,"count":1}}`)
	case p == "/rest/api/1.0/projects/SHOP/repos/web/settings/hooks":
		page(`{"details":{"key":"com.atlassian.bitbucket.server.bitbucket-bundled:needs-work-merge-check","name":"No 'needs work' status"},"enabled":false,"configured":false}`,
			`{"details":{"key":"com.atlassian.bitbucket.server.bitbucket-bundled:force-push","name":"Reject Force Push"},"enabled":true,"configured":true}`,
			`{"details":{"key":"com.example.jira:commit-checker","name":"Jira Commit Checker"},"enabled":true,"configured":true}`)
	case p == "/rest/default-reviewers/1.0/projects/SHOP/repos/web/conditions":
		fmt.Fprint(w, r2.Replace(`{"size":1,"limit":25,"isLastPage":true,"start":0,"values":[{"id":5,"scope":{"resourceId":7,"type":"REPOSITORY"},"sourceRefMatcher":{"id":"refs/heads/feature/**","displayId":"feature/**","type":{"id":"PATTERN","name":"Pattern"}},"targetRefMatcher":{"id":"refs/heads/develop","displayId":"develop","type":{"id":"BRANCH","name":"Branch"}},"reviewers":[{{JDOE}}],"requiredApprovals":1}]}`))
	case p == "/rest/api/1.0/projects/SHOP/repos/web/webhooks":
		page(`{"id":1,"name":"Jenkins","url":"https://ci:secret@jenkins.example.com/bitbucket-hook/?token=abc","active":true,"events":["repo:refs_changed"]}`)
	case p == "/rest/api/1.0/projects/SHOP/repos/web/pull-requests":
		if r.URL.Query().Get("state") != "ALL" {
			http.Error(w, "state", http.StatusBadRequest)
			return
		}
		page(r2.Replace(`{"id":1,"version":3,"title":"Add B","description":"Adds **B**.","state":"MERGED","open":false,"closed":true,
				"createdDate":`+ms(1)+`,"updatedDate":`+ms(7)+`,"closedDate":`+ms(7)+`,"fromRef":`+ref("feature", "c2")+`,"toRef":`+ref("main", "c1")+`,
				"author":{"user":{{JDOE}},"role":"AUTHOR","approved":false,"status":"UNAPPROVED"},
				"reviewers":[{"user":{{MMUSTER}},"role":"REVIEWER","approved":true,"status":"APPROVED"}]}`),
			r2.Replace(`{"id":2,"version":2,"title":"Fix the README","description":"","state":"OPEN","open":true,"closed":false,
				"createdDate":`+ms(8)+`,"updatedDate":`+ms(11)+`,"fromRef":`+ref("fix", "fix")+`,"toRef":`+ref("main", "c3")+`,
				"author":{"user":{{MMUSTER}},"role":"AUTHOR","approved":false,"status":"UNAPPROVED"},
				"reviewers":[{"user":{{JDOE}},"role":"REVIEWER","approved":false,"status":"NEEDS_WORK"}]}`),
			r2.Replace(`{"id":3,"version":1,"title":"An idea","description":"Maybe $5, or set $A$ in $HOME/$USER.","state":"DECLINED","open":false,"closed":true,
				"createdDate":`+ms(2)+`,"updatedDate":`+ms(6)+`,"closedDate":`+ms(6)+`,"fromRef":`+ref("old-idea", "c5")+`,"toRef":`+ref("main", "c1")+`,
				"author":{"user":{{JDOE}},"role":"AUTHOR","approved":false,"status":"UNAPPROVED"},"reviewers":[]}`),
			r2.Replace(`{"id":4,"version":0,"title":"From my fork","description":"","state":"OPEN","open":true,"closed":false,
				"createdDate":`+ms(12)+`,"updatedDate":`+ms(12)+`,
				"fromRef":{"id":"refs/heads/fork-work","displayId":"fork-work","latestCommit":"`+s.commit("c6")+`","repository":{"id":99,"slug":"web-fork","project":{"key":"~JDOE"}}},
				"toRef":`+ref("main", "c3")+`,"author":{"user":{{JDOE}},"role":"AUTHOR","approved":false,"status":"UNAPPROVED"},"reviewers":[]}`))
	case strings.HasPrefix(p, "/rest/api/1.0/projects/SHOP/repos/web/pull-requests/") && strings.HasSuffix(p, "/activities"):
		var list []string
		switch strings.Split(p, "/")[9] {
		case "1": // newest first, like the source
			list = []string{
				r2.Replace(`{"id":104,"createdDate":` + ms(7) + `,"user":{{MMUSTER}},"action":"MERGED","commit":{"id":"` + s.commit("c3") + `"}}`),
				r2.Replace(`{"id":105,"createdDate":` + ms(5) + `,"user":{{MMUSTER}},"action":"COMMENTED","commentAction":"ADDED",
					"comment":{"id":503,"version":1,"text":"Add a test","author":{{MMUSTER}},"createdDate":` + ms(5) + `,"updatedDate":` + ms(6) + `,
					"comments":[],"severity":"BLOCKER","state":"RESOLVED","resolvedDate":` + ms(6) + `}}`),
				r2.Replace(`{"id":103,"createdDate":` + ms(4) + `,"user":{{MMUSTER}},"action":"APPROVED"}`),
				r2.Replace(`{"id":102,"createdDate":` + ms(2) + `,"user":{{MMUSTER}},"action":"COMMENTED","commentAction":"ADDED",
					"comment":{"id":501,"version":0,"text":"Why **B**?","author":{{MMUSTER}},"createdDate":` + ms(2) + `,"updatedDate":` + ms(2) + `,
						"comments":[{"id":502,"version":0,"text":"Because @mmuster asked.","author":{{JDOE}},"createdDate":` + ms(3) + `,"updatedDate":` + ms(3) + `,"comments":[]}],
						"severity":"NORMAL","state":"OPEN"},
					"commentAnchor":{"fromHash":"` + s.commit("c1") + `","toHash":"` + s.commit("c2") + `","line":5,"lineType":"ADDED","fileType":"TO",
						"path":"src/app.go","diffType":"EFFECTIVE","orphaned":false}}`),
				r2.Replace(`{"id":101,"createdDate":` + ms(1) + `,"user":{{JDOE}},"action":"OPENED"}`),
			}
		case "2":
			list = []string{
				r2.Replace(`{"id":204,"createdDate":` + ms(11) + `,"user":{{JDOE}},"action":"COMMENTED","commentAction":"ADDED",
					"comment":{"id":601,"text":"The title?","author":{{JDOE}},"createdDate":` + ms(11) + `,"updatedDate":` + ms(11) + `,"comments":[]},
					"commentAnchor":{"fromHash":"` + s.commit("c3") + `","toHash":"` + s.commit("c4") + `","line":1,"lineType":"CONTEXT","fileType":"FROM",
						"path":"README.md","diffType":"EFFECTIVE","orphaned":true}}`),
				r2.Replace(`{"id":203,"createdDate":` + ms(10) + `,"user":{{MMUSTER}},"action":"RESCOPED","fromHash":"` + s.commit("c4") + `",
					"previousFromHash":"` + s.commit("c3") + `","added":{"commits":[{"id":"` + s.commit("c4") + `","displayId":"x","message":"Fix README\n\nDetails"}],"total":1},
					"removed":{"commits":[],"total":0}}`),
				r2.Replace(`{"id":202,"createdDate":` + ms(9) + `,"user":{{JDOE}},"action":"REVIEWED"}`),
				r2.Replace(`{"id":201,"createdDate":` + ms(8) + `,"user":{{MMUSTER}},"action":"OPENED"}`),
			}
		case "3":
			list = []string{
				r2.Replace(`{"id":302,"createdDate":` + ms(6) + `,"user":{{MMUSTER}},"action":"DECLINED"}`),
				r2.Replace(`{"id":301,"createdDate":` + ms(2) + `,"user":{{JDOE}},"action":"OPENED"}`),
			}
		case "4":
			list = []string{r2.Replace(`{"id":401,"createdDate":` + ms(12) + `,"user":{{JDOE}},"action":"OPENED"}`)}
		}
		page(list...)
	default:
		http.Error(w, `{"errors":[{"message":"not found"}]}`, http.StatusNotFound)
	}
}

func TestBitbucketImport(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	mm := env.CreateUser("mmuster")
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	mm.Get("/api/v1/me", &me)
	src := newBitbucket(t)
	work := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()

	bad := importer.NewBitbucket(importer.BitbucketOptions{URL: src.URL, Token: "wrong", Workdir: t.TempDir()}, log)
	if err := bad.Extract(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("extract with a wrong token: %v", err)
	}
	if err := importer.NewBitbucket(importer.BitbucketOptions{URL: src.URL, Token: bbToken, Projects: []string{"NOPE"}, Workdir: t.TempDir()},
		log).Extract(ctx); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("extract of an unknown project: %v", err)
	}

	bb := importer.NewBitbucket(importer.BitbucketOptions{URL: src.URL, Token: bbToken, Workdir: work}, log)
	if err := bb.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bitbucket/users.json", "bitbucket/projects/SHOP/restrictions.json", "bitbucket/projects/SHOP/repos/web/hooks.json",
		"bitbucket/projects/SHOP/repos/web/activities/1.json", "bitbucket/projects/SHOP/repos/web/default-branch.json",
		"bitbucket/projects/SHOP/repos/web/lfs.json"} {
		if _, err := os.Stat(filepath.Join(work, f)); err != nil {
			t.Errorf("not extracted: %s", f)
		}
	}
	if c := bbGit(t, work, "--git-dir", "bitbucket/git/SHOP/web.git", "rev-parse", "refs/pull-requests/3/from"); c != src.commit("c5") {
		t.Errorf("the commit of a pull request of a deleted branch: %s", c)
	}
	var users []json.RawMessage
	raw, _ := os.ReadFile(filepath.Join(work, "bitbucket/users.json"))
	if err := json.Unmarshal(raw, &users); err != nil || len(users) != 3 {
		t.Errorf("users over two pages: %d %v", len(users), err)
	}

	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Code: env.App.Code, Log: log}
	rep, err := bb.Load(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"code-projects": 1, "repositories": 1, "pull-requests": 4, "comments": 4, "tasks": 1, "lfs-objects": 1,
		"branch-permissions": 4, "grants": 3, "users": 2}
	for k, n := range want {
		if rep.Counts[k] != n {
			t.Errorf("count %s = %d, want %d (%v)", k, rep.Counts[k], n, rep.Counts)
		}
	}
	if strings.Join(rep.UsersMatched, ",") != "mmuster" || strings.Join(rep.Placeholders, ",") != "jdoe" {
		t.Errorf("users: matched %v, placeholders %v", rep.UsersMatched, rep.Placeholders)
	}
	warnings := strings.Join(rep.Warnings, "\n")
	for _, w := range []string{"personal project ~JDOE", `branching model branch "Production"`, "condition 5", `"Jenkins" (https://jenkins.example.com/bitbucket-hook/)`,
		"#4 came from the fork ~JDOE/web-fork", "access keys"} {
		if !strings.Contains(warnings, w) {
			t.Errorf("no warning with %q in\n%s", w, warnings)
		}
	}
	if strings.Contains(warnings, "secret") || strings.Contains(warnings, "token=abc") {
		t.Errorf("the report shows the webhook's credentials:\n%s", warnings)
	}
	if rep.Unsupported["hook Jira Commit Checker"] != 1 || rep.Unsupported["branch permission matcher MODEL_BRANCH"] != 1 {
		t.Errorf("unsupported = %v", rep.Unsupported)
	}
	// Rebasing strategies are Kogge's own now: nothing to report.
	for k := range rep.Unsupported {
		if strings.HasPrefix(k, "merge strategy") {
			t.Errorf("unsupported = %v", rep.Unsupported)
		}
	}
	if len(rep.Repositories) != 1 || rep.Repositories[0] != (importer.RepositorySummary{Project: "SHOP", Slug: "web", History: true,
		LFSObjects: 1, PullRequests: 4, Comments: 4}) {
		t.Errorf("repositories = %+v", rep.Repositories)
	}

	// The repository: branches, tags, files and the LFS object; the commits of pull requests are kept but hidden.
	var repo struct {
		DefaultBranch string `json:"defaultBranch"`
		CloneHTTP     string `json:"cloneHttp"`
		Settings      struct {
			MergeStrategies  []string `json:"mergeStrategies"`
			RequiredApproval int      `json:"requiredApprovals"`
			RequiredBuilds   int      `json:"requiredBuilds"`
			NoNeedsWork      bool     `json:"noNeedsWork"`
			TasksResolved    bool     `json:"tasksResolved"`
			DefaultReviewers []int64  `json:"defaultReviewers"`
		} `json:"settings"`
	}
	mm.Get("/api/v1/code/repos/SHOP/web", &repo)
	st := repo.Settings
	if repo.DefaultBranch != "main" || strings.Join(st.MergeStrategies, ",") != "squash,merge,rebase" || st.RequiredApproval != 2 || st.RequiredBuilds != 1 ||
		st.NoNeedsWork || !st.TasksResolved || !slices.Equal(st.DefaultReviewers, []int64{me.User.ID}) {
		t.Errorf("repository: %+v", repo)
	}
	var branches struct {
		Total int `json:"total"`
	}
	mm.Get("/api/v1/code/repos/SHOP/web/branches", &branches)
	if branches.Total != 3 {
		t.Errorf("branches = %d", branches.Total)
	}
	if r := mm.Do(http.MethodGet, "/api/v1/code/repos/SHOP/web/raw?ref=v1.0&path=src/app.go", nil); r.Status != http.StatusOK ||
		!strings.Contains(string(r.Body), "func B()") {
		t.Errorf("file at the tag: %d %q", r.Status, r.Body)
	}
	var lfs int
	sum := sha256.Sum256([]byte(lfsContent))
	if err := env.App.Pool.QueryRow(ctx, `SELECT count(*) FROM lfs_objects o JOIN repositories r ON r.id = o.repository_id
		WHERE r.slug = 'web' AND o.oid = $1`, hex.EncodeToString(sum[:])).Scan(&lfs); err != nil || lfs != 1 {
		t.Errorf("LFS object: %d %v", lfs, err)
	}
	var token struct {
		Token string `json:"token"`
	}
	mm.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "git", "scopes": []string{"read"}}, &token)
	u, _ := url.Parse(repo.CloneHTTP)
	u.User = url.UserPassword("mmuster", token.Token)
	refs := bbGit(t, t.TempDir(), "ls-remote", u.String())
	if !strings.Contains(refs, "refs/heads/fix") || !strings.Contains(refs, "refs/tags/v1.0") || strings.Contains(refs, "imported") ||
		strings.Contains(refs, "pull-requests") {
		t.Errorf("refs offered to clients:\n%s", refs)
	}

	// Pull requests keep their numbers, states, reviewers, comments and events.
	type pullRequest struct {
		Source     string `json:"source"`
		SourceSHA  string `json:"sourceSha"`
		MergeBase  string `json:"mergeBase"`
		State      string `json:"state"`
		Conflicted bool   `json:"conflicted"`
		ClosedBy   *struct {
			Username string `json:"username"`
		} `json:"closedBy"`
		Reviewers []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
			State string `json:"state"`
		} `json:"reviewers"`
		Comments      int             `json:"comments"`
		ResolvedTasks int             `json:"resolvedTasks"`
		Description   json.RawMessage `json:"description"`
	}
	prs := map[int]pullRequest{}
	for n := 1; n <= 4; n++ {
		var pr pullRequest
		mm.Get("/api/v1/code/repos/SHOP/web/pull-requests/"+strconv.Itoa(n), &pr)
		prs[n] = pr
	}
	if p := prs[1]; p.State != "merged" || p.ClosedBy == nil || p.ClosedBy.Username != "mmuster" || p.MergeBase != src.commit("c1") ||
		p.Comments != 3 || p.ResolvedTasks != 1 || len(p.Reviewers) != 1 || p.Reviewers[0].State != "approved" {
		t.Errorf("pull request 1: %+v", p)
	}
	if p := prs[2]; p.State != "open" || p.SourceSHA != src.commit("c4") || p.Conflicted || len(p.Reviewers) != 1 ||
		p.Reviewers[0].User.Username != "jdoe" || p.Reviewers[0].State != "needs-work" {
		t.Errorf("pull request 2: %+v", p)
	}
	if p := prs[3]; p.State != "declined" || p.ClosedBy == nil || p.ClosedBy.Username != "mmuster" || p.Source != "old-idea" {
		t.Errorf("pull request 3: %+v", p)
	}
	// Bitbucket's Markdown has no formulas: its dollars stay text.
	if d := string(prs[3].Description); strings.Contains(d, "inlineMath") || !strings.Contains(d, `"text":"Maybe $5, or set $A$ in $HOME/$USER."`) {
		t.Errorf("description of pull request 3: %s", d)
	}
	if p := prs[4]; p.State != "declined" || p.Source != "~JDOE/web-fork:fork-work" {
		t.Errorf("pull request 4: %+v", p)
	}
	type entry struct {
		Kind  string         `json:"kind"`
		Data  map[string]any `json:"data"`
		Actor *struct {
			Username string `json:"username"`
		} `json:"actor"`
		Comment *struct {
			ParentID int64  `json:"parentId"`
			Path     string `json:"path"`
			Line     int    `json:"line"`
			Side     string `json:"side"`
			LineText string `json:"lineText"`
			Outdated bool   `json:"outdated"`
			Task     bool   `json:"task"`
		} `json:"comment"`
	}
	timeline := func(n int) []entry {
		var list []entry
		mm.Get("/api/v1/code/repos/SHOP/web/pull-requests/"+strconv.Itoa(n)+"/timeline", &list)
		return list
	}
	kinds := func(list []entry) string {
		var k []string
		for _, e := range list {
			k = append(k, e.Kind)
		}
		return strings.Join(k, ",")
	}
	first := timeline(1)
	if got := kinds(first); got != "opened,commented,commented,reviewed,commented,merged" { // the reply after its comment
		t.Errorf("timeline 1: %s", got)
	}
	for _, e := range first {
		if c := e.Comment; c != nil && c.ParentID == 0 && !c.Task &&
			(c.Path != "src/app.go" || c.Line != 5 || c.Side != "new" || c.LineText != "func B() {}" || c.Outdated) {
			t.Errorf("line comment: %+v", c)
		}
	}
	second := timeline(2)
	if got := kinds(second); got != "opened,reviewed,rescoped,commented" {
		t.Errorf("timeline 2: %s", got)
	}
	for _, e := range second {
		if e.Kind == "rescoped" && fmt.Sprint(e.Data["added"]) != "[map[sha:"+src.commit("c4")+" subject:Fix README]]" {
			t.Errorf("rescoped: %v", e.Data)
		}
		if c := e.Comment; c != nil && (c.Side != "old" || c.LineText != "# Web" || !c.Outdated) {
			t.Errorf("outdated comment on the old side: %+v", c)
		}
	}

	// Permissions, branch permissions (patterns match like the source's) and the force-push hook.
	var grants []struct {
		Permission string `json:"permission"`
		Holder     struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"holder"`
	}
	admin.Get("/api/v1/code/projects/SHOP/permissions", &grants)
	var got []string
	for _, g := range grants {
		got = append(got, g.Permission+" "+g.Holder.Type+" "+g.Holder.Name)
	}
	slices.Sort(got)
	if strings.Join(got, "; ") != "repo.read group shop-devs; repo.write user Mmuster" {
		t.Errorf("project permissions: %v", got)
	}
	rows, err := env.App.Pool.Query(ctx, `SELECT format('%s %s %s/%s %s', pattern, kind, cardinality(exempt_users), cardinality(exempt_groups),
		CASE WHEN project_id IS NULL THEN 'repository' ELSE 'project' END) FROM branch_permissions ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for rows.Next() {
		var r string
		_ = rows.Scan(&r)
		got = append(got, r)
	}
	rows.Close()
	if strings.Join(got, "; ") != "main restrict-push 1/0 project; refs/** no-rewrite 0/0 repository; refs/**/release/* no-delete 0/1 project; "+
		"refs/**/stable/** no-rewrite 0/0 repository" {
		t.Errorf("branch permissions: %v", got)
	}

	// A second load changes nothing.
	again, err := bb.Load(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	var comments, events int
	if err := env.App.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM pull_request_comments), (SELECT count(*) FROM pull_request_activities)`).
		Scan(&comments, &events); err != nil || comments != 4 || events != 9 {
		t.Errorf("after a second load: %d comments, %d events (%v)", comments, events, err)
	}
	if again.Counts["pull-requests"] != 4 || strings.Join(again.Placeholders, ",") != "jdoe" || strings.Join(again.UsersMatched, ",") != "mmuster" {
		t.Errorf("second load: %v, matched %v, placeholders %v", again.Counts, again.UsersMatched, again.Placeholders)
	}

	// A delta load brings new commits of the source and the pull requests' new state.
	bbGit(t, src.work, "checkout", "-q", "fix")
	if err := os.WriteFile(filepath.Join(src.work, "README.md"), []byte("# Web\n\nFixed again.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bbGit(t, src.work, "commit", "-q", "-am", "Fix again")
	src.mu.Lock()
	src.sha["fix"] = bbGit(t, src.work, "rev-parse", "HEAD")
	src.mu.Unlock()
	bbGit(t, src.work, "push", "-q", src.bare, "fix")
	if err := bb.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	// The load brings the history before the pull requests; jobs of the running server must not rescope them in
	// between (the pull requests' events come from the source).
	var repoID int64
	if err := env.App.Pool.QueryRow(ctx, "SELECT id FROM repositories WHERE slug = 'web'").Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	if err := env.App.Code.LoadMirror(ctx, repoID, filepath.Join(work, "bitbucket/git/SHOP/web.git"), "main"); err != nil {
		t.Fatal(err)
	}
	env.WorkJobs()
	if got := kinds(timeline(2)); got != "opened,reviewed,rescoped,commented" {
		t.Errorf("rescoped by the import of the history: %s", got)
	}
	if _, err := bb.Load(ctx, loader); err != nil {
		t.Fatal(err)
	}
	env.WorkJobs()
	var pr pullRequest
	mm.Get("/api/v1/code/repos/SHOP/web/pull-requests/2", &pr)
	if pr.SourceSHA != src.commit("fix") || kinds(timeline(2)) != "opened,reviewed,rescoped,commented" {
		t.Errorf("after the delta load: %+v, %s", pr, kinds(timeline(2)))
	}
	if r := mm.Do(http.MethodGet, "/api/v1/code/repos/SHOP/web/raw?ref=fix&path=README.md", nil); !strings.Contains(string(r.Body), "again") {
		t.Errorf("the new commit: %d %q", r.Status, r.Body)
	}

	// The source's addresses lead to the repository.
	for path, target := range map[string]string{
		"/projects/SHOP/repos/web/browse":                                    "/code/shop/web",
		"/projects/shop/repos/web/browse/src/app.go?at=refs%2Fheads%2Ffix":   "/code/shop/web/src/src/app.go?at=fix",
		"/projects/SHOP/repos/web/pull-requests/2/overview":                  "/code/shop/web/pull-requests/2",
		"/projects/SHOP/repos/web/commits/" + src.commit("c2"):               "/code/shop/web/commits/" + src.commit("c2"),
		"/projects/SHOP/repos/web/raw/README.md?at=refs%2Fheads%2Fmain":      "/api/v1/code/repos/SHOP/web/raw?path=README.md&ref=main",
		"/projects/SHOP/repos/web/browse/docs/My%20Notes.md?at=v1.0":         "/code/shop/web/src/docs/My%20Notes.md?at=v1.0",
		"/projects/SHOP/repos/web/commits?until=refs%2Fheads%2Ffeature":      "/code/shop/web/commits?at=feature",
		"/projects/SHOP/repos/web/pull-requests?state=MERGED":                "/code/shop/web/pull-requests",
		"/projects/SHOP/repos/web/branches":                                  "/code/shop/web/branches",
		"/projects/SHOP/repos/web/settings/permissions":                      "/code/shop/web/settings",
		"/projects/SHOP/repos/web/forks":                                     "/code/shop/web",
		"/projects/SHOP/repos/web/compare/commits?sourceBranch=refs/heads/x": "/code/shop/web/compare",
	} {
		res := mm.Do(http.MethodGet, path, nil)
		if res.Status != http.StatusFound || res.Header.Get("Location") != target {
			t.Errorf("GET %s: %d %s, want %s", path, res.Status, res.Header.Get("Location"), target)
		}
	}
	// Administrators do not read content: no redirect for them.
	if res := admin.Do(http.MethodGet, "/projects/SHOP/repos/web/browse", nil); res.Status == http.StatusFound {
		t.Errorf("redirect for a reader without access: %s", res.Header.Get("Location"))
	}
}
