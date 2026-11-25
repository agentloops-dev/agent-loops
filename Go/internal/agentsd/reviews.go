package agentsd

import (
	"context"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The pull requests in the sidebar's For Review (#3330): each folder's open, non-draft pull
// requests with a review requested from the connected person (#5803), as a UI change waiting
// for them (#2811), until the person reviews it (GitHub then drops the request), it closes, or
// the person dismisses it. Read with forge/pulls (#3129), one GitHub search for every folder,
// on its own poll; said with each folder's pins
// (FolderPins.reviews, pins/changed). A dismissal is kept in <root>/review-dismissals.json,
// not in the folder's .agents/pins.json, which a repo may check in. It ages out 90 days after
// the pull request last changed (or was dismissed, when that is later), and For Review leaves
// out a pull request that has not changed for as long, so a dismissed one does not come back
// (#6728). A pull request the person opened is kept in <root>/review-opened.json, by the same
// limits (#6729), so every client draws its row read (#3464).

const (
	// reviewMarksMost bounds the marks in each of the two files: the oldest go first.
	reviewMarksMost = 2000
	// reviewKeep is how long a mark is kept, and how long a pull request that does not
	// change is listed: the same, or a pull request would come back, or be unread again, when its mark aged out.
	reviewKeep = store.MaxAge
)

// reviewEvery is the poll: forge/pulls keeps an answer a minute, so no faster than that.
var reviewEvery = 2 * time.Minute

// reviewFirst is how long after the start the first poll is.
var reviewFirst = 15 * time.Second

type reviewBook struct {
	reviewMu sync.Mutex
	// reviews is each folder's pull requests for review as last read, dismissed ones left out.
	reviews map[string][]wire.ReviewPull
	// dismissed is review-dismissals.json and opened is review-opened.json, by reviewMarkKey.
	dismissed, opened *store.JSONFile[reviewMark]
}

// reviewMark is a pull request dismissed from a folder's For Review, or opened there.
type reviewMark struct {
	Folder string   `json:"folder"`
	URL    wire.URL `json:"url"`
	// At is when it was marked, or when the pull request last changed since.
	At wire.Date `json:"at"`
}

func reviewMarkKey(folder string, url wire.URL) string { return folder + "|" + string(url) }

// openReviewMarks opens one of the two files. A file from before #6728 and #6729 was each
// folder's URLs, oldest first, with no time: they are dated now.
func (d *Daemon) openReviewMarks(name string) *store.JSONFile[reviewMark] {
	path := filepath.Join(d.Store.Loc.Root, name)
	f := smallFile(d, store.Spec{Name: name, Kind: "key-value", Scope: "machine",
		Bounds: store.Bounds{MaxRecordBytes: 8 << 10, MaxCount: reviewMarksMost, MaxAge: reviewKeep, Action: store.EvictOldest}},
		func() string { return path }, func(r *reviewMark) *wire.Date { return &r.At })
	var old map[string][]wire.URL
	data, err := os.ReadFile(path)
	if err != nil || diskjson.Unmarshal(data, &old) != nil || len(old) == 0 {
		return f
	}
	entries := map[string]reviewMark{}
	for folder, urls := range old {
		for _, url := range urls {
			entries[reviewMarkKey(folder, url)] = reviewMark{Folder: folder, URL: url, At: wire.DateOf(nowFunc())}
		}
	}
	if err := f.CarryOver(data, true, entries); err != nil {
		log.Printf("%s: could not bring over the pull requests: %v", name, err)
	}
	return f
}

// marked is the file's marks, each of a pull request in read dated by its last change.
func marked(f *store.JSONFile[reviewMark], read map[string][]wire.ReviewPull) map[string]reviewMark {
	marks, err := f.Read()
	if err != nil {
		log.Printf("for review: %s could not be read: %v", f.Name(), err)
		return marks
	}
	newer := map[string]reviewMark{}
	for folder, prs := range read {
		for _, p := range prs {
			key := reviewMarkKey(folder, p.URL)
			if r, ok := marks[key]; ok && p.UpdatedAt > r.At {
				r.At = p.UpdatedAt
				newer[key] = r
			}
		}
	}
	if len(newer) > 0 {
		changeMarks(f, func(m map[string]reviewMark) { maps.Copy(m, newer) })
	}
	return marks
}

// changeMarks changes the file's marks. A failure is only said.
func changeMarks(f *store.JSONFile[reviewMark], change func(map[string]reviewMark)) {
	if err := f.Update(func(m map[string]reviewMark) error {
		change(m)
		return nil
	}); err != nil {
		log.Printf("for review: %s was not written: %v", f.Name(), err)
	}
}

func markReview(f *store.JSONFile[reviewMark], folder string, url wire.URL) {
	changeMarks(f, func(m map[string]reviewMark) {
		m[reviewMarkKey(folder, url)] = reviewMark{Folder: folder, URL: url, At: wire.DateOf(nowFunc())}
	})
}

// reviewsFor is a folder's pull requests for review, newest first, each one the person opened
// said so; nil when it has none.
func (d *Daemon) reviewsFor(folder string) []wire.ReviewPull {
	d.reviewMu.Lock()
	defer d.reviewMu.Unlock()
	prs := slices.Clone(d.reviews[folder])
	if len(prs) == 0 {
		return nil
	}
	opened := marked(d.opened, nil)
	for i := range prs {
		if _, ok := opened[reviewMarkKey(folder, prs[i].URL)]; ok {
			prs[i].Opened = new(true)
		}
	}
	return prs
}

// reviewPullOf is a forge item as For Review lists it, and whether it belongs there.
func reviewPullOf(i wire.ForgeItem) (wire.ReviewPull, bool) {
	if i.State != "open" || i.URL == nil || *i.URL == "" || (i.Draft != nil && *i.Draft) {
		return wire.ReviewPull{}, false
	}
	// A ref is "github:pr:owner/name#12", or "gitlab:mr:group/project#12".
	r, ok := lineage.RefOf(i.Node)
	if !ok || r.Topic == "" {
		return wire.ReviewPull{}, false
	}
	number, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		return wire.ReviewPull{}, false
	}
	return wire.ReviewPull{URL: wire.URL(*i.URL), Number: number, Title: i.Title, Repo: r.Topic,
		Ref: new(i.Node.Ref), Author: i.Author, UpdatedAt: i.UpdatedAt}, true
}

// reviewer is the connected person's GitHub login, whose requested reviews make a pull request
// wait for them (#5803); "" when GitHub is not connected.
func (d *Daemon) reviewer() string {
	if c, ok := d.githubConnection(); ok {
		return c.Login
	}
	return ""
}

// readReviews reads every folder's open pull requests with a review requested from the person
// once, and says a folder's pins again when its list changed. A forge that could not be read,
// or no person to ask, keeps its last list.
func (d *Daemon) readReviews(ctx context.Context) {
	login := d.reviewer()
	if login == "" {
		return
	}
	list, err := d.forgeListAsked(ctx, true, wire.ForgeListRequest{State: new("open"), Limit: new(int64(forgeListMost))}, login)
	if err != nil || len(list.Errors) > 0 {
		return
	}
	read := map[string][]wire.ReviewPull{}
	for _, i := range list.Items {
		if p, ok := reviewPullOf(i); ok && i.Folder != nil {
			read[*i.Folder] = append(read[*i.Folder], p)
		}
	}
	d.setReviews(read)
}

// setReviews keeps what was read, dismissed ones and ones unchanged for reviewKeep left out, and says each folder that changed.
func (d *Daemon) setReviews(read map[string][]wire.ReviewPull) {
	d.reviewMu.Lock()
	dismissed := marked(d.dismissed, read)
	marked(d.opened, read)
	cutoff := wire.DateOf(nowFunc().Add(-reviewKeep))
	next := map[string][]wire.ReviewPull{}
	for folder, prs := range read {
		var kept []wire.ReviewPull
		for _, p := range prs {
			_, out := dismissed[reviewMarkKey(folder, p.URL)]
			// A forge that gave no time gives 0, which is not old.
			if old := p.UpdatedAt != 0 && p.UpdatedAt <= cutoff; !out && !old {
				kept = append(kept, p)
			}
		}
		slices.SortStableFunc(kept, func(a, b wire.ReviewPull) int {
			switch {
			case a.UpdatedAt > b.UpdatedAt:
				return -1
			case a.UpdatedAt < b.UpdatedAt:
				return 1
			}
			return 0
		})
		if len(kept) > 0 {
			next[folder] = kept
		}
	}
	var changed []string
	for folder, prs := range next {
		if !slices.Equal(prs, d.reviews[folder]) {
			changed = append(changed, folder)
		}
	}
	for folder := range d.reviews {
		if _, ok := next[folder]; !ok {
			changed = append(changed, folder)
		}
	}
	d.reviews = next
	d.reviewMu.Unlock()
	for _, folder := range changed {
		d.pinsChanged(folder)
	}
}

// dismissReview takes a pull request out of a folder's For Review.
func (d *Daemon) dismissReview(folder string, url wire.URL) {
	d.reviewMu.Lock()
	markReview(d.dismissed, folder, url)
	prs := slices.DeleteFunc(slices.Clone(d.reviews[folder]), func(p wire.ReviewPull) bool { return p.URL == url })
	if len(prs) == 0 {
		delete(d.reviews, folder)
	} else {
		d.reviews[folder] = prs
	}
	d.reviewMu.Unlock()
	d.pinsChanged(folder)
}

// openReview keeps that the person opened a pull request, so its row is drawn read.
func (d *Daemon) openReview(folder string, url wire.URL) {
	d.reviewMu.Lock()
	if _, ok := marked(d.opened, nil)[reviewMarkKey(folder, url)]; ok {
		d.reviewMu.Unlock()
		return
	}
	markReview(d.opened, folder, url)
	d.reviewMu.Unlock()
	d.pinsChanged(folder)
}

func (d *Daemon) registerReviews() {
	On(d, wire.CallReviewsDismiss, func(ctx context.Context, c *Client, r wire.ReviewDismissRequest) (wire.Empty, error) {
		folder, err := d.knownPinFolder(r.Folder)
		if err != nil {
			return wire.Empty{}, err
		}
		if strings.TrimSpace(string(r.URL)) == "" {
			return wire.Empty{}, Invalid("reviews/dismiss names the pull request's url.")
		}
		d.dismissReview(folder, r.URL)
		return wire.Empty{}, nil
	})
	On(d, wire.CallReviewsOpen, func(ctx context.Context, c *Client, r wire.ReviewOpenRequest) (wire.Empty, error) {
		folder, err := d.knownPinFolder(r.Folder)
		if err != nil {
			return wire.Empty{}, err
		}
		if strings.TrimSpace(string(r.URL)) == "" {
			return wire.Empty{}, Invalid("reviews/open names the pull request's url.")
		}
		d.openReview(folder, r.URL)
		return wire.Empty{}, nil
	})
}

// readReviewsEvery reads For Review's pull requests until ctx ends, at Shutdown (#3522).
func (d *Daemon) readReviewsEvery(ctx context.Context) {
	t := time.NewTimer(reviewFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.readReviews(ctx)
		t.Reset(reviewEvery)
	}
}

// startReviews starts the poll.
func (d *Daemon) startReviews() { d.goLoop("for review", d.readReviewsEvery) }
