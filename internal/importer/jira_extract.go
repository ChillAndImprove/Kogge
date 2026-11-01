// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
)

// The extraction of Jira. Big projects take hours: the issues are read page by page,
// each page with the details of its issues (comments, work logs and watchers where the search lists only some,
// and the attachment files, several at a time); a checkpoint after every page lets an interrupted extraction
// resume there.

// issuePage is the number of issues the extraction asks for at a time.
const issuePage = 100

// jiraExtraction is the state of one extraction.
type jiraExtraction struct {
	*Jira
	cp *checkpoint
}

func (j *Jira) concurrency() int { return max(j.opt.Concurrency, 1) }

func (j *Jira) save(ctx context.Context, path string, parts ...string) (json.RawMessage, error) {
	b, err := j.client.GetJSON(ctx, path)
	if err != nil {
		return nil, err
	}
	return b, j.work.Write(b, parts...)
}

// saveOptional stores a resource that older versions of Jira do not have, or that needs rights the token may lack;
// it reports whether it was stored.
func (j *Jira) saveOptional(ctx context.Context, path string, parts ...string) (bool, error) {
	_, err := j.save(ctx, path, parts...)
	var se *StatusError
	if errors.As(err, &se) && se.Status < 500 && se.Status != 401 {
		j.log.Debug("not available in the source", "resource", path, "status", se.Status)
		return false, nil
	}
	return err == nil, err
}

// Extract reads the configuration and the selected projects through the REST API into the work directory. An
// interrupted extraction resumes where it stopped (unless Restart is set).
func (j *Jira) Extract(ctx context.Context) error {
	if j.opt.Concurrency <= 0 {
		j.opt.Concurrency = 4
	}
	cp, err := openCheckpoint(j.work, "jira", j.opt.Restart, map[string]any{"attachments": !j.opt.SkipAttachments}, j.log)
	if err != nil {
		return err
	}
	defer cp.close()
	x := &jiraExtraction{Jira: j, cp: cp}
	keys := j.selected()
	if err := cp.part("config", func() error { return x.config(ctx, len(keys) == 0) }); err != nil {
		return err
	}
	if len(keys) == 0 {
		var all []jProject
		if _, err := j.work.Read(&all, "jira", "projects.json"); err != nil {
			return err
		}
		for _, p := range all {
			keys = append(keys, p.Key)
		}
	}
	for _, key := range keys {
		if err := cp.part("project/"+key, func() error { return x.project(ctx, key) }); err != nil {
			return fmt.Errorf("project %s: %w", key, err)
		}
	}
	if err := x.groups(ctx, keys); err != nil {
		return err
	}
	if err := cp.part("workflows", func() error { return x.workflows(ctx, keys) }); err != nil {
		return err
	}
	return cp.finish()
}

// config reads the instance's configuration (and the list of projects when none were selected).
func (x *jiraExtraction) config(ctx context.Context, all bool) error {
	for _, e := range []struct{ path, file string }{
		{"/rest/api/2/serverInfo", "serverInfo.json"},
		{"/rest/api/2/issuetype", "issuetypes.json"},
		{"/rest/api/2/status", "statuses.json"},
		{"/rest/api/2/priority", "priorities.json"},
		{"/rest/api/2/resolution", "resolutions.json"},
		{"/rest/api/2/issueLinkType", "linktypes.json"},
		{"/rest/api/2/field", "fields.json"},
	} {
		if _, err := x.save(ctx, e.path, "jira", e.file); err != nil {
			return err
		}
	}
	// The workflows (names and steps), for the analysis.
	if _, err := x.saveOptional(ctx, "/rest/api/2/workflow", "jira", "workflows.json"); err != nil {
		return err
	}
	if all {
		if _, err := x.save(ctx, "/rest/api/2/project", "jira", "projects.json"); err != nil {
			return err
		}
	}
	return nil
}

func (x *jiraExtraction) project(ctx context.Context, key string) error {
	x.log.Info("extracting project", "project", key)
	prefix := "project/" + key
	err := x.cp.part(prefix+"/meta", func() error {
		if _, err := x.save(ctx, "/rest/api/2/project/"+key, "jira", "projects", key, "project.json"); err != nil {
			return err
		}
		if _, err := x.save(ctx, "/rest/api/2/project/"+key+"/statuses", "jira", "projects", key, "statuses.json"); err != nil {
			return err
		}
		// Schemes the load does not take over (notifications, workflows) or only in part (security levels): the
		// analysis shows them.
		for name, path := range map[string]string{"notificationscheme.json": "/notificationscheme?expand=all",
			"issuesecuritylevelscheme.json": "/issuesecuritylevelscheme", "workflowscheme.json": "/workflowscheme"} {
			if _, err := x.saveOptional(ctx, "/rest/api/2/project/"+key+path, "jira", "projects", key, name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := x.issues(ctx, key); err != nil {
		return err
	}
	if err := x.cp.part(prefix+"/access", func() error { return x.access(ctx, key) }); err != nil {
		return err
	}
	return x.cp.part(prefix+"/boards", func() error { return x.boards(ctx, key) })
}

func pageFile(n int) string { return fmt.Sprintf("page-%05d.json", n) }

// issues reads the issues of a project page by page, each with its details. The pages of an interrupted
// extraction are complete up to the first one not marked; the extraction goes on after it.
func (x *jiraExtraction) issues(ctx context.Context, key string) error {
	prefix := "project/" + key + "/issues/"
	page, start := 0, 0
	for {
		v, ok := x.cp.isDone(prefix + strconv.Itoa(page))
		if !ok {
			break
		}
		start, _ = strconv.Atoi(v)
		page++
	}
	if page > 0 {
		x.log.Info("resuming the issues of the project", "project", key, "from", start)
	}
	var p *progress
	total := 0
	for {
		raw, err := x.search(ctx, key, start, issuePage, "*all", "renderedFields,changelog")
		positions := -1 // the issues of the page
		if serverError(err) {
			// One broken issue (a renderer or a field that fails) breaks the whole page: read it issue by issue.
			x.log.Warn("a page of issues cannot be read; reading it issue by issue", "project", key, "from", start, "error", err)
			raw, positions, err = x.searchOneByOne(ctx, key, start, issuePage, err)
		}
		if err != nil {
			return err
		}
		var res jSearch
		if err := json.Unmarshal(raw, &res); err != nil {
			return fmt.Errorf("issues from %d: %w", start, err)
		}
		total = res.Total
		if p == nil {
			p = newProgress(x.log, "issues of "+key, res.Total)
			p.done = start
		}
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(x.concurrency())
		for _, is := range res.Issues {
			g.Go(func() error {
				if err := x.issueDetails(gctx, key, is); err != nil {
					return fmt.Errorf("%s: %w", is.Key, err)
				}
				p.add(1)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}
		if err := x.work.Write(raw, "jira", "projects", key, "issues", pageFile(page)); err != nil {
			return err
		}
		if positions < 0 {
			positions = len(res.Issues)
		}
		start += positions
		if err := x.cp.markDone(prefix+strconv.Itoa(page), strconv.Itoa(start)); err != nil {
			return err
		}
		page++
		if positions == 0 || start >= res.Total {
			break
		}
	}
	p.finish()
	// Pages of an earlier extraction beyond the end are stale (issues that were deleted or moved since).
	for _, name := range x.work.List("jira", "projects", key, "issues") {
		var n int
		if _, err := fmt.Sscanf(name, "page-%05d.json", &n); err == nil && n >= page {
			if err := os.Remove(x.work.Path("jira", "projects", key, "issues", name)); err != nil {
				return err
			}
		}
	}
	// Issues that moved between the pages while they were read are missing (or doubled): the source changed.
	seen := map[string]bool{}
	for _, name := range x.work.List("jira", "projects", key, "issues") {
		var res struct {
			Issues []struct {
				Key string `json:"key"`
			} `json:"issues"`
		}
		if _, err := x.work.Read(&res, "jira", "projects", key, "issues", name); err != nil {
			return err
		}
		for _, is := range res.Issues {
			seen[is.Key] = true
		}
	}
	// Issues the source could not answer were left out and noted (also in an interrupted run).
	notes, err := readExtractionNotes(x.work, "jira")
	if err != nil {
		return err
	}
	unreadable := map[string]bool{} // a run that resumed may have noted an issue twice
	for _, n := range notes {
		if n.Kind == "issue" && n.ID == key {
			unreadable[n.Name] = true
		}
	}
	if len(seen)+len(unreadable) < total {
		msg := fmt.Sprintf("the search found %d issues, but %d were read: issues changed during the extraction; extract again", total, len(seen))
		x.log.Warn(msg, "project", key)
		return x.cp.note(ExtractionNote{Kind: "listing", ID: key, Note: msg})
	}
	return nil
}

// search reads a page of the issues of a project, in the order of their rank.
func (x *jiraExtraction) search(ctx context.Context, key string, start, size int, fields, expand string) (json.RawMessage, error) {
	q := query(map[string]string{"jql": "project = \"" + key + "\" ORDER BY Rank ASC, key ASC", "startAt": strconv.Itoa(start),
		"maxResults": strconv.Itoa(size), "fields": fields, "expand": expand})
	return x.client.GetJSON(ctx, "/rest/api/2/search?"+q)
}

// searchOneByOne reads a page of issues one at a time. An issue that fails is read again without its rendered
// fields (the load then converts its text as plain text); one that fails also so is noted and left out. When the
// source fails as a whole, or no issue of the page can be told, the extraction stops (to resume later) instead.
// It returns the page and the number of positions it went through.
func (x *jiraExtraction) searchOneByOne(ctx context.Context, key string, start, size int, failure error) (json.RawMessage, int,
	error) {
	page := jSearch{StartAt: start, MaxResults: size}
	issues := []json.RawMessage{}
	positions := 0
	told := false     // whether an issue of the page could be read or named
	var unnamed []int // positions of issues that could not be named either
	few := fewAttempts(ctx)
	for i := start; i < start+size; i++ {
		positions++
		raw, err := x.search(few, key, i, 1, "*all", "renderedFields,changelog")
		if serverError(err) {
			raw, err = x.search(few, key, i, 1, "*all", "changelog")
		}
		if serverError(err) {
			if !x.client.brokenItem(ctx, err, "/rest/api/2/serverInfo") {
				return nil, 0, err
			}
			// Which issue it is: its key (with the summary only) can be read.
			var one struct {
				Total  int `json:"total"`
				Issues []struct {
					Key string `json:"key"`
				} `json:"issues"`
			}
			kraw, kerr := x.search(few, key, i, 1, "summary", "")
			if kerr != nil || json.Unmarshal(kraw, &one) != nil || len(one.Issues) != 1 {
				failure = err
				unnamed = append(unnamed, i)
				continue
			}
			told, page.Total = true, one.Total
			x.log.Warn("an issue cannot be read; it is left out", "project", key, "issue", one.Issues[0].Key, "error", err)
			if err := x.cp.note(ExtractionNote{Kind: "issue", ID: key, Name: one.Issues[0].Key, Note: err.Error()}); err != nil {
				return nil, 0, err
			}
			if i+1 >= one.Total {
				break
			}
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		var one struct {
			Total  int               `json:"total"`
			Issues []json.RawMessage `json:"issues"`
		}
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, 0, err
		}
		told, page.Total = true, one.Total
		if len(one.Issues) == 0 {
			positions--
			break
		}
		issues = append(issues, one.Issues...)
		if i+1 >= one.Total {
			break
		}
	}
	if !told {
		// Not one issue of the page can be told: which ones are missing is unknown.
		return nil, 0, failure
	}
	for _, i := range unnamed {
		name := key + " #" + strconv.Itoa(i+1)
		x.log.Warn("an issue cannot be read; it is left out", "project", key, "issue", name, "error", failure)
		if err := x.cp.note(ExtractionNote{Kind: "issue", ID: key, Name: name, Note: failure.Error()}); err != nil {
			return nil, 0, err
		}
	}
	out, err := json.Marshal(map[string]any{"startAt": page.StartAt, "maxResults": page.MaxResults, "total": page.Total, "issues": issues})
	return out, positions, err
}

// issueDetails fetches comments, work logs and watchers when the search lists only some of them, the change log
// when it is cut off, and the attachment files.
func (x *jiraExtraction) issueDetails(ctx context.Context, project string, is jIssue) error {
	var comment struct {
		Total    int               `json:"total"`
		Comments []json.RawMessage `json:"comments"`
	}
	_ = json.Unmarshal(is.Fields["comment"], &comment)
	if comment.Total > len(comment.Comments) {
		if err := x.allPages(ctx, "/rest/api/2/issue/"+url.PathEscape(is.Key)+"/comment?expand=renderedBody", "comments",
			"jira", "projects", project, "comments", is.Key+".json"); err != nil {
			return err
		}
	}
	var worklog struct {
		Total    int               `json:"total"`
		Worklogs []json.RawMessage `json:"worklogs"`
	}
	_ = json.Unmarshal(is.Fields["worklog"], &worklog)
	if worklog.Total > len(worklog.Worklogs) {
		if err := x.allPages(ctx, "/rest/api/2/issue/"+url.PathEscape(is.Key)+"/worklog", "worklogs",
			"jira", "projects", project, "worklogs", is.Key+".json"); err != nil {
			return err
		}
	}
	var watches struct {
		WatchCount int `json:"watchCount"`
	}
	_ = json.Unmarshal(is.Fields["watches"], &watches)
	if watches.WatchCount > 0 {
		if _, err := x.save(ctx, "/rest/api/2/issue/"+url.PathEscape(is.Key)+"/watchers", "jira", "projects", project, "watchers", is.Key+".json"); err != nil {
			return err
		}
	}
	if is.Changelog != nil && is.Changelog.Total > len(is.Changelog.Histories) {
		if _, err := x.save(ctx, "/rest/api/2/issue/"+url.PathEscape(is.Key)+"?fields=created&expand=changelog", "jira", "projects", project,
			"changelogs", is.Key+".json"); err != nil {
			return err
		}
	}
	if x.opt.SkipAttachments {
		return nil
	}
	var atts []jAttachment
	_ = json.Unmarshal(is.Fields["attachment"], &atts)
	for _, a := range atts {
		file := x.work.Path("jira", "attachments", a.ID)
		if size := x.work.Size("jira", "attachments", a.ID); size >= 0 && (size == a.Size || a.Size <= 0) {
			continue // downloaded completely before (attachments do not change)
		}
		// The content URL carries the configured base URL, which may differ from the one used for the extraction.
		err := x.client.Download(ctx, "/secure/attachment/"+url.PathEscape(a.ID)+"/"+url.PathEscape(a.Filename), file, sizeOrUnknown(a.Size))
		if gone(err) {
			// Jira lists the file, but does not have it (any more): the load reports it as not extracted.
			if err := x.cp.note(ExtractionNote{Kind: "attachment", ID: a.ID, Name: is.Key + ": " + a.Filename, Note: err.Error()}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("attachment %s: %w", a.Filename, err)
		}
	}
	return nil
}

// sizeOrUnknown is the size a download is checked against: sizes of 0 are not checked (some sources do not
// know them).
func sizeOrUnknown(n int64) int64 {
	if n <= 0 {
		return -1
	}
	return n
}

// allPages reads every page of a paged list of an issue ({startAt, maxResults, total, <field>: [...]}) and stores
// them as one.
func (x *jiraExtraction) allPages(ctx context.Context, path, field string, parts ...string) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var items []json.RawMessage
	for start := 0; ; {
		raw, err := x.client.GetJSON(ctx, path+sep+"startAt="+strconv.Itoa(start)+"&maxResults=1000")
		if err != nil {
			return err
		}
		var page map[string]json.RawMessage
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		var list []json.RawMessage
		var total int
		_ = json.Unmarshal(page[field], &list)
		_ = json.Unmarshal(page["total"], &total)
		items = append(items, list...)
		start += len(list)
		if len(list) == 0 || start >= total {
			break
		}
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	b, err := json.Marshal(map[string]any{"startAt": 0, "maxResults": len(items), "total": len(items), field: items})
	if err != nil {
		return err
	}
	return x.work.Write(b, parts...)
}

// boards reads the boards of the project with their configuration and sprints (Jira Software).
func (x *jiraExtraction) boards(ctx context.Context, key string) error {
	raw, err := x.client.GetJSON(ctx, "/rest/agile/1.0/board?"+query(map[string]string{"projectKeyOrId": key}))
	if err != nil {
		x.log.Warn("no boards (Jira Software API not available?)", "project", key, "error", err)
		return nil
	}
	if err := x.work.Write(raw, "jira", "projects", key, "boards.json"); err != nil {
		return err
	}
	var boards jBoards
	_ = json.Unmarshal(raw, &boards)
	for _, b := range boards.Values {
		if err := x.extractBoard(ctx, key, b.ID); err != nil {
			return err
		}
		if b.Type != "scrum" {
			continue
		}
		for start, page := 0, 0; ; page++ {
			raw, err := x.save(ctx, fmt.Sprintf("/rest/agile/1.0/board/%d/sprint?startAt=%d&maxResults=50", b.ID, start),
				"jira", "projects", key, "sprints", fmt.Sprintf("board-%d-%03d.json", b.ID, page))
			if err != nil {
				return err
			}
			var p jPage[jSprint]
			_ = json.Unmarshal(raw, &p)
			start += len(p.Values)
			if p.IsLast || len(p.Values) == 0 {
				break
			}
		}
	}
	return nil
}

// extractBoard reads the configuration, the filter and the quick filters of a board.
func (x *jiraExtraction) extractBoard(ctx context.Context, project string, id int64) error {
	out := map[string]json.RawMessage{}
	raw, err := x.client.GetJSON(ctx, fmt.Sprintf("/rest/agile/1.0/board/%d/configuration", id))
	if err != nil {
		x.log.Warn("the board configuration could not be read", "board", id, "error", err)
		return nil
	}
	out["configuration"] = raw
	var d jBoardDetails
	_ = json.Unmarshal([]byte(`{"configuration":`+string(raw)+`}`), &d)
	if d.Configuration != nil && d.Configuration.Filter.ID != "" {
		if raw, err := x.client.GetJSON(ctx, "/rest/api/2/filter/"+url.PathEscape(d.Configuration.Filter.ID)); err == nil {
			out["filter"] = raw
		} else {
			x.log.Warn("the filter of a board could not be read", "board", id, "error", err)
		}
	}
	if raw, err := x.client.GetJSON(ctx, fmt.Sprintf("/rest/agile/1.0/board/%d/quickfilter?maxResults=50", id)); err == nil {
		out["quickFilters"] = raw
	}
	b, _ := json.Marshal(out)
	return x.work.Write(b, "jira", "projects", project, "boards", fmt.Sprintf("board-%d.json", id))
}

// access reads the project roles with their members and the permission scheme of a project. Reading permission
// schemes needs an administrator's token.
func (x *jiraExtraction) access(ctx context.Context, key string) error {
	if _, err := x.save(ctx, "/rest/api/2/project/"+key+"/permissionscheme?expand=all", "jira", "projects", key, "permissionscheme.json"); err != nil {
		x.log.Warn("the permission scheme could not be read; the project gets Kogge's default scheme", "project", key, "error", err)
		if err := os.Remove(x.work.Path("jira", "projects", key, "permissionscheme.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	raw, err := x.client.GetJSON(ctx, "/rest/api/2/project/"+key+"/role")
	if err != nil {
		return err
	}
	var refs map[string]string
	if err := json.Unmarshal(raw, &refs); err != nil {
		return err
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref[strings.LastIndex(ref, "/")+1:])
	}
	slices.Sort(ids)
	roles := []json.RawMessage{}
	for _, id := range ids {
		raw, err := x.client.GetJSON(ctx, "/rest/api/2/project/"+key+"/role/"+url.PathEscape(id))
		if err != nil {
			return err
		}
		roles = append(roles, raw)
	}
	b, _ := json.Marshal(roles)
	return x.work.Write(b, "jira", "projects", key, "roles.json")
}

// jiraGroupsOf lists the groups the stored permission scheme and roles of a project name.
func jiraGroupsOf(w Workdir, key string) ([]string, error) {
	var names []string
	var ps jPermissionScheme
	if _, err := w.Read(&ps, "jira", "projects", key, "permissionscheme.json"); err != nil {
		return nil, err
	}
	for _, p := range ps.Permissions {
		if p.Holder.Type == "group" && p.Holder.Parameter != "" {
			names = append(names, p.Holder.Parameter)
		}
	}
	var roles []jRole
	if _, err := w.Read(&roles, "jira", "projects", key, "roles.json"); err != nil {
		return nil, err
	}
	for _, r := range roles {
		for _, a := range r.Actors {
			if a.Type == "atlassian-group-role-actor" {
				names = append(names, a.Name)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// groups reads the members of the groups that permissions and roles of the projects name.
func (x *jiraExtraction) groups(ctx context.Context, keys []string) error {
	var names []string
	for _, key := range keys {
		list, err := jiraGroupsOf(x.work, key)
		if err != nil {
			return err
		}
		names = append(names, list...)
	}
	slices.Sort(names)
	for _, g := range slices.Compact(names) {
		err := x.cp.part("group/"+g, func() error {
			members := []json.RawMessage{}
			for start := 0; ; {
				raw, err := x.client.GetJSON(ctx, "/rest/api/2/group/member?"+query(map[string]string{"groupname": g, "startAt": strconv.Itoa(start),
					"maxResults": "50", "includeInactiveUsers": "true"}))
				if err != nil {
					return fmt.Errorf("members of group %s: %w", g, err)
				}
				var page jPage[json.RawMessage]
				if err := json.Unmarshal(raw, &page); err != nil {
					return err
				}
				members = append(members, page.Values...)
				start += len(page.Values)
				if page.IsLast || len(page.Values) == 0 {
					break
				}
			}
			b, _ := json.Marshal(members)
			return x.work.Write(b, "jira", "groups", url.PathEscape(g)+".json")
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// jWorkflowScheme maps issue types of a project to workflows.
type jWorkflowScheme struct {
	ID                int64             `json:"id"`
	Name              string            `json:"name"`
	DefaultWorkflow   string            `json:"defaultWorkflow"`
	IssueTypeMappings map[string]string `json:"issueTypeMappings"`
}

// workflowNames lists the workflows the projects use (from their stored workflow schemes).
func workflowNames(w Workdir, keys []string) ([]string, error) {
	var names []string
	for _, key := range keys {
		var ws jWorkflowScheme
		if _, err := w.Read(&ws, "jira", "projects", key, "workflowscheme.json"); err != nil {
			return nil, err
		}
		if ws.DefaultWorkflow != "" {
			names = append(names, ws.DefaultWorkflow)
		}
		for _, n := range ws.IssueTypeMappings {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// workflows reads the definitions (transitions with their conditions, validators and post-functions) of the
// workflows the projects use. Jira's REST API has no resource for them; the XML export of the administration is
// read instead, where the token's rights allow it. The analysis lists what they hold; without them it can only
// list the workflows' names.
func (x *jiraExtraction) workflows(ctx context.Context, keys []string) error {
	names, err := workflowNames(x.work, keys)
	if err != nil {
		return err
	}
	for _, name := range names {
		b, err := x.client.GetText(ctx, "/secure/admin/workflows/ViewWorkflowXml.jspa?"+query(map[string]string{"workflowMode": "live",
			"workflowName": name}), "application/xml, text/xml", 32<<20)
		if err == nil && !bytes.Contains(b[:min(len(b), 4096)], []byte("<workflow")) {
			err = fmt.Errorf("the answer is not a workflow (a login page?)")
		}
		if err != nil {
			x.log.Warn("the workflow definitions cannot be read; the analysis lists the workflows without their rules", "workflow", name, "error", err)
			return x.cp.note(ExtractionNote{Kind: "workflow", Name: name, Note: err.Error()})
		}
		if err := writeFile(x.work.Path("jira", "workflows", name+".xml"), b); err != nil {
			return err
		}
	}
	return nil
}
