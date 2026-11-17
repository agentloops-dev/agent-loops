//go:build live

package agentsd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/connections"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// forge/priority and forge/hold on real forges (#6111, after #6091), through the daemon as a
// client calls them, run by hand, never by CI. Each forge's test is skipped when its account is
// not set:
//
//	AGENTS_LIVE_GITLAB_TOKEN=$(cat ~/.agents/secrets/gitlab-token) go test -tags live -run LivePlanFields -v ./internal/agentsd/
//
//	AGENTS_LIVE_JIRA_URL=https://agentstest.atlassian.net/ AGENTS_LIVE_JIRA_EMAIL=… AGENTS_LIVE_JIRA_TOKEN=… AGENTS_LIVE_JIRA_PROJECT=KAN …
//	AGENTS_LIVE_BITBUCKET_EMAIL=… AGENTS_LIVE_BITBUCKET_TOKEN=… AGENTS_LIVE_BITBUCKET_REPO=workspace/repo …
//	AGENTS_LIVE_AZURE_DEVOPS_TOKEN=… AGENTS_LIVE_BOARDS_PROJECT=agentloops/agents-walk …
//
// Each test opens an issue, sets each priority and none, puts it on hold and takes it off,
// reads the item back after each write, and closes the issue.

// livePlanWalk sets each priority and the hold on node, and reads each back. reads maps a
// priority to what a forge reads back where that differs: Bitbucket's major is both medium and
// none, and its read takes it as none.
func livePlanWalk(t *testing.T, c *conn, node wire.LineageNode, reads map[string]string) {
	t.Helper()
	read := func(id int) wire.ForgeItem {
		var d wire.ForgeItemDetail
		if err := json.Unmarshal(c.call(t, id, "forge/issue", wire.ForgeItemRequest{Node: node}), &d); err != nil {
			t.Fatal(err)
		}
		return d.Item
	}
	id := 200
	for _, p := range []string{"high", "medium", "low", ""} {
		id++
		c.call(t, id, "forge/priority", wire.ForgePriorityRequest{Node: node, Priority: setTo(p)})
		id++
		want, ok := reads[p]
		if !ok {
			want = p
		}
		if got := deref(read(id).Priority); got != want {
			t.Fatalf("set %q, read %q, want %q", p, got, want)
		}
	}
	for _, on := range []bool{true, false} {
		id++
		c.call(t, id, "forge/hold", wire.ForgeHoldRequest{Node: node, On: on})
		id++
		if h := read(id).OnHold; (h != nil && *h) != on {
			got := h != nil && *h
			t.Fatalf("hold %v, read %v", on, got)
		}
	}
}

// liveJSON sends body to u with the headers and decodes the answer into out.
func liveJSON(t *testing.T, method, u string, header map[string]string, body, out any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, u, res.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v", method, u, err)
		}
	}
}

func liveTitle() string {
	return "Live #6111: a priority and a hold (" + time.Now().UTC().Format("2006-01-02 15:04") + ")"
}

// GitLab has no field for either: the labels priority:* and on hold (#6091).
func TestLivePlanFieldsGitLab(t *testing.T) {
	token := os.Getenv("AGENTS_LIVE_GITLAB_TOKEN")
	if token == "" {
		t.Skip("AGENTS_LIVE_GITLAB_TOKEN is not set")
	}
	project := os.Getenv("AGENTS_LIVE_GITLAB_PROJECT")
	if project == "" {
		project = "agentstest-group/agents-walk"
	}
	personalHomeFor(t, t.TempDir())
	d, c := start(t, t.TempDir())
	t.Cleanup(d.Shutdown)
	file := &connections.File{Path: filepath.Join(t.TempDir(), ".agents-secrets", connections.FileName)}
	d.connStoreFor = func() (connections.Store, error) { return file, nil }
	c.call(t, 100, "folders/add", map[string]any{"folder": "file://" + gitFolder(t, "git@gitlab.com:"+project+".git")})
	if err := file.Put(connections.Connection{Provider: connections.GitLab, Login: "agentstest", Access: token, URL: connections.GitLabCom,
		AccessExpiresAt: time.Now().Add(time.Hour), ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	api := "https://gitlab.com/api/v4/projects/" + url.PathEscape(project) + "/issues"
	header := map[string]string{"PRIVATE-TOKEN": token}
	var made struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
	}
	liveJSON(t, http.MethodPost, api, header, map[string]any{"title": liveTitle()}, &made)
	t.Cleanup(func() {
		liveJSON(t, http.MethodPut, fmt.Sprintf("%s/%d", api, made.IID), header, map[string]any{"state_event": "close"}, nil)
	})
	t.Logf("issue %s", made.WebURL)
	livePlanWalk(t, c, wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("gitlab:issue:%s#%d", project, made.IID)}, nil)
}

// liveSecretsHome is a home whose secrets file has lines, as the jira and azuredevops
// connectors' sign-in is read from it.
func liveSecretsHome(t *testing.T, lines ...string) {
	t.Helper()
	home := t.TempDir()
	personalHomeFor(t, home)
	os.MkdirAll(filepath.Join(home, ".agents-secrets"), 0o700)
	if err := os.WriteFile(filepath.Join(home, ".agents-secrets", "secrets.env"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// liveFolderNaming is a folder on no forge whose .agents/folder.json says json.
func liveFolderNaming(t *testing.T, c *conn, json string) {
	t.Helper()
	folder := gitFolder(t, "")
	os.MkdirAll(filepath.Join(folder, ".agents"), 0o755)
	if err := os.WriteFile(filepath.Join(folder, ".agents", "folder.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	c.call(t, 100, "folders/add", map[string]any{"folder": "file://" + folder})
}

func basicAuth(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

// Jira: the priority field (High, Medium, Low, and none), and a status named On hold where the
// workflow has one, else the label on-hold (#6091).
func TestLivePlanFieldsJira(t *testing.T) {
	site, email, token, project := os.Getenv("AGENTS_LIVE_JIRA_URL"), os.Getenv("AGENTS_LIVE_JIRA_EMAIL"), os.Getenv("AGENTS_LIVE_JIRA_TOKEN"), os.Getenv("AGENTS_LIVE_JIRA_PROJECT")
	if site == "" || email == "" || token == "" || project == "" {
		t.Skip("AGENTS_LIVE_JIRA_URL, _EMAIL, _TOKEN and _PROJECT are not set")
	}
	site = strings.TrimSuffix(site, "/") + "/"
	liveSecretsHome(t, "JIRA_URL="+site, "JIRA_EMAIL="+email, "JIRA_API_TOKEN="+token)
	d, c := start(t, t.TempDir())
	t.Cleanup(d.Shutdown)
	liveFolderNaming(t, c, fmt.Sprintf(`{"jiraProjects": [%q]}`, project))
	header := map[string]string{"Authorization": basicAuth(email, token)}
	issueType := os.Getenv("AGENTS_LIVE_JIRA_TYPE")
	if issueType == "" {
		issueType = "Task"
	}
	var made struct{ Key string }
	liveJSON(t, http.MethodPost, site+"rest/api/3/issue", header, map[string]any{"fields": map[string]any{
		"project": map[string]string{"key": project}, "summary": liveTitle(), "issuetype": map[string]string{"name": issueType}}}, &made)
	t.Cleanup(func() { liveJSON(t, http.MethodDelete, site+"rest/api/3/issue/"+made.Key, header, nil, nil) })
	t.Logf("issue %sbrowse/%s", site, made.Key)
	livePlanWalk(t, c, wire.LineageNode{Kind: "issue", Ref: "jira:issue:" + strings.ToLower(project) + "#" + made.Key}, nil)
}

// Bitbucket: an issue's priority (critical, major, minor) and its state on hold, then open
// (#6091). Its major is medium and none alike, and the read takes it as none.
func TestLivePlanFieldsBitbucket(t *testing.T) {
	email, token, repo := os.Getenv("AGENTS_LIVE_BITBUCKET_EMAIL"), os.Getenv("AGENTS_LIVE_BITBUCKET_TOKEN"), os.Getenv("AGENTS_LIVE_BITBUCKET_REPO")
	if email == "" || token == "" || repo == "" {
		t.Skip("AGENTS_LIVE_BITBUCKET_EMAIL, _TOKEN and _REPO are not set")
	}
	personalHomeFor(t, t.TempDir())
	d, c := start(t, t.TempDir())
	t.Cleanup(d.Shutdown)
	file := &connections.File{Path: filepath.Join(t.TempDir(), ".agents-secrets", connections.FileName)}
	d.connStoreFor = func() (connections.Store, error) { return file, nil }
	c.call(t, 100, "folders/add", map[string]any{"folder": "file://" + gitFolder(t, "git@bitbucket.org:"+repo+".git")})
	if err := file.Put(connections.BitbucketConnection(connections.BitbucketAPI, email, token, strings.Split(repo, "/")[0], time.Now())); err != nil {
		t.Fatal(err)
	}
	api := "https://api.bitbucket.org/2.0/repositories/" + repo + "/issues"
	header := map[string]string{"Authorization": basicAuth(email, token)}
	var made struct {
		ID    int `json:"id"`
		Links struct {
			HTML struct{ Href string } `json:"html"`
		} `json:"links"`
	}
	liveJSON(t, http.MethodPost, api, header, map[string]any{"title": liveTitle()}, &made)
	t.Cleanup(func() { liveJSON(t, http.MethodDelete, fmt.Sprintf("%s/%d", api, made.ID), header, nil, nil) })
	t.Logf("issue %s", made.Links.HTML.Href)
	livePlanWalk(t, c, wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("bitbucket:issue:%s#%d", strings.ToLower(repo), made.ID)},
		map[string]string{"medium": ""})
}

// Azure Boards: the work item's Priority field (1, 2, 3, and none) where its type has one, else
// the tags; on hold is the tag on hold (#6091). AGENTS_LIVE_BOARDS_TYPE is a type with Priority
// (Issue, Basic's, when empty); AGENTS_LIVE_BOARDS_TAGS_TYPE, when set, is one without it.
func TestLivePlanFieldsAzureBoards(t *testing.T) {
	token, project := os.Getenv("AGENTS_LIVE_AZURE_DEVOPS_TOKEN"), os.Getenv("AGENTS_LIVE_BOARDS_PROJECT")
	if token == "" || project == "" {
		t.Skip("AGENTS_LIVE_AZURE_DEVOPS_TOKEN and AGENTS_LIVE_BOARDS_PROJECT (org/project) are not set")
	}
	liveSecretsHome(t, "AZURE_DEVOPS_URL=https://dev.azure.com/", "AZURE_DEVOPS_TOKEN="+token)
	d, c := start(t, t.TempDir())
	t.Cleanup(d.Shutdown)
	liveFolderNaming(t, c, fmt.Sprintf(`{"azureBoardsProjects": [%q]}`, project))
	org, name, _ := strings.Cut(project, "/")
	header := map[string]string{"Authorization": basicAuth("", token), "Content-Type": "application/json-patch+json"}
	withPriority := os.Getenv("AGENTS_LIVE_BOARDS_TYPE")
	if withPriority == "" {
		withPriority = "Issue"
	}
	for _, typ := range []string{withPriority, os.Getenv("AGENTS_LIVE_BOARDS_TAGS_TYPE")} {
		if typ == "" {
			continue
		}
		t.Run(typ, func(t *testing.T) {
			base := "https://dev.azure.com/" + url.PathEscape(org) + "/" + url.PathEscape(name) + "/_apis/wit/workitems/"
			var made struct {
				ID    int `json:"id"`
				Links struct {
					HTML struct{ Href string } `json:"html"`
				} `json:"_links"`
			}
			liveJSON(t, http.MethodPost, base+"$"+url.PathEscape(typ)+"?api-version=7.1", header,
				[]map[string]any{{"op": "add", "path": "/fields/System.Title", "value": liveTitle()}}, &made)
			t.Cleanup(func() {
				liveJSON(t, http.MethodDelete, fmt.Sprintf("%s%d?api-version=7.1", base, made.ID), header, nil, nil)
			})
			t.Logf("%s %s", typ, made.Links.HTML.Href)
			// Priority 2 is the default, which the read takes as none; the tags say medium.
			var reads map[string]string
			if typ == withPriority {
				reads = map[string]string{"medium": ""}
			}
			livePlanWalk(t, c, wire.LineageNode{Kind: "issue", Ref: fmt.Sprintf("azureboards:issue:%s#%d", strings.ToLower(project), made.ID)}, reads)
		})
	}
}
