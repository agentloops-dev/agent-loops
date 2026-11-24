package agentsd

import (
	"cmp"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// The daily summaries that builds before #5853 saved, ~/.agents/summaries/ (#6714): no build
// writes one now, so the hourly sweep lets go of what is left, and of their marks with the
// last of them. Any other file there is the person's own.
var oldSummaries = store.Spec{Name: "summaries", Kind: "document", Scope: "person",
	Bounds: store.Bounds{MaxRecordBytes: assessReportMaxBytes, MaxCount: 3, MaxAge: store.MaxAge, Action: store.EvictOldest}}

const (
	oldSummaryPage     = 50
	oldSummaryMarksMax = 1 << 20
)

// oldSummaryName is the name those builds gave a page: yyyy-MM-dd-HHmm, with .md.
var oldSummaryName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{4}\.md$`)

// summaryFiles is the document interface to the saved pages. A page has a page's name and
// title and is under the largest size; one that git tracks is the repository's history.
type summaryFiles struct{ reportFiles }

func (s summaryFiles) List(cursor string, limit int) ([]reportDocument, string, error) {
	entries, err := os.ReadDir(s.dir) // sorted by name
	if err != nil {
		return nil, "", err
	}
	var out []reportDocument
	var tracked map[string]bool
	for _, e := range entries {
		if e.Name() <= cursor || !e.Type().IsRegular() || !oldSummaryName.MatchString(e.Name()) {
			continue
		}
		if tracked == nil {
			if tracked, err = trackedFiles(s.dir); err != nil {
				return nil, "", err
			}
			if tracked == nil {
				tracked = map[string]bool{}
			}
		}
		info, err := e.Info()
		if err != nil || tracked[e.Name()] || info.Size() > int64(oldSummaries.Bounds.MaxRecordBytes) {
			continue
		}
		text, err := s.Get(e.Name())
		if err != nil || !strings.HasPrefix(string(text), "# Daily summary") {
			continue
		}
		if len(out) == limit {
			return out, out[limit-1].id, nil
		}
		out = append(out, reportDocument{id: e.Name(), size: info.Size(), at: info.ModTime()})
	}
	return out, "", nil
}

// sweepOldSummaries holds the saved pages to their bounds, in the hourly sweep.
func (d *Daemon) sweepOldSummaries(now time.Time) {
	home, rel := d.personalHome(), filepath.Join(".agents", "summaries")
	if home == "" || !ownFolders(home, rel) {
		return
	}
	dir := filepath.Join(home, rel)
	var in reportDocuments = summaryFiles{reportFiles{dir}}
	var pages []reportDocument
	for cursor := ""; ; {
		some, next, err := in.List(cursor, oldSummaryPage)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				log.Printf("retention: could not list the old daily summaries: %v", err)
			}
			return
		}
		pages = append(pages, some...)
		if next == "" {
			break
		}
		cursor = next
	}
	slices.SortFunc(pages, func(a, b reportDocument) int { // newest first
		return cmp.Or(b.at.Compare(a.at), cmp.Compare(b.id, a.id))
	})
	left := len(pages)
	for i, p := range pages {
		if i < oldSummaries.Bounds.MaxCount && now.Sub(p.at) < oldSummaries.Bounds.MaxAge {
			continue
		}
		if err := in.Delete(p.id); err != nil {
			log.Printf("retention: could not delete an old daily summary: %v", err)
			continue
		}
		left--
	}
	if left > 0 {
		return
	}
	if len(pages) > 0 {
		log.Printf("retention: let go of the last of the old daily summaries")
	}
	if marks := filepath.Join(dir, "pages.json"); oldSummaryMarks(marks) {
		if err := os.Remove(marks); err != nil {
			log.Printf("retention: could not delete the old daily summaries' marks: %v", err)
			return
		}
	}
	_ = os.Remove(dir) // only when nothing else is in it
}

// oldSummaryMarks says whether a file is the marks that those builds kept beside the pages:
// for each page's name, its writer and whether it is unread.
func oldSummaryMarks(path string) bool {
	fi, err := os.Lstat(path) //nolint:gosec // G703: a fixed name in the daemon's own home
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > oldSummaryMarksMax {
		return false
	}
	b, err := os.ReadFile(path) //nolint:gosec // G703: as above
	var marks map[string]map[string]any
	if err != nil || diskjson.Unmarshal(b, &marks) != nil || marks == nil {
		return false
	}
	for name, mark := range marks {
		if !oldSummaryName.MatchString(name + ".md") {
			return false
		}
		for key := range mark {
			if key != "writer" && key != "unread" {
				return false
			}
		}
	}
	return true
}
