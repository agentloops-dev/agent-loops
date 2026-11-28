package store

import (
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Session parents (#7538, specs/7535-session-parents): a session's parent is one field on
// its own record, so a move is one write and its subtree goes with it. The children of a
// session are not stored: the store keeps them as an index, as it keeps inFolder.

// EffectiveParent is the session a's tree puts it under: parent.id when parent is set (none
// when it is set with no id, "at the top"), else its starter.
func EffectiveParent(a *wire.Agent) (wire.UUID, bool) {
	if a.Parent != nil {
		if a.Parent.ID == nil {
			return "", false
		}
		return upper(*a.Parent.ID), true
	}
	if a.StartedByAgent != nil {
		return upper(*a.StartedByAgent), true
	}
	return "", false
}

func upper(id wire.UUID) wire.UUID { return wire.UUID(strings.ToUpper(string(id))) }

// fileUnder files a under its effective parent. With mu held, after drop.
func (s *Store) fileUnder(a *wire.Agent) {
	p, ok := EffectiveParent(a)
	if !ok {
		return
	}
	if s.under == nil {
		s.under, s.parentOf = map[wire.UUID]map[wire.UUID]struct{}{}, map[wire.UUID]wire.UUID{}
	}
	ids := s.under[p]
	if ids == nil {
		ids = map[wire.UUID]struct{}{}
		s.under[p] = ids
	}
	ids[a.ID] = struct{}{}
	s.parentOf[a.ID] = p
}

// unfileUnder takes id out of its parent's children. With mu held.
func (s *Store) unfileUnder(id wire.UUID) {
	p, ok := s.parentOf[id]
	if !ok {
		return
	}
	delete(s.parentOf, id)
	if ids := s.under[p]; ids != nil {
		delete(ids, id)
		if len(ids) == 0 {
			delete(s.under, p)
		}
	}
}

// Children is the ids of the sessions whose effective parent is id, archived ones too.
func (s *Store) Children(id wire.UUID) []wire.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.under[upper(id)]
	out := make([]wire.UUID, 0, len(ids))
	for c := range ids {
		out = append(out, c)
	}
	return out
}

// LiveChildren is how many of id's children are not archived.
func (s *Store) LiveChildren(id wire.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.under[upper(id)] {
		if _, ok := s.live[c]; ok {
			n++
		}
	}
	return n
}

// ParentOf is the effective parent id is filed under, from the index.
func (s *Store) ParentOf(id wire.UUID) (wire.UUID, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.parentOf[upper(id)]
	return p, ok
}

// SubtreeHeight is how many levels hang below id (0 for a session with no children),
// counted no further than most levels.
func (s *Store) SubtreeHeight(id wire.UUID, most int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.height(upper(id), most, map[wire.UUID]bool{})
}

func (s *Store) height(id wire.UUID, most int, seen map[wire.UUID]bool) int {
	if most <= 0 || seen[id] {
		return 0
	}
	seen[id] = true
	h := 0
	for c := range s.under[id] {
		if ch := 1 + s.height(c, most-1, seen); ch > h {
			h = ch
		}
	}
	return h
}
