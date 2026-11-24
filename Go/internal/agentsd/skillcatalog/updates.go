package skillcatalog

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Updates is whether a skill's source has moved on since it was added (SkillUpdates). Per
// source repository, at most once an hour: its HEAD from git's ref list, which costs
// nothing; and only when HEAD has moved past what was recorded, the tree at the new HEAD,
// one API request. A skill whose folder is the same at the new HEAD is current, and its
// recorded commit moves forward so the next check needs no request at all.
type Updates struct {
	GitHub  GitHub
	Sidecar string
	Cache   *UpdateCache
}

// UpdateCache is each source's last answer: its HEAD, each folder's tree SHA there and,
// once a persona from it needed them, each file's blob SHA.
type UpdateCache struct {
	mu      sync.Mutex
	answers map[string]updateAnswer
}

type updateAnswer struct {
	at      time.Time
	head    string
	folders map[string]string
	blobs   map[string]string
}

const updateInterval = time.Hour

// Check is the update state of each managed skill at place, by folder name.
func (u Updates) Check(ctx context.Context, managed map[string]wire.ManagedSkill, place Place, now time.Time) map[string]wire.UpdateState {
	out := map[string]wire.UpdateState{}
	side := LoadSidecar(u.Sidecar)
	moved := map[string]string{}
	bySource := map[string][]wire.ManagedSkill{}
	for _, m := range managed {
		bySource[strings.ToLower(m.Source)] = append(bySource[strings.ToLower(m.Source)], m)
	}
	unknown := wire.UpdateState{Unknown: &struct{}{}}
	for source, skills := range bySource {
		owner, repo, ok := GitHubSource(skills[0].Source)
		if !ok {
			continue
		}
		answer, ok := u.skillAnswer(ctx, source, owner, repo, skills, now)
		if !ok {
			for _, s := range skills {
				out[s.Name] = unknown
			}
			continue
		}
		for _, s := range skills {
			out[s.Name] = skillUpdateState(s, answer, place, side, moved)
		}
	}
	// The file may have changed while the sources answered: only the records still there move on.
	if err := UpdateSidecar(u.Sidecar, func(m map[string]SidecarRecord) {
		for key, commit := range moved {
			if r, ok := m[key]; ok {
				r.Commit = commit
				m[key] = r
			}
		}
	}); err != nil {
		log.Printf("skills: could not write %s: %v", filepath.Base(u.Sidecar), err)
	}
	return out
}

// cached is source's last answer, if there is one.
func (c *UpdateCache) cached(source string) (updateAnswer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.answers == nil {
		c.answers = map[string]updateAnswer{}
	}
	answer, had := c.answers[source]
	return answer, had
}

// remember keeps answer as source's last answer.
func (c *UpdateCache) remember(source string, answer updateAnswer) {
	c.mu.Lock()
	c.answers[source] = answer
	c.mu.Unlock()
}

// skillAnswer is source's answer for skills: the cached one if it is under an hour old,
// else its HEAD now and, when a skill is not at that commit, each folder's tree SHA there.
// It is false when the HEAD cannot be read.
func (u Updates) skillAnswer(ctx context.Context, source, owner, repo string, skills []wire.ManagedSkill, now time.Time) (updateAnswer, bool) {
	answer, had := u.Cache.cached(source)
	if had && now.Sub(answer.at) <= updateInterval {
		return answer, true
	}
	head, err := u.GitHub.Head(ctx, owner, repo)
	if err != nil {
		return updateAnswer{}, false
	}
	folders := map[string]string{}
	if skillsMoved(skills, head.Commit) {
		folders = u.treeFolders(ctx, owner, repo, head.Commit)
	}
	answer = updateAnswer{at: now, head: head.Commit, folders: folders}
	u.Cache.remember(source, answer)
	return answer, true
}

// skillsMoved is whether a skill is not at commit.
func skillsMoved(skills []wire.ManagedSkill, commit string) bool {
	for _, s := range skills {
		if s.Commit == nil || *s.Commit != commit {
			return true
		}
	}
	return false
}

// treeFolders is each folder's tree SHA at commit, by path; empty when the tree cannot be read.
func (u Updates) treeFolders(ctx context.Context, owner, repo, commit string) map[string]string {
	folders := map[string]string{}
	tree, err := u.GitHub.Tree(ctx, owner, repo, commit)
	if err != nil {
		return folders
	}
	for _, e := range tree {
		if e.Type == "tree" {
			folders[e.Path] = e.SHA
		}
	}
	return folders
}

// skillUpdateState is s's update state against answer. A skill whose folder has the same
// tree SHA at the new HEAD is current, and moved has that commit for its sidecar record.
func skillUpdateState(s wire.ManagedSkill, answer updateAnswer, place Place, side Sidecar, moved map[string]string) wire.UpdateState {
	key := SidecarKey(place.Destination, s.Name)
	if s.Commit != nil && *s.Commit == answer.head {
		return wire.UpdateState{Current: &struct{}{}}
	}
	folder := ""
	if s.SkillPath != nil {
		folder = strings.Trim(strings.TrimSuffix(*s.SkillPath, "SKILL.md"), "/")
	}
	// What the folder was when it was added: the lock's tree SHA for a personal
	// skill, the sidecar's for a folder one.
	var was *string
	if place.LockKind == Personal {
		was = s.RecordedHash
	} else if r, ok := side.Skills[key]; ok {
		was = &r.TreeSHA
	}
	sha, ok := answer.folders[folder]
	switch {
	case !ok || was == nil:
		return wire.UpdateState{Unknown: &struct{}{}}
	case sha == *was:
		moved[key] = answer.head
		return wire.UpdateState{Current: &struct{}{}}
	default:
		return wire.UpdateState{Available: &wire.UpdateStateAvailable{Commit: answer.head}}
	}
}
