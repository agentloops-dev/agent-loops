package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

type completionKind int

const (
	completionSlash completionKind = iota
	completionFile
)

type completionQuery struct {
	kind   completionKind
	marker byte
	start  int
	term   string
}

type completion struct {
	match  string
	insert string
	label  string
	detail string
	path   string
}

type pendingAttachment struct {
	text       string
	attachment wire.Attachment
}

func completionAt(e lineEditor) (completionQuery, bool) {
	before := e.text[:e.cursor]
	var best completionQuery
	found := false
	for _, want := range []struct {
		marker byte
		kind   completionKind
	}{{'/', completionSlash}, {'@', completionFile}} {
		at := strings.LastIndexByte(before, want.marker)
		if at < 0 {
			continue
		}
		if at > 0 {
			r, _ := utf8.DecodeLastRuneInString(before[:at])
			if !unicode.IsSpace(r) {
				continue
			}
		}
		term := before[at+1:]
		if strings.ContainsAny(term, " \t\r\n") {
			continue
		}
		if found && at < best.start {
			continue
		}
		best = completionQuery{kind: want.kind, marker: want.marker, start: at, term: term}
		found = true
	}
	return best, found
}

func completionKey(q completionQuery) string {
	return fmt.Sprintf("%c:%d:%s", q.marker, q.start, q.term)
}

func editorCompletionKey(e lineEditor) string {
	q, ok := completionAt(e)
	if !ok {
		return ""
	}
	return completionKey(q)
}

func (e *lineEditor) replace(start, end int, text string) {
	e.text = e.text[:start] + text + e.text[end:]
	e.cursor = start + len(text)
}

func commonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0]
	for _, v := range values[1:] {
		i := 0
		for i < len(prefix) && i < len(v) && prefix[i] == v[i] {
			i++
		}
		if i < len(prefix) && !utf8.RuneStart(prefix[i]) {
			for i > 0 && !utf8.RuneStart(prefix[i]) {
				i--
			}
		}
		prefix = prefix[:i]
	}
	return prefix
}

func commandDescription(c wire.SlashCommand) string {
	if c.Description != nil && *c.Description != "" {
		return *c.Description
	}
	if c.InputHint != nil {
		return *c.InputHint
	}
	return ""
}

func sortCompletions(matches []completion, term string) {
	needle := strings.ToLower(term)
	sort.SliceStable(matches, func(i, j int) bool {
		return completionRank(matches[i], needle) < completionRank(matches[j], needle)
	})
}

func completionRank(c completion, needle string) int {
	if needle == "" {
		return 0
	}
	name := strings.ToLower(filepath.Base(c.match))
	if strings.HasPrefix(name, needle) {
		return 0
	}
	if strings.Contains(name, needle) {
		return 1
	}
	return 2
}

func (f *live) slashCompletions(term string) []completion {
	needle := strings.ToLower(term)
	seen := map[string]bool{}
	var matches []completion
	add := func(name, detail string) {
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			return
		}
		lower := strings.ToLower(name)
		if seen[lower] {
			return
		}
		seen[lower] = true
		if !strings.Contains(lower, needle) {
			return
		}
		matches = append(matches, completion{match: name, insert: name, label: "/" + name, detail: detail})
	}
	for _, c := range cliCommands {
		add(c.name, c.about)
	}
	for _, c := range f.commands {
		add(c.Name, commandDescription(c))
	}
	sortCompletions(matches, term)
	return matches
}

func (f *live) fileCompletions(ctx context.Context, term string) ([]completion, error) {
	if f.id == "" {
		return localFileCompletions(f.p, term), nil
	}
	raw, err := f.c.Call(ctx, f.host(), wire.CallFilesMention.Name, wire.FileMentionRequest{AgentID: f.id, Term: term})
	if err != nil {
		return nil, err
	}
	var files []wire.FileMentionDTO
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, err
	}
	matches := make([]completion, 0, len(files))
	for _, file := range files {
		rel := filepath.ToSlash(file.RelativePath)
		if rel == "" {
			rel = filepath.ToSlash(file.Path)
		}
		matches = append(matches, completion{match: rel, insert: filepath.Base(file.Path), label: rel, path: file.Path})
	}
	sortCompletions(matches, term)
	return matches, nil
}

func localFileCompletions(p place, term string) []completion {
	root := p.dir
	if root == "" {
		root = store.FolderPath(p.folder)
	}
	if root == "" {
		return nil
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	needle := strings.ToLower(term)
	var matches []completion
	seen := 0
	_ = filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		seen++
		if err != nil {
			return nil
		}
		if len(matches) >= 50 || seen > 20000 {
			return filepath.SkipAll
		}
		name := e.Name()
		if e.IsDir() {
			if name != filepath.Base(root) && (strings.HasPrefix(name, ".") || skippedFolder(name)) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.Contains(strings.ToLower(rel), needle) {
			matches = append(matches, completion{match: rel, insert: name, label: rel, path: path})
		}
		return nil
	})
	sortCompletions(matches, term)
	return matches
}

func skippedFolder(name string) bool {
	switch name {
	case ".git", "node_modules", ".build", "build", "DerivedData", "target", "dist":
		return true
	}
	return false
}

func fileAttachment(path string) (wire.Attachment, error) {
	info, err := os.Stat(path)
	if err != nil {
		return wire.Attachment{}, err
	}
	uri := string(store.PathURL(path))
	name := filepath.Base(path)
	size := float64(info.Size())
	count := info.Size()
	return wire.Attachment{
		ID:          store.NewUUID(),
		Block:       wire.ContentBlock{Type: "resource_link", URI: &uri, Name: &name, Size: &size},
		DisplayName: name,
		ByteCount:   &count,
	}, nil
}

func (f *live) clearCompletions() {
	f.sc.completions = nil
	f.sc.completionFor = ""
	f.sc.completionShown = false
}

func (f *live) pruneAttachments() {
	if len(f.attachments) == 0 {
		return
	}
	kept := f.attachments[:0]
	for _, a := range f.attachments {
		if strings.Contains(f.sc.input.text, a.text) {
			kept = append(kept, a)
		}
	}
	f.attachments = kept
}

func (f *live) takeAttachments() []wire.Attachment {
	out := make([]wire.Attachment, 0, len(f.attachments))
	for _, a := range f.attachments {
		out = append(out, a.attachment)
	}
	f.attachments = nil
	return out
}

func (f *live) complete(ctx context.Context) error {
	q, ok := completionAt(f.sc.input)
	if !ok {
		f.clearCompletions()
		f.sc.input.insert("    ")
		return nil
	}
	if f.sc.completionFor == completionKey(q) {
		f.sc.completionShown = true
		return nil
	}
	var matches []completion
	if q.kind == completionSlash {
		matches = f.slashCompletions(q.term)
	} else {
		var err error
		matches, err = f.fileCompletions(ctx, q.term)
		if err != nil {
			f.print(indent + f.t.st.paint(roleFailure, "Could not list files for @ here."))
			return nil
		}
	}
	if len(matches) == 0 {
		f.clearCompletions()
		return nil
	}
	if len(matches) == 1 {
		c := matches[0]
		insert := c.insert
		if q.kind == completionFile {
			if c.path == "" {
				insert = c.match
			} else if a, err := fileAttachment(c.path); err == nil {
				f.attachments = append(f.attachments, pendingAttachment{text: insert + " ", attachment: a})
			} else {
				insert = c.match
			}
		}
		f.sc.input.replace(q.start, f.sc.input.cursor, string(q.marker)+insert+" ")
		f.clearCompletions()
		return nil
	}
	values := make([]string, 0, len(matches))
	for _, c := range matches {
		values = append(values, c.match)
	}
	f.sc.input.replace(q.start, f.sc.input.cursor, string(q.marker)+commonPrefix(values))
	f.sc.completions = matches
	f.sc.completionFor = editorCompletionKey(f.sc.input)
	f.sc.completionShown = false
	return nil
}
