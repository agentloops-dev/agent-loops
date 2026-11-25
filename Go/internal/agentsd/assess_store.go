package agentsd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The runtime assessments, <root>/assessments/ (#2292, #6724): a store of kind "document",
// reached through assessmentDocuments. It evicts: the oldest assessment first. A start or a
// score over the largest size is refused at the write. The scope file is the assessing
// agent's own write, so one over that size is cut at the next sweep.
const (
	assessmentMaxBytes = 256 << 10 // one file of an assessment
	assessmentsKept    = 50
	assessmentMaxAge   = store.MaxAge // since it started
	assessmentPage     = 200
)

// assessment is one document: what was known at its start, its score, and whether the file
// outside the folder was written. A part that is not there is nil.
type assessment struct {
	start        *assessStart
	score        *wire.RuntimeAssessmentScore
	scopeWritten bool
}

// assessmentInfo is what List says of one, without its content.
type assessmentInfo struct {
	id    string
	at    time.Time // when it started
	large bool      // a start or a score over the largest size
	cut   bool      // a scope file over the largest size
}

// errAssessmentStarted is a start for an assessment that has one.
var errAssessmentStarted = errors.New("the assessment has a start")

// assessmentDocuments is the document interface to the assessments (#6670).
type assessmentDocuments interface {
	// Get is an assessment; one that is not there has no parts.
	Get(id string) (assessment, error)
	// Put writes the parts that doc has. A start is written once: a second one fails with
	// errAssessmentStarted. A score replaces the one before it.
	Put(id string, doc assessment) error
	// Delete deletes an assessment; one that is not there is not an error.
	Delete(id string) error
	// Cut brings an assessment's scope file to the largest size, with scopeCutMark at its end.
	Cut(id string) error
	// List is the assessments after cursor, by id, and the cursor of the next page ("" at the end).
	List(cursor string, limit int) ([]assessmentInfo, string, error)
}

const (
	assessStartSuffix = ".start.json"
	assessScoreSuffix = ".json"
)

// assessmentFiles keeps an assessment as <id>.start.json, <id>.json and scope-<short id>.txt
// in one directory. A file there that belongs to no start (a score or a scope file left
// behind, a write that a crash cut off) is a document of its own, so it ages out too.
type assessmentFiles struct {
	dir string
	mu  *sync.Mutex
}

func (d *Daemon) assessmentsPath() string { return filepath.Join(d.Store.Loc.Root, "assessments") }

func (d *Daemon) assessments() assessmentDocuments {
	return assessmentFiles{dir: d.assessmentsPath(), mu: &d.assessMu}
}

// path is a file's path; a name that is not one file's has none.
func (f assessmentFiles) path(name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("%q is not an assessment's id", name)
	}
	return filepath.Join(f.dir, name), nil
}

// read decodes a file into v, and says if it is there.
func (f assessmentFiles) read(name string, v any) (bool, error) {
	path, err := f.path(name)
	if err != nil {
		return false, err
	}
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, assessmentMaxBytes+1))
	if err != nil {
		return false, err
	}
	if len(b) > assessmentMaxBytes {
		return false, fmt.Errorf("%s: over %d bytes: %w", name, assessmentMaxBytes, store.ErrLimit)
	}
	return true, diskjson.Unmarshal(b, v)
}

// scopeName is the name of a start's scope file, "" for none in this directory.
func (s assessStart) scopeName() string {
	name := filepath.Base(s.ScopePath)
	if !strings.HasPrefix(name, "scope-") || !strings.HasSuffix(name, ".txt") {
		return ""
	}
	return name
}

func (f assessmentFiles) Get(id string) (assessment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var doc assessment
	var start assessStart
	ok, startErr := f.read(id+assessStartSuffix, &start)
	if ok && startErr == nil {
		doc.start = &start
		if name := start.scopeName(); name != "" {
			_, err := os.Lstat(filepath.Join(f.dir, name))
			doc.scopeWritten = err == nil
		}
	}
	var score wire.RuntimeAssessmentScore
	ok, scoreErr := f.read(id+assessScoreSuffix, &score)
	if ok && scoreErr == nil {
		doc.score = &score
	}
	return doc, errors.Join(startErr, scoreErr)
}

func (f assessmentFiles) Put(id string, doc assessment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if doc.start != nil {
		path, err := f.path(id + assessStartSuffix)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); err == nil {
			return errAssessmentStarted
		}
		if err := f.write(path, doc.start); err != nil {
			return err
		}
	}
	if doc.score == nil {
		return nil
	}
	path, err := f.path(id + assessScoreSuffix)
	if err != nil {
		return err
	}
	return f.write(path, doc.score)
}

func (f assessmentFiles) write(path string, v any) error {
	b, err := diskjson.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > assessmentMaxBytes {
		return fmt.Errorf("%s: %d bytes, at most %d: %w", filepath.Base(path), len(b), assessmentMaxBytes, store.ErrLimit)
	}
	if err := os.MkdirAll(f.dir, 0o755); err != nil { //nolint:gosec // G301: the mode this folder always had; only its error is new
		return err
	}
	// store-bounds: kind=document; size=50 records of 3 files of 256KiB; age=90d; applied=hourly sweep, and when an assessment starts or is scored
	return store.WriteAtomically(path, b, 0o644)
}

func (f assessmentFiles) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := []string{id + assessScoreSuffix, id}
	var start assessStart
	// A start that cannot be read names no scope file: that one is then a document of its own.
	if ok, err := f.read(id+assessStartSuffix, &start); ok && err == nil && start.scopeName() != "" {
		names = append(names, start.scopeName())
	}
	// The start last, so that a delete cut off is done again.
	var failed error
	for _, name := range append(names, id+assessStartSuffix) {
		path, err := f.path(name)
		if err == nil {
			err = os.Remove(path)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

func (f assessmentFiles) Cut(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := id
	var start assessStart
	if ok, err := f.read(id+assessStartSuffix, &start); err != nil {
		return err
	} else if ok {
		name = start.scopeName()
	}
	path, err := f.path(name)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= assessmentMaxBytes {
		return err
	}
	if err := cutScopeFile(path); err != nil {
		return err
	}
	return os.Chtimes(path, info.ModTime(), info.ModTime()) // its age goes on
}

// scopeCutMark is the end of a scope file that was cut.
const scopeCutMark = "\n… cut: a scope file is at most 256 KiB\n"

// cutScopeFile cuts a file to the largest size, at a whole character, with the mark in it. It is
// cut where it is, with no second file.
func cutScopeFile(path string) (err error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	b := make([]byte, assessmentMaxBytes-len(scopeCutMark))
	if _, err := io.ReadFull(file, b); err != nil {
		return err
	}
	b = wholeUTF8(b)
	if err := file.Truncate(int64(len(b))); err != nil {
		return err
	}
	_, err = file.WriteAt([]byte(scopeCutMark), int64(len(b)))
	return err
}

func (f assessmentFiles) List(cursor string, limit int) ([]assessmentInfo, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := os.ReadDir(f.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	files := map[string]fs.FileInfo{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil { // else it went since
			files[e.Name()] = info
		}
	}
	var all []assessmentInfo
	for name, info := range files {
		id, ok := strings.CutSuffix(name, assessStartSuffix)
		if !ok || id == "" {
			continue
		}
		one := assessmentInfo{id: id, at: info.ModTime(), large: info.Size() > assessmentMaxBytes}
		if score, ok := files[id+assessScoreSuffix]; ok {
			one.large = one.large || score.Size() > assessmentMaxBytes
			delete(files, id+assessScoreSuffix)
		}
		var start assessStart
		if ok, err := f.read(name, &start); ok && err == nil {
			if scope, ok := files[start.scopeName()]; ok {
				one.cut = scope.Mode().IsRegular() && scope.Size() > assessmentMaxBytes
				delete(files, start.scopeName())
			}
		}
		delete(files, name)
		all = append(all, one)
	}
	for name, info := range files {
		large := info.Mode().IsRegular() && info.Size() > assessmentMaxBytes
		scope := strings.HasPrefix(name, "scope-")
		all = append(all, assessmentInfo{id: name, at: info.ModTime(), large: large && !scope, cut: large && scope})
	}
	slices.SortFunc(all, func(a, b assessmentInfo) int { return cmp.Compare(a.id, b.id) })
	all = slices.DeleteFunc(all, func(a assessmentInfo) bool { return a.id <= cursor })
	if len(all) > limit {
		return all[:limit], all[limit-1].id, nil
	}
	return all, "", nil
}

// listAssessments is every assessment, by id.
func listAssessments(in assessmentDocuments) ([]assessmentInfo, error) {
	var all []assessmentInfo
	for cursor := ""; ; {
		page, next, err := in.List(cursor, assessmentPage)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		cursor = next
	}
}

// sweepAssessments brings the assessments under their bounds: in the hourly sweep, when one
// starts, and when one is scored. The reports have bounds of their own (assess_reports.go),
// and are swept first: a start that goes can be the last word on where its report is.
func (d *Daemon) sweepAssessments(now time.Time) {
	sweepAssessments(d.assessments(), now)
}

func sweepAssessments(in assessmentDocuments, now time.Time) {
	all, err := listAssessments(in)
	if err != nil {
		log.Printf("retention: could not list runtime assessments: %v", err)
		return
	}
	slices.SortFunc(all, func(a, b assessmentInfo) int { // newest first
		return cmp.Or(b.at.Compare(a.at), cmp.Compare(b.id, a.id))
	})
	kept := 0
	for _, one := range all {
		if kept >= assessmentsKept || one.large || now.Sub(one.at) >= assessmentMaxAge {
			if err := in.Delete(one.id); err != nil {
				log.Printf("retention: could not delete a runtime assessment: %v", err)
			}
			continue
		}
		kept++
		if one.cut {
			if err := in.Cut(one.id); err != nil {
				log.Printf("retention: could not cut a runtime assessment's scope file to size: %v", err)
			}
		}
	}
}
