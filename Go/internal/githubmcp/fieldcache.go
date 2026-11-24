package githubmcp

import (
	"fmt"
	"sync"
	"time"
)

// fieldsFor is how long a repo's Priority issue field is kept (#6644): a repo's issue fields
// seldom change, and a write that fails asks again.
const fieldsFor = 10 * time.Minute

// issueField is a repo's single-select issue field, with its options.
type issueField struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Options []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"options"`
}

// fieldCache keeps each repo's Priority issue field (or that it has none) for fieldsFor, and
// the ids of issues written there, which never change (#6644). At most pullsCount of each;
// when full it starts again.
type fieldCache struct {
	mu     sync.Mutex
	now    func() time.Time
	repos  map[string]keptField
	issues map[string]string
}

type keptField struct {
	field *issueField
	at    time.Time
}

func (c *fieldCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// field is repo's kept Priority field; known is false when none is kept or it is too old.
func (c *fieldCache) field(repo string) (f *issueField, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, ok := c.repos[repo]
	if !ok || c.clock().Sub(k.at) >= fieldsFor {
		return nil, false
	}
	return k.field, true
}

func (c *fieldCache) keep(repo string, f *issueField) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.repos == nil || len(c.repos) >= pullsCount {
		c.repos = map[string]keptField{}
	}
	c.repos[repo] = keptField{field: f, at: c.clock()}
}

// forget drops repo's field, after a write there failed.
func (c *fieldCache) forget(repo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.repos, repo)
}

func (c *fieldCache) issue(repo string, number int) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.issues[fmt.Sprintf("%s#%d", repo, number)]
	return id, ok
}

func (c *fieldCache) keepIssue(repo string, number int, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.issues == nil || len(c.issues) >= pullsCount {
		c.issues = map[string]string{}
	}
	c.issues[fmt.Sprintf("%s#%d", repo, number)] = id
}
