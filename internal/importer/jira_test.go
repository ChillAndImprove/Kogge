// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/testutil/apitest"
)

const jiraToken = "jira-test-token"

// jiraUsers of the fixture: mmuster exists in Kogge, jdoe becomes a placeholder.
const (
	jdoe    = `{"name":"jdoe","key":"JIRAUSER10000","displayName":"John Doe","emailAddress":"john.doe@example.org","active":true}`
	mmuster = `{"name":"mmuster","key":"JIRAUSER10001","displayName":"Max Muster","emailAddress":"mmuster@example.com","active":true}`
)

func jiraIssues(base string) []string {
	r := strings.NewReplacer("{{BASE}}", base, "{{JDOE}}", jdoe, "{{MMUSTER}}", mmuster)
	return []string{
		// Ordered by rank: the story before its epic.
		r.Replace(`{"id":"10002","key":"SHOP-2","fields":{
			"summary":"Pay by invoice","issuetype":{"id":"7","name":"Story"},"status":{"id":"3","name":"In Progress"},
			"priority":{"id":"2","name":"High"},"resolution":null,"reporter":{{JDOE}},"creator":{{JDOE}},"assignee":{{MMUSTER}},
			"created":"2026-01-10T09:00:00.000+0100","updated":"2026-01-20T10:00:00.000+0100","resolutiondate":null,"duedate":"2026-03-01",
			"labels":["payment"],"components":[{"id":"100","name":"Frontend"}],"fixVersions":[{"id":"200","name":"1.0"}],"versions":[],
			"description":"See SHOP-3","environment":"",
			"timeoriginalestimate":7200,"timeestimate":3600,"timespent":3600,
			"comment":{"total":2,"maxResults":2,"comments":[
				{"id":"20001","author":{{JDOE}},"body":"Looks good","created":"2026-01-11T09:00:00.000+0100","updated":"2026-01-11T09:00:00.000+0100"},
				{"id":"20002","author":{{MMUSTER}},"body":"Internal note","created":"2026-01-12T09:00:00.000+0100","updated":"2026-01-12T09:00:00.000+0100",
				 "visibility":{"type":"role","value":"Developers"}}]},
			"worklog":{"total":1,"maxResults":20,"worklogs":[{"id":"30001","author":{{MMUSTER}},"comment":"Pairing","started":"2026-01-15T10:00:00.000+0100",
				"created":"2026-01-15T11:00:00.000+0100","timeSpentSeconds":3600}]},
			"watches":{"watchCount":0},"votes":{"votes":3},
			"attachment":[{"id":"10500","filename":"screenshot.png","author":{{JDOE}},"created":"2026-01-10T09:30:00.000+0100","size":8,
				"mimeType":"image/png","content":"{{BASE}}/secure/attachment/10500/screenshot.png"}],
			"issuelinks":[{"id":"40001","type":{"id":"10000","name":"Blocks"},"outwardIssue":{"id":"10003","key":"SHOP-3"}}],
			"customfield_10002":5,
			"customfield_10100":["com.atlassian.greenhopper.service.sprint.Sprint@1a2b[id=12,rapidViewId=1,state=ACTIVE,name=Sprint 2,startDate=2026-01-19T09:00:00.000+01:00,endDate=2026-02-02T09:00:00.000+01:00,completeDate=<null>,sequence=12,goal=Ship it]"],
			"customfield_10101":"SHOP-1",
			"customfield_10200":null},
		"renderedFields":{
			"description":"<p>See <a href=\"{{BASE}}/browse/SHOP-3\" class=\"issue-link\" data-issue-key=\"SHOP-3\">SHOP-3</a> and ask <a href=\"{{BASE}}/secure/ViewProfile.jspa?name=mmuster\" class=\"user-hover\" rel=\"mmuster\">Max Muster</a>.</p><p><span class=\"image-wrap\"><img src=\"{{BASE}}/secure/attachment/10500/screenshot.png\" alt=\"screenshot.png\"></span></p>",
			"comment":{"comments":[{"id":"20001","body":"<p>Looks <b>good</b></p>"},{"id":"20002","body":"<p>Internal note</p>"}]}},
		"changelog":{"histories":[
			{"id":"50001","author":{{JDOE}},"created":"2026-01-15T09:00:00.000+0100","items":[
				{"field":"status","fieldtype":"jira","from":"1","fromString":"Open","to":"3","toString":"In Progress"},
				{"field":"Story Points","fieldtype":"custom","from":null,"fromString":"3","to":null,"toString":"5"},
				{"field":"Rank","fieldtype":"custom","from":"","fromString":"","to":"","toString":"Ranked higher"},
				{"field":"Sprint","fieldtype":"custom","from":"","fromString":"","to":"12","toString":"Sprint 2"}]}]}}`),
		r.Replace(`{"id":"10001","key":"SHOP-1","fields":{
			"summary":"Checkout","issuetype":{"id":"6","name":"Epic"},"status":{"id":"3","name":"In Progress"},"priority":{"id":"3","name":"Medium"},
			"resolution":null,"reporter":{{JDOE}},"creator":{{JDOE}},"assignee":null,
			"created":"2026-01-05T09:00:00.000+0100","updated":"2026-01-06T10:00:00.000+0100",
			"labels":["shop"],"components":[],"fixVersions":[],"versions":[],"description":"The new checkout.",
			"comment":{"total":0,"comments":[]},"worklog":{"total":0,"worklogs":[]},"watches":{"watchCount":0},"votes":{"votes":0},
			"attachment":[],"issuelinks":[],"customfield_10100":null,"customfield_10101":null},
		"renderedFields":{"description":"<p>The new <b>checkout</b>.</p>"},"changelog":{"histories":[]}}`),
		r.Replace(`{"id":"10003","key":"SHOP-3","fields":{
			"summary":"Invoice total is wrong","issuetype":{"id":"1","name":"Bug"},"status":{"id":"10001","name":"Done"},
			"priority":{"id":"2","name":"High"},"resolution":{"id":"1","name":"Fixed"},"reporter":{{MMUSTER}},"creator":{{MMUSTER}},"assignee":{{JDOE}},
			"created":"2026-01-08T09:00:00.000+0100","updated":"2026-01-25T10:00:00.000+0100","resolutiondate":"2026-01-25T10:00:00.000+0100",
			"labels":[],"components":[],"fixVersions":[{"id":"200","name":"1.0"}],"versions":[],"description":null,
			"security":{"id":"10200","name":"Internal","description":"Staff only"},
			"comment":{"total":3,"maxResults":1,"comments":[{"id":"20003","author":{{JDOE}},"body":"First","created":"2026-01-09T09:00:00.000+0100","updated":"2026-01-09T09:00:00.000+0100"}]},
			"worklog":{"total":0,"worklogs":[]},"watches":{"watchCount":2},"votes":{"votes":0},"attachment":[],
			"issuelinks":[{"id":"40001","type":{"id":"10000","name":"Blocks"},"inwardIssue":{"id":"10002","key":"SHOP-2"}}],
			"customfield_10100":[
				{"id":11,"rapidViewId":1,"state":"closed","name":"Sprint 1","startDate":"2026-01-05T09:00:00.000+01:00","endDate":"2026-01-19T09:00:00.000+01:00","completeDate":"2026-01-19T09:00:00.000+01:00","goal":""},
				{"id":12,"rapidViewId":1,"state":"active","name":"Sprint 2","startDate":"2026-01-19T09:00:00.000+01:00","endDate":"2026-02-02T09:00:00.000+01:00","goal":"Ship it"}],
			"customfield_10200":{"id":"10400","value":"Payments"}},
		"renderedFields":{"description":null},"changelog":{"histories":[
			{"id":"50002","author":{{JDOE}},"created":"2026-01-25T10:00:00.000+0100","items":[
				{"field":"status","fieldtype":"jira","from":"3","fromString":"In Progress","to":"10001","toString":"Done"},
				{"field":"resolution","fieldtype":"jira","from":null,"fromString":null,"to":"1","toString":"Fixed"},
				{"field":"assignee","fieldtype":"jira","from":"mmuster","fromString":"Max Muster","to":"jdoe","toString":"John Doe"}]}]}}`),
		r.Replace(`{"id":"10004","key":"SHOP-4","fields":{
			"summary":"Add invoice address form","issuetype":{"id":"5","name":"Sub-task"},"status":{"id":"1","name":"Open"},
			"priority":{"id":"3","name":"Medium"},"resolution":null,"reporter":{{JDOE}},"creator":{{JDOE}},"assignee":null,
			"created":"2026-01-12T09:00:00.000+0100","updated":"2026-01-12T09:00:00.000+0100","parent":{"id":"10002","key":"SHOP-2"},
			"labels":[],"components":[],"fixVersions":[],"versions":[],"description":"",
			"comment":{"total":0,"comments":[]},"worklog":{"total":0,"worklogs":[]},"watches":{"watchCount":0},"votes":{"votes":0},
			"attachment":[],"issuelinks":[],"customfield_10100":null,"customfield_10101":null},
		"renderedFields":{},"changelog":{"histories":[]}}`),
	}
}

// jiraWorkflowXML is a workflow definition as Jira's administration exports it (OSWorkflow): a condition with a
// counterpart in Kogge, one without, a script and post-functions Kogge applies anyway.
const jiraWorkflowXML = `<?xml version="1.0" encoding="UTF-8"?>
<workflow>
  <initial-actions>
    <action id="1" name="Create">
      <results><unconditional-result old-status="null" status="open" step="1"><post-functions>
        <function type="class"><arg name="class.name">com.atlassian.jira.workflow.function.issue.IssueCreateFunction</arg></function>
      </post-functions></unconditional-result></results>
    </action>
  </initial-actions>
  <steps>
    <step id="1" name="Open"><meta name="jira.status.id">1</meta><actions>
      <action id="11" name="Start">
        <restrict-to><conditions type="AND">
          <condition type="class"><arg name="class.name">com.atlassian.jira.workflow.condition.AllowOnlyAssignee</arg></condition>
          <condition type="class"><arg name="class.name">com.atlassian.jira.workflow.condition.AlwaysFalseCondition</arg></condition>
        </conditions></restrict-to>
        <results><unconditional-result old-status="Not Done" status="Underway" step="2"><post-functions>
          <function type="class"><arg name="class.name">com.onresolve.jira.groovy.GroovyFunctionPlugin</arg></function>
        </post-functions></unconditional-result></results>
      </action>
    </actions></step>
    <step id="2" name="In Progress"><meta name="jira.status.id">3</meta></step>
  </steps>
</workflow>`

// fakeJira serves the REST resources the importer reads, in pages of two issues.
func fakeJira(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	fixed := map[string]string{
		"/rest/api/2/serverInfo": `{"baseUrl":"{{BASE}}","version":"9.12.4","deploymentType":"Server"}`,
		"/rest/api/2/issuetype": `[{"id":"1","name":"Bug","description":"A problem","subtask":false},{"id":"5","name":"Sub-task","subtask":true},
			{"id":"6","name":"Epic","subtask":false},{"id":"7","name":"Story","subtask":false}]`,
		"/rest/api/2/status": `[{"id":"1","name":"Open","statusCategory":{"key":"new"}},{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}},
			{"id":"10001","name":"Done","statusCategory":{"key":"done"}}]`,
		"/rest/api/2/priority":      `[{"id":"2","name":"High"},{"id":"3","name":"Medium"}]`,
		"/rest/api/2/resolution":    `[{"id":"1","name":"Fixed"}]`,
		"/rest/api/2/issueLinkType": `{"issueLinkTypes":[{"id":"10000","name":"Blocks","inward":"is blocked by","outward":"blocks"}]}`,
		"/rest/api/2/field": `[{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string","system":"summary"}},
			{"id":"customfield_10002","name":"Story Points","custom":true,"schema":{"type":"number","custom":"com.atlassian.jira.plugin.system.customfieldtypes:float"}},
			{"id":"customfield_10100","name":"Sprint","custom":true,"schema":{"type":"array","items":"string","custom":"com.pyxis.greenhopper.jira:gh-sprint"}},
			{"id":"customfield_10101","name":"Epic Link","custom":true,"schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link"}},
			{"id":"customfield_10102","name":"Rank","custom":true,"schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-lexo-rank"}},
			{"id":"customfield_10200","name":"Team","custom":true,"schema":{"type":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:select"}},
			{"id":"customfield_10300","name":"Checklist","custom":true,"schema":{"type":"any","custom":"com.example.checklist:items"}}]`,
		"/rest/api/2/project/SHOP": `{"id":"10100","key":"SHOP","name":"Web shop","description":"The online shop","projectTypeKey":"software",
			"projectCategory":{"name":"Commerce"},"lead":{{JDOE}},
			"components":[{"id":"100","name":"Frontend","description":"Web UI","lead":{{MMUSTER}}}],
			"versions":[{"id":"200","name":"1.0","description":"First release","releaseDate":"2026-02-01","released":false,"archived":false}],
			"issueTypes":[{"id":"1","name":"Bug"},{"id":"5","name":"Sub-task","subtask":true},{"id":"6","name":"Epic"},{"id":"7","name":"Story"}]}`,
		"/rest/api/2/project/SHOP/statuses": `[{"id":"7","name":"Story","statuses":[{"id":"1","name":"Open","statusCategory":{"key":"new"}},
			{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}},{"id":"10001","name":"Done","statusCategory":{"key":"done"}}]}]`,
		"/rest/api/2/issue/SHOP-3/comment": `{"startAt":0,"maxResults":5000,"total":3,"comments":[
			{"id":"20003","author":{{JDOE}},"body":"First","renderedBody":"<p>First</p>","created":"2026-01-09T09:00:00.000+0100","updated":"2026-01-09T09:00:00.000+0100"},
			{"id":"20004","author":{{MMUSTER}},"body":"Second","renderedBody":"<p>Second with <code>code</code></p>","created":"2026-01-10T09:00:00.000+0100","updated":"2026-01-10T09:00:00.000+0100"},
			{"id":"20005","author":{{JDOE}},"body":"Secret","renderedBody":"<p>Secret</p>","created":"2026-01-11T09:00:00.000+0100","updated":"2026-01-11T09:00:00.000+0100",
			 "visibility":{"type":"group","value":"jira-auditors"}}]}`,
		"/rest/api/2/issue/SHOP-3/watchers": `{"watchCount":2,"watchers":[{{JDOE}},{{MMUSTER}}]}`,
		"/rest/api/2/project/SHOP/permissionscheme": `{"id":0,"name":"Default Permission Scheme","description":"Shared by all projects","permissions":[
			{"permission":"BROWSE_PROJECTS","holder":{"type":"applicationRole","parameter":""}},
			{"permission":"VIEW_VOTERS_AND_WATCHERS","holder":{"type":"applicationRole","parameter":""}},
			{"permission":"ADMINISTER_PROJECTS","holder":{"type":"projectRole","parameter":"10002","projectRole":{"name":"Administrators","id":10002}}},
			{"permission":"CREATE_ISSUES","holder":{"type":"projectRole","parameter":"10002","projectRole":{"name":"Administrators","id":10002}}},
			{"permission":"CREATE_ISSUES","holder":{"type":"group","parameter":"jira-developers"}},
			{"permission":"EDIT_ISSUES","holder":{"type":"projectRole","parameter":"10001","projectRole":{"name":"Developers","id":10001}}},
			{"permission":"ADD_COMMENTS","holder":{"type":"userCustomField","parameter":"customfield_10500"}},
			{"permission":"VIEW_READONLY_WORKFLOW","holder":{"type":"applicationRole","parameter":""}}]}`,
		"/rest/api/2/project/SHOP/role": `{"Administrators":"{{BASE}}/rest/api/2/project/10100/role/10002","Developers":"{{BASE}}/rest/api/2/project/10100/role/10001"}`,
		"/rest/api/2/project/SHOP/role/10002": `{"name":"Administrators","id":10002,"description":"Project administrators","actors":[
			{"id":1,"type":"atlassian-group-role-actor","name":"jira-administrators","displayName":"jira-administrators"}]}`,
		"/rest/api/2/project/SHOP/role/10001": `{"name":"Developers","id":10001,"description":"","actors":[
			{"id":2,"type":"atlassian-group-role-actor","name":"jira-developers","displayName":"jira-developers"},
			{"id":3,"type":"atlassian-user-role-actor","name":"jdoe","displayName":"John Doe"}]}`,
		"/rest/agile/1.0/board": `{"maxResults":50,"startAt":0,"isLast":true,"values":[{"id":1,"name":"SHOP board","type":"scrum"},
			{"id":2,"name":"SHOP bugs","type":"kanban"}]}`,
		"/rest/agile/1.0/board/1/configuration": `{"id":1,"name":"SHOP board","type":"scrum","filter":{"id":"10000"},
			"columnConfig":{"columns":[{"name":"To Do","statuses":[{"id":"1"}]},{"name":"Doing","statuses":[{"id":"3"}],"max":5},
			{"name":"Done","statuses":[{"id":"10001"}]}],"constraintType":"issueCount"},
			"estimation":{"type":"field","field":{"fieldId":"customfield_10002","displayName":"Story Points"}}}`,
		"/rest/api/2/filter/10000": `{"id":"10000","name":"Filter for SHOP board","jql":"project = SHOP ORDER BY Rank ASC"}`,
		"/rest/agile/1.0/board/1/quickfilter": `{"maxResults":50,"startAt":0,"isLast":true,"values":[
			{"id":1,"boardId":1,"name":"Only my issues","jql":"assignee = currentUser()"},
			{"id":2,"boardId":1,"name":"Big ones","jql":"cf[10002] >= 5"},
			{"id":3,"boardId":1,"name":"Broken","jql":"status = = Done"}]}`,
		"/rest/agile/1.0/board/2/configuration": `{"id":2,"name":"SHOP bugs","type":"kanban","filter":{"id":"10001"},
			"subQuery":{"query":"fixVersion in unreleasedVersions() OR fixVersion is EMPTY"},
			"columnConfig":{"columns":[{"name":"Backlog","statuses":[{"id":"1"}]},{"name":"Selected","statuses":[]},
			{"name":"In Progress","statuses":[{"id":"3"}]},{"name":"Done","statuses":[{"id":"10001"}]}],"constraintType":"none"},
			"estimation":{"type":"issueCount"}}`,
		"/rest/api/2/filter/10001": `{"id":"10001","name":"Filter for SHOP bugs","jql":"project = SHOP AND issuetype = Bug ORDER BY Rank ASC"}`,
		"/rest/api/2/workflow": `[{"name":"jira","description":"The default workflow","steps":5,"default":true},
			{"name":"SHOP bugs","description":"","steps":3,"default":false}]`,
		"/rest/api/2/project/SHOP/workflowscheme": `{"id":10100,"name":"SHOP workflow scheme","defaultWorkflow":"jira",
			"issueTypeMappings":{"1":"SHOP bugs"},"draft":false}`,
		"/rest/api/2/project/SHOP/notificationscheme": `{"id":10000,"name":"Default Notification Scheme","notificationSchemeEvents":[
			{"event":{"id":1,"name":"Issue Created"},"notifications":[{"id":1,"notificationType":"CurrentAssignee"},
			{"id":2,"notificationType":"Group","parameter":"jira-developers"}]}]}`,
		"/rest/api/2/project/SHOP/issuesecuritylevelscheme": `{"id":10000,"name":"SHOP security","levels":[
			{"id":"10200","name":"Internal","description":"Staff only"},{"id":"10201","name":"Board","description":""}]}`,
		"/rest/agile/1.0/board/1/sprint": `{"maxResults":50,"startAt":0,"isLast":true,"values":[
			{"id":11,"state":"closed","name":"Sprint 1","startDate":"2026-01-05T09:00:00.000+01:00","endDate":"2026-01-19T09:00:00.000+01:00","completeDate":"2026-01-19T09:00:00.000+01:00"},
			{"id":12,"state":"active","name":"Sprint 2","startDate":"2026-01-19T09:00:00.000+01:00","endDate":"2026-02-02T09:00:00.000+01:00","goal":"Ship it"},
			{"id":13,"state":"future","name":"Sprint 3"}]}`,
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+jiraToken {
			http.Error(w, `{"errorMessages":["unauthorized"]}`, http.StatusUnauthorized)
			return
		}
		replace := strings.NewReplacer("{{BASE}}", srv.URL, "{{JDOE}}", jdoe, "{{MMUSTER}}", mmuster)
		if r.URL.Path == "/secure/attachment/10500/screenshot.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
			return
		}
		if r.URL.Path == "/rest/api/2/search" {
			if !strings.Contains(r.URL.Query().Get("jql"), `project = "SHOP"`) {
				http.Error(w, "unexpected query", http.StatusBadRequest)
				return
			}
			start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
			all := jiraIssues(srv.URL)
			size := 2 // whatever the client asks for, unless it is less
			if n, err := strconv.Atoi(r.URL.Query().Get("maxResults")); err == nil && n < size {
				size = n
			}
			end := min(start+size, len(all))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"startAt":` + strconv.Itoa(start) + `,"maxResults":2,"total":` + strconv.Itoa(len(all)) + `,"issues":[` +
				strings.Join(all[start:end], ",") + `]}`))
			return
		}
		if r.URL.Path == "/secure/admin/workflows/ViewWorkflowXml.jspa" {
			// The export of a workflow; the default one is not readable with this token.
			if r.URL.Query().Get("workflowName") != "SHOP bugs" {
				http.Error(w, "<html>Log in</html>", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(jiraWorkflowXML))
			return
		}
		if r.URL.Path == "/rest/api/2/group/member" {
			members := map[string]string{"jira-administrators": `{"name":"admin","key":"admin","displayName":"Administrator","active":true}`,
				"jira-developers": mmuster}[r.URL.Query().Get("groupname")]
			_, _ = w.Write([]byte(`{"startAt":0,"maxResults":50,"total":1,"isLast":true,"values":[` + members + `]}`))
			return
		}
		body, ok := fixed[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(replace.Replace(body)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type issueJSON struct {
	Key     string                `json:"key"`
	Summary string                `json:"summary"`
	Rank    string                `json:"rank"`
	Status  struct{ Name string } `json:"status"`
	Type    struct {
		ID   int64
		Name string
	} `json:"type"`
	Assignee *struct {
		Username string `json:"username"`
	} `json:"assignee"`
	Reporter *struct {
		Username string `json:"username"`
	} `json:"reporter"`
	Parent *struct {
		Key string `json:"key"`
	} `json:"parent"`
	SecurityLevel *struct {
		Name string `json:"name"`
	} `json:"securityLevel"`
	Resolution *struct{ Name string }  `json:"resolution"`
	Sprint     *struct{ Name string }  `json:"sprint"`
	Closed     []struct{ Name string } `json:"closedSprints"`
	Fields     map[string]any          `json:"fields"`
	Labels     []string                `json:"labels"`
	Components []struct{ Name string } `json:"components"`
	FixVers    []struct{ Name string } `json:"fixVersions"`
	Votes      int                     `json:"votes"`
	Watchers   int                     `json:"watchers"`
	TimeSpent  int64                   `json:"timeSpent"`
	Links      []struct {
		Direction string `json:"direction"`
		Issue     struct {
			Key string `json:"key"`
		} `json:"issue"`
	} `json:"links"`
	Attachments []struct {
		ID       int64  `json:"id"`
		Filename string `json:"filename"`
	} `json:"attachments"`
	Description map[string]any `json:"description"`
}

func TestJiraImport(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	env.CreateUser("mmuster")
	fake := fakeJira(t)
	work := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()

	// A wrong token stops the extraction with the source's answer.
	bad := importer.NewJira(importer.JiraOptions{URL: fake.URL, Token: "wrong", Projects: []string{"SHOP"}, Workdir: t.TempDir()}, log)
	if err := bad.Extract(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("extract with a wrong token: %v", err)
	}

	j := importer.NewJira(importer.JiraOptions{URL: fake.URL, Token: jiraToken, Projects: []string{"shop"}, Workdir: work}, log)
	if err := j.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"jira/serverInfo.json", "jira/projects/SHOP/issues/page-00000.json", "jira/projects/SHOP/issues/page-00001.json",
		"jira/projects/SHOP/comments/SHOP-3.json", "jira/projects/SHOP/watchers/SHOP-3.json", "jira/attachments/10500",
		"jira/projects/SHOP/sprints/board-1-000.json"} {
		if _, err := os.Stat(work + "/" + f); err != nil {
			t.Errorf("not extracted: %s", f)
		}
	}
	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Log: log}
	rep, err := j.Load(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["issues"] != 4 || rep.Counts["comments"] != 5 || rep.Counts["attachments"] != 1 || rep.Counts["worklogs"] != 1 ||
		rep.Counts["sprints"] != 3 {
		t.Errorf("counts = %v", rep.Counts)
	}
	if strings.Join(rep.UsersMatched, ",") != "admin,mmuster" || strings.Join(rep.Placeholders, ",") != "jdoe" {
		t.Errorf("users: matched %v, placeholders %v", rep.UsersMatched, rep.Placeholders)
	}
	if rep.Counts["groups"] != 2 || rep.Unsupported["custom field Checklist (com.example.checklist:items)"] != 1 ||
		rep.Unsupported["permission VIEW_READONLY_WORKFLOW"] != 1 || rep.Unsupported["permission holder userCustomField"] != 1 {
		t.Errorf("unsupported = %v", rep.Unsupported)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), `security level "Internal"`) ||
		!strings.Contains(strings.Join(rep.Warnings, "\n"), `group "jira-auditors"`) {
		t.Errorf("warnings = %v", rep.Warnings)
	}
	if _, err := os.Stat(work + "/jira-report.json"); err != nil {
		t.Error("no report")
	}

	var story issueJSON
	admin.Get("/api/v1/issues/SHOP-2", &story)
	if story.Summary != "Pay by invoice" || story.Status.Name != "In Progress" || story.Type.Name != "Story" ||
		story.Assignee == nil || story.Assignee.Username != "mmuster" || story.Reporter == nil || story.Reporter.Username != "jdoe" ||
		story.Parent == nil || story.Parent.Key != "SHOP-1" || story.Sprint == nil || story.Sprint.Name != "Sprint 2" ||
		story.Votes != 3 || story.TimeSpent != 3600 || len(story.Labels) != 1 || len(story.Components) != 1 || len(story.FixVers) != 1 {
		t.Errorf("SHOP-2 = %+v", story)
	}
	if len(story.Attachments) != 1 || story.Attachments[0].Filename != "screenshot.png" {
		t.Fatalf("attachments = %+v", story.Attachments)
	}
	desc, _ := json.Marshal(story.Description)
	for _, want := range []string{`"type":"issue"`, `"key":"SHOP-3"`, `"type":"mention"`, `"/api/v1/issue-attachments/` + strconv.FormatInt(story.Attachments[0].ID, 10)} {
		if !strings.Contains(string(desc), want) {
			t.Errorf("description lacks %s: %s", want, desc)
		}
	}
	points := false
	for _, v := range story.Fields {
		if v == 5.0 {
			points = true
		}
	}
	if !points {
		t.Errorf("story points missing: %v", story.Fields)
	}
	if len(story.Links) != 1 || story.Links[0].Issue.Key != "SHOP-3" {
		t.Errorf("links = %+v", story.Links)
	}

	var epic, bug, sub issueJSON
	admin.Get("/api/v1/issues/SHOP-1", &epic)
	admin.Get("/api/v1/issues/SHOP-3", &bug)
	admin.Get("/api/v1/issues/SHOP-4", &sub)
	if !(story.Rank < epic.Rank && epic.Rank < bug.Rank && bug.Rank < sub.Rank) {
		t.Errorf("ranks do not follow the source order: %s %s %s %s", story.Rank, epic.Rank, bug.Rank, sub.Rank)
	}
	if bug.Resolution == nil || bug.Resolution.Name != "Fixed" || bug.SecurityLevel == nil || bug.SecurityLevel.Name != "Internal" ||
		bug.Sprint == nil || bug.Sprint.Name != "Sprint 2" || len(bug.Closed) != 1 || bug.Watchers != 2 {
		t.Errorf("SHOP-3 = %+v", bug)
	}
	team := false
	for _, v := range bug.Fields {
		if _, ok := v.(float64); ok {
			team = true
		}
	}
	if !team {
		t.Errorf("select option missing: %v", bug.Fields)
	}
	if sub.Parent == nil || sub.Parent.Key != "SHOP-2" || sub.Status.Name != "Open" {
		t.Errorf("SHOP-4 = %+v", sub)
	}

	// The security level hides the bug from users outside the Administrators role.
	if res := env.Login("mmuster", apitest.UserPassword).Do(http.MethodGet, "/api/v1/issues/SHOP-3", nil); res.Status != http.StatusNotFound {
		t.Errorf("restricted issue visible to mmuster: %d", res.Status)
	}
	// Who may edit issues (the Developers, mmuster through the group jira-developers) may change them in bulk.
	var project struct {
		Permissions []string `json:"permissions"`
	}
	env.Login("mmuster", apitest.UserPassword).Get("/api/v1/projects/SHOP", &project)
	if !slices.Contains(project.Permissions, "issue.edit") || !slices.Contains(project.Permissions, "issue.bulk-change") {
		t.Errorf("permissions of mmuster: %v", project.Permissions)
	}

	var comments []struct {
		VisibilityType string         `json:"visibilityType"`
		Body           map[string]any `json:"body"`
	}
	admin.Get("/api/v1/issues/SHOP-3/comments", &comments)
	if len(comments) != 3 || comments[2].VisibilityType != "role" {
		t.Errorf("SHOP-3 comments = %+v", comments)
	}
	var history []struct {
		Items []struct {
			Field string `json:"field"`
			From  any    `json:"from"`
			To    any    `json:"to"`
		} `json:"items"`
	}
	admin.Get("/api/v1/issues/SHOP-2/history", &history)
	fields := []string{}
	for _, h := range history {
		for _, it := range h.Items {
			fields = append(fields, it.Field)
		}
	}
	if s := strings.Join(fields, ","); !strings.Contains(s, "status") || !strings.Contains(s, "sprint") || strings.Contains(s, "rank") ||
		!strings.Contains(s, "cf_") {
		t.Errorf("history fields = %v", s)
	}

	var search struct {
		Total  int `json:"total"`
		Issues []struct {
			Key string `json:"key"`
		} `json:"issues"`
	}
	admin.Get("/api/v1/search?q="+apitest.Query(`project = SHOP AND text ~ "invoice"`), &search)
	if search.Total != 3 {
		t.Errorf("search found %d issues", search.Total)
	}

	// Boards keep their columns, filters, quick filters and estimation.
	var boardPage struct {
		Items []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Type   string `json:"type"`
			Query  string `json:"query"`
			Config struct {
				Columns []struct {
					Name     string  `json:"name"`
					Statuses []int64 `json:"statuses"`
					Max      *int    `json:"max"`
				} `json:"columns"`
				QuickFilters []struct {
					Name  string `json:"name"`
					Query string `json:"query"`
				} `json:"quickFilters"`
				Estimation struct {
					Type  string `json:"type"`
					Field string `json:"field"`
				} `json:"estimation"`
				SubQuery string `json:"subQuery"`
			} `json:"config"`
		} `json:"items"`
	}
	admin.Get("/api/v1/boards?project=SHOP", &boardPage)
	boards := boardPage.Items
	if len(boards) != 2 {
		t.Fatalf("boards = %+v", boards)
	}
	scrum, kanban := boards[0], boards[1]
	if scrum.Type != "scrum" {
		scrum, kanban = kanban, scrum
	}
	if scrum.Name != "SHOP board" || scrum.Query != "project = SHOP ORDER BY Rank ASC" || len(scrum.Config.Columns) != 3 ||
		scrum.Config.Columns[1].Max == nil || *scrum.Config.Columns[1].Max != 5 || len(scrum.Config.QuickFilters) != 2 ||
		!strings.HasPrefix(scrum.Config.QuickFilters[1].Query, "cf[") || scrum.Config.Estimation.Type != "field" {
		t.Errorf("scrum board = %+v", scrum)
	}
	if kanban.Name != "SHOP bugs" || kanban.Type != "kanban" || len(kanban.Config.Columns) != 4 || kanban.Config.SubQuery == "" ||
		!strings.Contains(kanban.Query, "issuetype = Bug") {
		t.Errorf("kanban board = %+v", kanban)
	}
	// The kanban board's query selects the bug (the board hides it: it is done for more than two weeks).
	var bugs struct {
		Total int `json:"total"`
	}
	admin.Get("/api/v1/search?q="+apitest.Query(kanban.Query), &bugs)
	if bugs.Total != 1 {
		t.Errorf("the kanban board's query finds %d issues", bugs.Total)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), `quick filter "Broken"`) {
		t.Errorf("no warning for the broken quick filter: %v", rep.Warnings)
	}

	// Old addresses lead to the migrated issues, boards and files.
	for path, want := range map[string]string{
		"/secure/RapidBoard.jspa?rapidView=1":               "/projects/SHOP/boards/",
		"/secure/RapidBoard.jspa?rapidView=1&view=planning": "/backlog",
		"/secure/RapidBoard.jspa?rapidView=2":               "/projects/SHOP/boards/" + strconv.FormatInt(kanban.ID, 10),
		"/browse/SHOP":                                      "/projects/SHOP",
		"/projects/SHOP/issues/SHOP-2":                      "/browse/SHOP-2",
		"/secure/attachment/10500/screenshot.png":           "/api/v1/issue-attachments/" + strconv.FormatInt(story.Attachments[0].ID, 10),
		"/issues/?jql=" + apitest.Query("project = SHOP"):   "/issues/search?q=project+%3D+SHOP",
	} {
		res := admin.Do(http.MethodGet, path, nil)
		loc := res.Header.Get("Location")
		if res.Status != http.StatusFound || !(strings.HasPrefix(loc, want) || strings.HasSuffix(loc, want)) {
			t.Errorf("GET %s: %d %s, want %s", path, res.Status, loc, want)
		}
	}
	if res := admin.Do(http.MethodGet, "/browse/SHOP-2", nil); res.Status != http.StatusOK {
		t.Errorf("issue page: %d", res.Status)
	}

	// A second load updates instead of duplicating; new issues continue the numbering.
	if _, err := j.Load(ctx, loader); err != nil {
		t.Fatal(err)
	}
	admin.Get("/api/v1/issues/SHOP-3/comments", &comments)
	var again issueJSON
	admin.Get("/api/v1/issues/SHOP-2", &again)
	if len(comments) != 3 || len(again.Attachments) != 1 || len(again.Links) != 1 || again.Rank != story.Rank {
		t.Errorf("second load: %d comments, %+v", len(comments), again)
	}
	var created struct {
		Key string `json:"key"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": bug.Type.ID, "summary": "After the migration"}, &created)
	if created.Key != "SHOP-5" {
		t.Errorf("new issue key = %s", created.Key)
	}
}
