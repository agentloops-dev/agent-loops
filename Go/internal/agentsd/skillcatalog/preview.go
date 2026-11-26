package skillcatalog

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Staged is a skill fetched at one commit into a folder the daemon owns and nowhere an
// agent looks (StagedSkill). What Add moves into place is exactly what was fetched here and
// shown; nothing is fetched again.
type Staged struct {
	Preview wire.SkillPreview
	// Folder is <cache>/catalog-staging/<previewID>/<name>.
	Folder string
	// LockKey is the skill's own name, the key the CLI records it under.
	LockKey   string
	CreatedAt time.Time
}

// Staging is the previews waiting to be added: at most eight, each swept after half an
// hour, oldest first.
type Staging struct {
	Root  string
	mu    sync.Mutex
	items map[wire.UUID]Staged
}

const (
	stagingLifetime = 30 * time.Minute
	stagingMost     = 8
)

func (s *Staging) FolderFor(id wire.UUID) string { return filepath.Join(s.Root, string(id)) }

func (s *Staging) Put(st Staged) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[wire.UUID]Staged{}
	}
	s.items[st.Preview.PreviewID] = st
	s.sweep(time.Now())
}

func (s *Staging) Get(id wire.UUID) (Staged, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(time.Now())
	st, ok := s.items[id]
	return st, ok
}

// Take is the preview for adding: used up whether the add succeeds or not.
func (s *Staging) Take(id wire.UUID) (Staged, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(time.Now())
	st, ok := s.items[id]
	delete(s.items, id)
	return st, ok
}

// Discard removes what is left of a taken preview.
func (s *Staging) Discard(id wire.UUID) { os.RemoveAll(s.FolderFor(id)) }

func (s *Staging) sweep(now time.Time) {
	for id, st := range s.items {
		if now.Sub(st.CreatedAt) > stagingLifetime {
			delete(s.items, id)
			s.Discard(id)
		}
	}
	for len(s.items) > stagingMost {
		var oldest wire.UUID
		var at time.Time
		for id, st := range s.items {
			if at.IsZero() || st.CreatedAt.Before(at) {
				oldest, at = id, st.CreatedAt
			}
		}
		delete(s.items, oldest)
		s.Discard(oldest)
	}
}

// Previewer stages a skill (SkillPreviewer).
type Previewer struct {
	// Catalog is skills.sh, whose copy saves fetching the files; nil for a skill from
	// another marketplace, whose repository skills.sh need not hear about.
	Catalog *SkillsSh
	GitHub  GitHub
}

const (
	maxBytes = 10 << 20
	maxFiles = 500
)

// Stage fetches result at its repository's current commit into staging. The destination
// state is left free: the caller works it out for the destination asked about.
func (p Previewer) Stage(ctx context.Context, result wire.CatalogResult, staging string, id wire.UUID, now time.Time) (Staged, error) {
	owner, repo := result.Owner, result.Repo
	if _, _, ok := GitHubSource(owner + "/" + repo); !ok || !plainName(result.SkillID) {
		return Staged{}, CannotAdd([]wire.PreviewProblem{{NotFoundInRepo: &struct{}{}}})
	}
	head, err := p.GitHub.Head(ctx, owner, repo)
	if err != nil {
		return Staged{}, err
	}
	work := filepath.Join(staging, ".work")
	defer os.RemoveAll(work)

	unpacked, tree, err := p.stageTree(ctx, owner, repo, head.Commit, work)
	if err != nil {
		return Staged{}, err
	}

	skillMD := p.findSkill(ctx, result, tree, head, unpacked)
	if skillMD == "" {
		return empty(result, id, head, []wire.PreviewProblem{{NotFoundInRepo: &struct{}{}}}, staging, now, "", nil), nil
	}
	folderPath := strings.Trim(strings.TrimSuffix(skillMD, "SKILL.md"), "/")
	wanted, skipped, declared := wantedFiles(tree, folderPath)
	if len(wanted) > maxFiles || declared > maxBytes {
		return empty(result, id, head, []wire.PreviewProblem{{TooLarge: &wire.PreviewProblemTooLarge{Bytes: declared, Files: int64(len(wanted))}}},
			staging, now, "", nil), nil
	}

	files, inMemory, total, err := p.fetchFiles(ctx, result, head, unpacked, wanted)
	if err != nil {
		return Staged{}, err
	}
	if total > maxBytes {
		return empty(result, id, head, []wire.PreviewProblem{{TooLarge: &wire.PreviewProblemTooLarge{Bytes: total, Files: int64(len(files))}}},
			staging, now, "", nil), nil
	}
	markdown := skillMarkdown(inMemory)
	front := ParseSkillFile(markdown)
	if front.Name == "" || front.Description == "" {
		return empty(result, id, head, []wire.PreviewProblem{{NoSkillFile: &struct{}{}}}, staging, now, markdown, files), nil
	}
	name := FolderName(front.Name)
	folder := filepath.Join(staging, name)
	if err := writeStaged(folder, inMemory, wanted); err != nil {
		return Staged{}, err
	}
	problems := []wire.PreviewProblem{}
	if len(skipped) > 0 {
		problems = append(problems, wire.PreviewProblem{Skipped: &wire.PreviewProblemSkipped{Value: skipped}})
	}
	treeSHA, err := stagedTreeSHA(tree, folderPath, folder)
	if err != nil {
		return Staged{}, err
	}
	preview := wire.SkillPreview{PreviewID: id, Result: result, Name: name, Description: front.Description, SkillPath: skillMD,
		Commit: head.Commit, TreeSHA: treeSHA, ComputedHash: ComputedHashOf(inMemory), Files: files, SkillMarkdown: markdown,
		TotalBytes: total, Problems: problems, DestinationState: wire.DestinationState{Free: &struct{}{}}}
	if at := p.GitHub.CommitDate(ctx, owner, repo, head.Commit, folderPath); at != nil {
		d := wire.DateOf(*at)
		preview.CommittedAt = &d
	}
	return Staged{Preview: preview, Folder: folder, LockKey: front.Name, CreatedAt: now}, nil
}

// wantedFile is one file of the skill's folder: its path in the folder and its tree entry.
type wantedFile struct {
	rel   string
	entry TreeEntry
}

// stageTree is the repository's tree at commit: GitHub's tree, or, when that is rate
// limited, the tarball's, unpacked into work (the folder it is unpacked in comes first).
func (p Previewer) stageTree(ctx context.Context, owner, repo, commit, work string) (string, []TreeEntry, error) {
	tree, err := p.GitHub.Tree(ctx, owner, repo, commit)
	unpacked := ""
	if errors.Is(err, ErrRateLimited) {
		unpacked, tree, err = p.GitHub.TarballTree(ctx, owner, repo, commit, work)
		var ce *Error
		if errors.As(err, &ce) && ce.Why.Unreachable != nil {
			return "", nil, RateLimited(nil)
		}
	}
	return unpacked, tree, err
}

// wantedFiles is the blobs under folderPath that can be staged, the paths skipped (links
// and unsafe paths) and the bytes the tree says the wanted ones have.
func wantedFiles(tree []TreeEntry, folderPath string) (wanted []wantedFile, skipped []string, declared int64) {
	prefix := ""
	if folderPath != "" {
		prefix = folderPath + "/"
	}
	for _, e := range tree {
		if e.Type != "blob" || !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		rel := e.Path[len(prefix):]
		if e.Mode == "120000" || !IsSafe(rel) {
			skipped = append(skipped, rel)
			continue
		}
		wanted = append(wanted, wantedFile{rel, e})
		if e.Size != nil {
			declared += *e.Size
		}
	}
	return wanted, skipped, declared
}

// fetchFiles is the bytes of each wanted file, sorted by path: skills.sh's copy where each
// file's blob matches the commit's, GitHub's own otherwise, and each file from GitHub
// checked too. It sorts wanted in place, so wanted[i] is the entry of the i-th file.
func (p Previewer) fetchFiles(ctx context.Context, result wire.CatalogResult, head Head, unpacked string, wanted []wantedFile) (files []wire.PreviewFile, inMemory []File, total int64, err error) {
	var copies map[string][]byte
	if unpacked == "" && p.Catalog != nil {
		copies = p.Catalog.Snapshot(ctx, result.Owner, result.Repo, result.SkillID)
	}
	sort.Slice(wanted, func(i, j int) bool { return wanted[i].rel < wanted[j].rel })
	files = []wire.PreviewFile{}
	for _, w := range wanted {
		data, via, err := p.fileBytes(ctx, result, head, unpacked, copies, w)
		if err != nil {
			return nil, nil, 0, err
		}
		total += int64(len(data))
		runnable := w.entry.Mode == "100755" || strings.HasPrefix(w.rel, "scripts/") || bytes.HasPrefix(data, []byte("#!"))
		files = append(files, wire.PreviewFile{Path: w.rel, Bytes: int64(len(data)), Runnable: runnable, Via: via})
		inMemory = append(inMemory, File{w.rel, data})
	}
	return files, inMemory, total, nil
}

// fileBytes is one wanted file's bytes and where they came from: the unpacked tarball,
// skills.sh's snapshot when its blob matches, else GitHub, whose bytes must match the blob.
func (p Previewer) fileBytes(ctx context.Context, result wire.CatalogResult, head Head, unpacked string, copies map[string][]byte, w wantedFile) ([]byte, string, error) {
	if unpacked != "" {
		data, err := os.ReadFile(filepath.Join(unpacked, w.entry.Path))
		if err != nil {
			return nil, "", Failed("could not read the unpacked " + w.rel)
		}
		return data, "tarball", nil
	}
	if c, ok := copies[w.rel]; ok && BlobSHA(c) == w.entry.SHA {
		return c, "snapshot", nil
	}
	data, err := p.GitHub.Raw(ctx, result.Owner, result.Repo, head.Commit, w.entry.Path, w.entry.SHA, head.SignedIn)
	if err != nil {
		return nil, "", err
	}
	via := "raw"
	if head.SignedIn {
		via = "gh"
	}
	if BlobSHA(data) != w.entry.SHA {
		if head.SignedIn {
			return nil, "", Unreachable("github.com")
		}
		return nil, "", Unreachable(host(p.GitHub.Endpoints.Raw))
	}
	return data, via, nil
}

// skillMarkdown is the text of the folder's SKILL.md, or "" when it has none.
func skillMarkdown(inMemory []File) string {
	markdown := ""
	for _, f := range inMemory {
		if f.Path == "SKILL.md" {
			markdown = string(f.Data)
		}
	}
	return markdown
}

// writeStaged writes the files into a new folder, each runnable one (by its tree mode) 0755.
func writeStaged(folder string, inMemory []File, wanted []wantedFile) error {
	os.RemoveAll(folder)
	for i, f := range inMemory {
		path := filepath.Join(folder, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Failed("could not stage " + f.Path)
		}
		mode := os.FileMode(0o644)
		if wanted[i].entry.Mode == "100755" {
			mode = 0o755
		}
		// store-bounds: not a store: a file in a temporary preview folder.
		if err := os.WriteFile(path, f.Data, mode); err != nil {
			return Failed("could not stage " + f.Path)
		}
		if err := os.Chmod(path, mode); err != nil {
			return Failed("could not stage " + f.Path)
		}
	}
	return nil
}

// stagedTreeSHA is the tree SHA to record for the folder. The CLI records GitHub's own tree
// SHA for the folder; the staged copy's is the same unless something was skipped, and is
// the fallback when the tree came from a tarball.
func stagedTreeSHA(tree []TreeEntry, folderPath, folder string) (string, error) {
	treeSHA := ""
	for _, e := range tree {
		if e.Type == "tree" && e.Path == folderPath && e.SHA != "" {
			treeSHA = e.SHA
		}
	}
	if treeSHA != "" {
		return treeSHA, nil
	}
	treeSHA, err := TreeSHA(folder)
	if err != nil {
		return "", Failed("could not hash the staged folder")
	}
	return treeSHA, nil
}

// findSkill is which SKILL.md is the skill: the one the marketplace named, if it is still
// there; else a folder named like the id, then one whose own name makes the same slug (the
// CLI's rule), then a lone one at the top.
func (p Previewer) findSkill(ctx context.Context, result wire.CatalogResult, tree []TreeEntry, head Head, unpacked string) string {
	var entries []TreeEntry
	for _, e := range tree {
		if e.Type == "blob" && (e.Path == "SKILL.md" || strings.HasSuffix(e.Path, "/SKILL.md")) {
			entries = append(entries, e)
		}
	}
	if result.SkillPath != nil {
		for _, e := range entries {
			if e.Path == *result.SkillPath {
				return e.Path
			}
		}
	}
	want := Slug(result.SkillID)
	for _, e := range entries {
		parts := strings.Split(e.Path, "/")
		if len(parts) >= 2 && Slug(parts[len(parts)-2]) == want {
			return e.Path
		}
	}
	for i, e := range entries {
		if i == 20 {
			break
		}
		var data []byte
		if unpacked != "" {
			data, _ = os.ReadFile(filepath.Join(unpacked, e.Path))
		} else {
			data, _ = p.GitHub.Raw(ctx, result.Owner, result.Repo, head.Commit, e.Path, e.SHA, head.SignedIn)
		}
		if n := ParseSkillFile(string(data)).Name; n != "" && Slug(n) == want {
			return e.Path
		}
	}
	if len(entries) == 1 && entries[0].Path == "SKILL.md" {
		return "SKILL.md"
	}
	return ""
}

// IsSafe is a relative path that stays inside the skill's folder.
func IsSafe(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func empty(result wire.CatalogResult, id wire.UUID, head Head, problems []wire.PreviewProblem, staging string, now time.Time,
	markdown string, files []wire.PreviewFile) Staged {
	if files == nil {
		files = []wire.PreviewFile{}
	}
	total := int64(0)
	for _, f := range files {
		total += f.Bytes
	}
	preview := wire.SkillPreview{PreviewID: id, Result: result, Name: FolderName(result.SkillID), Commit: head.Commit,
		Files: files, SkillMarkdown: markdown, TotalBytes: total, Problems: problems,
		DestinationState: wire.DestinationState{Free: &struct{}{}}}
	return Staged{Preview: preview, Folder: filepath.Join(staging, "none"), LockKey: result.SkillID, CreatedAt: now}
}

// CanAdd is whether nothing but skipped files was found.
func CanAdd(p wire.SkillPreview) bool {
	for _, x := range p.Problems {
		if x.Skipped == nil {
			return false
		}
	}
	return true
}
