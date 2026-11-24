package agentsd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// The runtime assessment reports, <folder>/.agents/reviews/runtimes/ (#6712): a document
// store of kind "document", reached through reportDocuments. It evicts: the oldest report
// first. The agent writes its report with its own tools, so a report over the largest
// size is cut at the next sweep, not refused. A report that git tracks is the
// repository's history, not this store's, and is never changed or deleted.
const (
	assessReportMaxBytes = 1 << 20             // one report
	assessReportsKept    = 50                  // in all of this machine's folders
	assessReportMaxAge   = 90 * 24 * time.Hour // since its last write
	assessReportPage     = 200
)

// reportDocument is what List says of one report, without its content.
type reportDocument struct {
	id   string // its file name
	size int64
	at   time.Time
}

// errReportChanged is a conditional Put on a report that was written since it was listed.
var errReportChanged = errors.New("the report changed")

// reportDocuments is the document interface to one collection of reports (#6670).
type reportDocuments interface {
	// Get is a report's content, and no more than the largest size of it.
	Get(id string) ([]byte, error)
	// Put replaces the content of the report as it was listed, or fails with errReportChanged.
	Put(was reportDocument, content []byte) error
	// Delete deletes a report; one that is not there is not an error.
	Delete(id string) error
	// List is the reports after cursor, by id, and the cursor of the next page ("" at the end).
	List(cursor string, limit int) ([]reportDocument, string, error)
}

// reportFiles keeps the reports as the Markdown files of one directory.
type reportFiles struct{ dir string }

// reportName is a name freshReportPath gives. Any other file there is the person's own.
var reportName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*-\d{4}-\d{2}-\d{2}(-\d+)?\.md$`)

// reportsDir is a folder's directory of reports.
func reportsDir(folder string) string {
	return filepath.Join(folder, ".agents", "reviews", "runtimes")
}

// path is a report's file; an id that is not one file's name has none.
func (r reportFiles) path(id string) (string, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return "", fmt.Errorf("%q is not a report's name", id)
	}
	return filepath.Join(r.dir, id), nil
}

func (r reportFiles) Get(id string) ([]byte, error) {
	path, err := r.path(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, assessReportMaxBytes))
	return cutUTF8(b), err
}

func (r reportFiles) Put(was reportDocument, content []byte) error {
	if len(content) > assessReportMaxBytes {
		return fmt.Errorf("report %s is %d bytes; the limit is %d", was.id, len(content), assessReportMaxBytes)
	}
	path, err := r.path(was.id)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Size() != was.size || !info.ModTime().Equal(was.at) {
		return errReportChanged
	}
	// store-bounds: kind=document; size=50 records of 1MiB; age=90d; applied=hourly sweep, and when an assessment starts or is scored
	if err := store.WriteAtomically(path, content, info.Mode().Perm()); err != nil {
		return err
	}
	// Its age goes on from the agent's write.
	return os.Chtimes(path, was.at, was.at)
}

func (r reportFiles) Delete(id string) error {
	path, err := r.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (r reportFiles) List(cursor string, limit int) ([]reportDocument, string, error) {
	entries, err := os.ReadDir(r.dir) // sorted by name
	if err != nil {
		return nil, "", err
	}
	names := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() && reportName.MatchString(e.Name()) {
			names = append(names, e)
		}
	}
	if len(names) == 0 {
		return nil, "", nil
	}
	tracked, err := trackedFiles(r.dir)
	if err != nil {
		return nil, "", err
	}
	var out []reportDocument
	for _, e := range names {
		if e.Name() <= cursor || tracked[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // it went since
		}
		if len(out) == limit {
			return out, out[limit-1].id, nil
		}
		out = append(out, reportDocument{id: e.Name(), size: info.Size(), at: info.ModTime()})
	}
	return out, "", nil
}

// trackedFiles is the names in dir that git tracks: none outside a repository, and an error
// where there is one that git cannot read, so that no committed file looks like ours.
func trackedFiles(dir string) (map[string]bool, error) {
	out, err := gitRaw(dir, nil, "ls-files", "-z", "--", ".")
	if err != nil {
		for at := dir; ; at = filepath.Dir(at) {
			if _, statErr := os.Lstat(filepath.Join(at, ".git")); !errors.Is(statErr, os.ErrNotExist) {
				return nil, err
			}
			if at == filepath.Dir(at) {
				return nil, nil
			}
		}
	}
	tracked := map[string]bool{}
	for _, name := range strings.Split(string(out), "\x00") {
		tracked[name] = true
	}
	return tracked, nil
}

// cutUTF8 is b without a last rune that the cut at the largest size split.
func cutUTF8(b []byte) []byte {
	if len(b) < assessReportMaxBytes {
		return b
	}
	for n := len(b) - 1; n >= 0 && n >= len(b)-utf8.UTFMax; n-- {
		if utf8.RuneStart(b[n]) {
			if !utf8.FullRune(b[n:]) {
				return b[:n]
			}
			break
		}
	}
	return b
}

// reportDirs is every directory of reports this machine has: of each folder, archived too,
// and of each assessment it keeps the start of.
func (d *Daemon) reportDirs() []string {
	var dirs []string
	for _, f := range d.Store.Folders() {
		dirs = append(dirs, reportsDir(store.FolderPath(f.Folder)))
	}
	for _, f := range d.Store.AgentFolders() {
		dirs = append(dirs, reportsDir(f))
	}
	starts, _ := filepath.Glob(filepath.Join(d.assessmentsPath(), "*.start.json"))
	for _, p := range starts {
		var s assessStart
		if b, err := os.ReadFile(p); err != nil || diskjson.Unmarshal(b, &s) != nil || !filepath.IsAbs(s.ReportPath) {
			continue
		}
		if dir := filepath.Dir(s.ReportPath); strings.HasSuffix(dir, reportsDir("")) {
			dirs = append(dirs, dir)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		// One directory by two paths is one; one that is not there has no reports.
		path, err := filepath.EvalSymlinks(dir)
		if err == nil && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

// sweepAssessReports brings the reports under their bounds: in the hourly sweep, when an
// assessment starts, and when one is scored.
func (d *Daemon) sweepAssessReports(now time.Time) {
	var all []reportDocuments
	for _, dir := range d.reportDirs() {
		all = append(all, reportFiles{dir})
	}
	sweepReports(all, now)
}

func sweepReports(all []reportDocuments, now time.Time) {
	type kept struct {
		in  reportDocuments
		doc reportDocument
	}
	var reports []kept
	for _, in := range all {
		for cursor := ""; ; {
			page, next, err := in.List(cursor, assessReportPage)
			if err != nil {
				log.Printf("retention: could not list runtime assessment reports: %v", err)
				break
			}
			for _, doc := range page {
				reports = append(reports, kept{in, doc})
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	slices.SortFunc(reports, func(a, b kept) int { // newest first
		if c := b.doc.at.Compare(a.doc.at); c != 0 {
			return c
		}
		return cmp.Compare(b.doc.id, a.doc.id)
	})
	for i, r := range reports {
		if i >= assessReportsKept || now.Sub(r.doc.at) > assessReportMaxAge {
			if err := r.in.Delete(r.doc.id); err != nil {
				log.Printf("retention: could not delete a runtime assessment report: %v", err)
			}
			continue
		}
		if r.doc.size <= assessReportMaxBytes {
			continue
		}
		content, err := r.in.Get(r.doc.id)
		if err == nil {
			err = r.in.Put(r.doc, content)
		}
		if err != nil {
			log.Printf("retention: could not cut a runtime assessment report to size: %v", err)
		}
	}
}
