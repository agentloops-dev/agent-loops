package agentsd

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The read-only checks (#3355): every few hours, the Daily checks workflow's agent asks
// this host, with check_folders, what looks wrong in the folders that had no work since the last
// check: failing CI on the default branch, pull requests that went stale or are in conflict,
// issues assigned to the person that no session has, and a missed scheduled workflow. It keeps
// each finding that needs the person as a suggestion, with a prompt that starts a session on
// it, and an urgent one sends a notice. It reads and never acts.
// Every client lists them in For Review (#3331), with the folder's pins, until the person starts
// a session from one (suggestions/start) or dismisses it (suggestions/dismiss); a dismissed one
// does not come back for 90 days, in For Review or a later check. The daily summary that read them is gone
// (#5853).

const (
	dailyChecksID = "daily-checks"
	// suggestionsKept is the most suggestions the checks keep: the sweep evicts the oldest.
	suggestionsKept = 50
	// checksGap is how far back the first check looks.
	checksGap = 4 * time.Hour
	// staleAfter is how long an open pull request can go unchanged before it is stale.
	staleAfter = 7 * 24 * time.Hour
	// checkFolders is the most quiet folders one check reads with gh.
	checkFolders = 30
	// checkLines is the most findings of one kind a folder lists: the rest is a count.
	checkLines = 10
	// suggestionLinkMost is the longest link of a suggestion, in bytes.
	suggestionLinkMost = 2 << 10
	// dismissalsKept is the most dismissals the checks keep: the sweep evicts the oldest.
	dismissalsKept = 200
	// checksReach is how long a suggestion is kept, and the furthest back a check looks.
	checksReach = 7 * 24 * time.Hour
	// personalWorkflowFolder names the person's own workflows in a claim, the same on every
	// machine whatever its home folder is called.
	personalWorkflowFolder = "~"
)

// oneMachineWorkflows are the person's own workflows that run on one of their machines,
// claimed per schedule, and only in their home folder: never copied into a project (#3192).
var oneMachineWorkflows = map[string]bool{dailyChecksID: true}

// inPersonalHome is whether a session works in the person's home folder: tools/list offers
// such a one check_folders, as a run's workflow is marked on it only after it starts.
func (d *Daemon) inPersonalHome(id wire.UUID) bool {
	a := d.Store.Agent(id)
	home := d.personalHome()
	return a != nil && home != "" && filepath.Clean(store.FolderPath(a.Cwd)) == filepath.Clean(home)
}

func agentTitle(a *wire.Agent) string {
	if a.Title != nil && strings.TrimSpace(*a.Title) != "" {
		return strings.TrimSpace(*a.Title)
	}
	return "Untitled session"
}

func checksClock(t time.Time) string { return t.Local().Format("Mon 15:04") }

// checkBook is what the checks keep in ~/.agents/checks (#6726), two bounded small files.
type checkBook struct {
	// mu keeps one read-change-write of the two files at a time.
	mu sync.Mutex
	// suggestions is suggestions.json, by id, and when the last check started (checkedKey).
	suggestions *store.JSONFile[checkSuggestion]
	// dismissed is dismissed.json, by dismissalKey.
	dismissed *store.JSONFile[dismissedSuggestion]
	// carried is that the file from before #6726 is brought over, or there is none.
	carried bool
}

// checkedKey is the entry of suggestions.json whose time is when the last check started.
const checkedKey = "checked"

// checkSuggestion is one finding the checks keep for the person.
type checkSuggestion struct {
	Folder string    `json:"folder,omitempty"`
	Title  string    `json:"title,omitempty"`
	Prompt string    `json:"prompt,omitempty"`
	Link   string    `json:"link,omitempty"`
	Urgent bool      `json:"urgent,omitempty"`
	At     wire.Date `json:"at"`
	// Seq orders the ones found in one millisecond, the file's smallest time.
	Seq int64 `json:"seq,omitempty"`
}

// suggestionID is a suggestion's key and its name on the wire (#3331): the same for as long as
// it is kept.
func suggestionID(folder, title string, at time.Time) string {
	sum := sha256.Sum256([]byte(folder + "\x00" + title + "\x00" + at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:8])
}

// dismissedSuggestion is one the person dismissed (#3331): a check that finds it again, by
// its folder and title, does not keep it for 90 days.
type dismissedSuggestion struct {
	Folder string    `json:"folder"`
	Title  string    `json:"title"`
	At     wire.Date `json:"at"`
}

func dismissalKey(folder, title string) string {
	sum := sha256.Sum256([]byte(folder + "\x00" + title))
	return hex.EncodeToString(sum[:])
}

// checksPath is a file of the checks; with no home it is "", which reads as no file: a relative
// path would be in a scratch daemon's working folder (#5815).
func (d *Daemon) checksPath(name string) string {
	home := d.personalHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".agents", "checks", name)
}

// openChecks opens the checks' files. Both evict: a suggestion that goes is found again by a
// later check, and a dismissal that goes lets its finding be suggested again.
func (d *Daemon) openChecks() {
	d.checks.suggestions = smallFile(d, store.Spec{Name: "checks/suggestions.json", Kind: "document", Scope: "person",
		Bounds: store.Bounds{MaxRecordBytes: 32 << 10, MaxCount: suggestionsKept + 1, MaxAge: checksReach, Action: store.EvictOldest}},
		func() string { return d.checksPath("suggestions.json") }, func(s *checkSuggestion) *wire.Date { return &s.At })
	d.checks.dismissed = smallFile(d, store.Spec{Name: "checks/dismissed.json", Kind: "document", Scope: "person",
		Bounds: store.Bounds{MaxRecordBytes: 8 << 10, MaxCount: dismissalsKept, MaxAge: store.MaxAge, Action: store.EvictOldest}},
		func() string { return d.checksPath("dismissed.json") }, func(x *dismissedSuggestion) *wire.Date { return &x.At })
}

// carryOverChecksLocked brings over suggestions.json from before #6726, one record of lists. It
// runs before each use of the files, not when they are opened: the home is not known then.
func (d *Daemon) carryOverChecksLocked() {
	path := d.checksPath("suggestions.json")
	if path == "" || d.checks.carried {
		return
	}
	var old struct {
		CheckedAt   time.Time
		Suggestions []struct {
			Folder, Title, Prompt, Link string
			Urgent                      bool
			At                          time.Time
		}
		Dismissed []struct {
			Folder, Title string
			At            time.Time
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &old) != nil ||
		old.CheckedAt.IsZero() && len(old.Suggestions)+len(old.Dismissed) == 0 {
		d.checks.carried = err == nil || errors.Is(err, os.ErrNotExist)
		return
	}
	// The dismissals first: a suggestion that a stop loses here, a later check finds again.
	err = d.checks.dismissed.Update(func(m map[string]dismissedSuggestion) error {
		for _, x := range old.Dismissed {
			m[dismissalKey(x.Folder, x.Title)] = dismissedSuggestion{Folder: x.Folder, Title: x.Title, At: wire.DateOf(x.At)}
		}
		return nil
	})
	if err == nil {
		// Not left to Update, which would set the old record aside as corrupt.
		err = os.Remove(path)
	}
	if err != nil {
		log.Printf("daily checks: could not bring over the suggestions: %v", err)
		return
	}
	d.checks.carried = true
	if err := d.checks.suggestions.Update(func(m map[string]checkSuggestion) error {
		if !old.CheckedAt.IsZero() {
			m[checkedKey] = checkSuggestion{At: wire.DateOf(old.CheckedAt)}
		}
		for i, s := range old.Suggestions {
			if len(s.Link) > suggestionLinkMost {
				s.Link = ""
			}
			m[suggestionID(s.Folder, s.Title, s.At)] = checkSuggestion{Folder: s.Folder, Title: s.Title, Prompt: s.Prompt,
				Link: s.Link, Urgent: s.Urgent, At: wire.DateOf(s.At), Seq: int64(i + 1)}
		}
		return nil
	}); err != nil {
		log.Printf("daily checks: could not bring over the suggestions: %v", err)
	}
}

// suggestionsLocked is the suggestions of a folder, by id.
func (d *Daemon) suggestionsLocked(folder string) map[string]checkSuggestion {
	d.carryOverChecksLocked()
	m, err := d.checks.suggestions.Read()
	if err != nil {
		log.Printf("daily checks: %v", err)
	}
	maps.DeleteFunc(m, func(id string, s checkSuggestion) bool {
		return id == checkedKey || filepath.Clean(s.Folder) != filepath.Clean(folder)
	})
	return m
}

// updateSuggestionsLocked changes the suggestions, in the folder only the person reads.
func (d *Daemon) updateSuggestionsLocked(change func(map[string]checkSuggestion)) error {
	path := d.checksPath("suggestions.json")
	if path == "" {
		return errNoPersonalHome
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	d.carryOverChecksLocked()
	return d.checks.suggestions.Update(func(m map[string]checkSuggestion) error {
		change(m)
		return nil
	})
}

// byChecksWorkflow is whether a session is a check's own: it is not work.
func byChecksWorkflow(a *wire.Agent) bool {
	return a.StartedByWorkflow != nil && *a.StartedByWorkflow == dailyChecksID
}

// runsDailyChecks is whether an agent may call check_folders: the Daily checks workflow's, in
// the person's home folder.
func (d *Daemon) runsDailyChecks(id wire.UUID) bool {
	a := d.Store.Agent(id)
	return d.inPersonalHome(id) && a.StartedByWorkflow != nil && *a.StartedByWorkflow == dailyChecksID
}

// dailyChecks is check_folders: collect, the findings in the quiet folders; suggest, keep one.
func (d *Daemon) dailyChecks(id wire.UUID, args map[string]json.RawMessage) (string, error) {
	if !d.runsDailyChecks(id) {
		return "", &Error{Code: wire.FailureNotAllowed, Message: "Only the Daily checks workflow's session, in your home folder, can use check_folders."}
	}
	switch argString(args, "action") {
	case "collect":
		return d.checkFacts(time.Now()), nil
	case "suggest":
		return d.keepSuggestion(id, args, time.Now())
	}
	return "", Invalid("Send `action`: collect, for what looks wrong, or suggest, with `folder`, `title` and `prompt`, to keep one finding.")
}

// quietFolders is the folders here, but the home folder, that no session worked in since.
func (d *Daemon) quietFolders(since time.Time) []string {
	from := wire.DateOf(since)
	busy := map[string]bool{filepath.Clean(d.personalHome()): true}
	for _, a := range d.Store.Agents() {
		if !byChecksWorkflow(&a) && a.LastActivityAt >= from {
			busy[filepath.Clean(folderPath(&a))] = true
		}
	}
	var out []string
	for _, f := range d.folderPaths() {
		if busy[filepath.Clean(f)] {
			continue
		}
		if st, err := os.Stat(f); err == nil && st.IsDir() {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// checkFacts is what looks wrong in each folder that had no work since the last check, as
// Markdown the agent decides from. It starts a new check: the next one looks from here.
func (d *Daemon) checkFacts(at time.Time) string {
	var since time.Time
	d.checks.mu.Lock()
	if err := d.updateSuggestionsLocked(func(m map[string]checkSuggestion) {
		if last, ok := m[checkedKey]; ok {
			since = last.At.Time()
		}
		m[checkedKey] = checkSuggestion{At: wire.DateOf(at)}
	}); err != nil {
		log.Printf("daily checks: the check's start was not kept: %v", err)
	}
	d.checks.mu.Unlock()
	if since.IsZero() || since.Before(at.Add(-checksReach)) {
		since = at.Add(-checksGap)
	}

	quiet := d.quietFolders(since)
	missed := map[string][]string{}
	for _, w := range d.workflowSummaries("") {
		o := w.LastOutcome
		if o != nil && o.Refused != nil && o.Refused.Value.MissedWhileClosed != nil && o.Refused.At >= wire.DateOf(since) {
			f := filepath.Clean(store.FolderPath(w.Workflow.Folder))
			missed[f] = append(missed[f], fmt.Sprintf("Workflow %q missed its run at %s: the machine slept or the app was closed.",
				w.Workflow.Name, checksClock(o.Refused.At.Time())))
		}
	}
	gh := findGH()
	var b strings.Builder
	fmt.Fprintf(&b, "# Facts for the daily checks\n\nSince %s, %d of %d folders had no work.\n",
		since.Local().Format("Mon 2 Jan 15:04"), len(quiet), len(d.folderPaths()))
	if gh == "" {
		b.WriteString("This machine has no gh, so CI, pull requests and issues are not read.\n")
	}
	found := 0
	for i, f := range quiet {
		lines := missed[filepath.Clean(f)]
		if gh != "" && i < checkFolders && pushesToForge(f) {
			lines = append(lines, d.forgeChecks(gh, f, at)...)
		}
		if len(lines) == 0 {
			continue
		}
		found++
		fmt.Fprintf(&b, "\n## %s (%s)\n\n", filepath.Base(f), f)
		for _, line := range lines {
			b.WriteString("- " + line + "\n")
		}
	}
	if len(quiet) > checkFolders && gh != "" {
		fmt.Fprintf(&b, "\nOnly the first %d quiet folders were read with gh.\n", checkFolders)
	}
	if found == 0 {
		b.WriteString("\nNothing looks wrong. End without a suggestion.\n")
		return b.String()
	}
	b.WriteString("\nFor each finding that needs the person, call check_folders with action suggest. Do not act on any of them.\n")
	return b.String()
}

// ghRun is one default-branch workflow run, as gh run list gives it.
type ghRun struct {
	WorkflowName string    `json:"workflowName"`
	Conclusion   string    `json:"conclusion"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"createdAt"`
}

// failedConclusions are the ends of a run that mean the default branch is broken.
var failedConclusions = map[string]bool{"failure": true, "timed_out": true, "startup_failure": true}

// forgeChecks is what gh says is wrong in one folder: a workflow whose newest run on the
// default branch failed, a pull request in conflict or unchanged for a week, and an issue
// assigned to the person that no session in the folder is labelled with.
func (d *Daemon) forgeChecks(gh, folder string, at time.Time) []string {
	var repo struct {
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := ghJSON(d.ctx, gh, folder, &repo, "repo", "view", "--json", "defaultBranchRef"); err != nil {
		return []string{"gh could not read this folder's repository: " + err.Error()}
	}
	var out []string
	add := func(kind []string) { out = appendCheckLines(out, kind) }
	if branch := repo.DefaultBranchRef.Name; branch != "" {
		if failing, ok := d.forgeCIFailures(gh, folder, branch); ok {
			add(failing)
		}
	}
	var pulls []struct {
		Number    int       `json:"number"`
		Title     string    `json:"title"`
		URL       string    `json:"url"`
		UpdatedAt time.Time `json:"updatedAt"`
		Mergeable string    `json:"mergeable"`
		IsDraft   bool      `json:"isDraft"`
	}
	if err := ghJSON(d.ctx, gh, folder, &pulls, "pr", "list", "--state", "open", "--limit", "100",
		"--json", "number,title,url,updatedAt,mergeable,isDraft"); err == nil {
		var stuck []string
		for _, p := range pulls {
			switch {
			case p.Mergeable == "CONFLICTING":
				stuck = append(stuck, fmt.Sprintf("Pull request #%d %q is in conflict. %s", p.Number, p.Title, p.URL))
			case !p.IsDraft && at.Sub(p.UpdatedAt) > staleAfter:
				stuck = append(stuck, fmt.Sprintf("Pull request #%d %q has not changed for %d days. %s",
					p.Number, p.Title, int(at.Sub(p.UpdatedAt).Hours()/24), p.URL))
			}
		}
		add(stuck)
	}
	var issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	}
	if err := ghJSON(d.ctx, gh, folder, &issues, "issue", "list", "--state", "open", "--assignee", "@me",
		"--limit", "100", "--json", "number,title,url"); err == nil {
		taken := d.issueLabels(folder)
		var waiting []string
		for _, i := range issues {
			if !taken[fmt.Sprintf("#%d", i.Number)] {
				waiting = append(waiting, fmt.Sprintf("Issue #%d %q is assigned to you, and no session has it. %s", i.Number, i.Title, i.URL))
			}
		}
		add(waiting)
	}
	return out
}

// appendCheckLines appends at most checkLines lines of one kind to out, and a line that
// counts the rest.
func appendCheckLines(out, kind []string) []string {
	for i, line := range kind {
		if i == checkLines {
			out = append(out, fmt.Sprintf("and %d more like that", len(kind)-checkLines))
			break
		}
		out = append(out, line)
	}
	return out
}

// forgeCIFailures is a line for each workflow whose newest run on branch failed. It is
// false when gh could not read the runs.
func (d *Daemon) forgeCIFailures(gh, folder, branch string) ([]string, bool) {
	var runs []ghRun
	if err := ghJSON(d.ctx, gh, folder, &runs, "run", "list", "--branch", branch, "--limit", "30",
		"--json", "workflowName,conclusion,url,createdAt"); err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	var failing []string
	for _, run := range runs { // newest first
		if run.Conclusion == "" || seen[run.WorkflowName] {
			continue
		}
		seen[run.WorkflowName] = true
		if failedConclusions[run.Conclusion] {
			failing = append(failing, fmt.Sprintf("CI on %s fails: the newest %q run, at %s, ended %s. %s",
				branch, run.WorkflowName, checksClock(run.CreatedAt), run.Conclusion, run.URL))
		}
	}
	return failing, true
}

// issueLabels is every label of the sessions in a folder that are not archived, such as "#12".
func (d *Daemon) issueLabels(folder string) map[string]bool {
	out := map[string]bool{}
	for _, a := range d.Store.Agents() {
		if a.State == wire.AgentStateArchived || filepath.Clean(folderPath(&a)) != filepath.Clean(folder) {
			continue
		}
		for _, l := range a.Labels {
			out[l.Value] = true
		}
	}
	return out
}

// ghJSON runs one read-only gh command in a folder and reads its JSON into out.
func ghJSON(ctx context.Context, gh, folder string, out any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, args...)
	cmd.Dir = folder
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	var stdout, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &errs
	if err := cmd.Run(); err != nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(errs.String()), "\n"); line != "" {
			return errors.New(line)
		}
		return err
	}
	return json.Unmarshal(stdout.Bytes(), out)
}

// keepSuggestion is check_folders' suggest: it keeps one finding, the newest in place of one
// with the same folder and title, and sends a notice when it is urgent.
func (d *Daemon) keepSuggestion(id wire.UUID, args map[string]json.RawMessage, at time.Time) (string, error) {
	s := checkSuggestion{
		Folder: strings.TrimSpace(argString(args, "folder")),
		Title:  prefix(strings.Join(strings.Fields(argString(args, "title")), " "), 120),
		Prompt: prefix(strings.TrimSpace(argString(args, "prompt")), 2000),
		Link:   strings.TrimSpace(argString(args, "link")),
		Urgent: argBool(args, "urgent"),
		At:     wire.DateOf(at),
	}
	if s.Title == "" || s.Prompt == "" {
		return "", Invalid("Nothing was kept: send a `title` that says what is wrong and a `prompt` that starts a session on it.")
	}
	if len(s.Link) > suggestionLinkMost {
		return "", Invalid("Nothing was kept: the `link` is over %d bytes.", suggestionLinkMost)
	}
	known := false
	for _, f := range d.folderPaths() {
		if filepath.Clean(f) == filepath.Clean(s.Folder) {
			s.Folder, known = f, true
		}
	}
	if !known {
		return "", Invalid("Nothing was kept: %q is not a folder on this machine. Send the folder's path as the facts give it.", s.Folder)
	}
	d.checks.mu.Lock()
	d.carryOverChecksLocked()
	dismissed, err := d.checks.dismissed.Read()
	if err != nil {
		log.Printf("daily checks: %v", err)
	}
	if _, ok := dismissed[dismissalKey(s.Folder, s.Title)]; ok {
		d.checks.mu.Unlock()
		return "Not kept: the person dismissed this one before. Do not suggest it again.", nil
	}
	err = d.updateSuggestionsLocked(func(m map[string]checkSuggestion) {
		for id, old := range m {
			s.Seq = max(s.Seq, old.Seq+1)
			if id != checkedKey && old.Folder == s.Folder && old.Title == s.Title {
				delete(m, id)
			}
		}
		m[suggestionID(s.Folder, s.Title, at)] = s
	})
	d.checks.mu.Unlock()
	if err != nil {
		return "", &Error{Message: "The suggestion was not kept: " + err.Error()}
	}
	d.pinsChanged(s.Folder)
	said := "Kept: For Review lists it."
	if s.Urgent {
		if reply, err := d.notify(id, map[string]json.RawMessage{"title": jsonString("Daily checks"),
			"body": jsonString(s.Title + " (" + filepath.Base(s.Folder) + ")")}); err != nil {
			said += " No notice: " + err.Error()
		} else {
			said += " " + reply
		}
	}
	return said, nil
}

// suggestionsFor is a folder's suggestions for For Review (#3331), newest first: those of the
// last week.
func (d *Daemon) suggestionsFor(folder string, at time.Time) []wire.CheckSuggestion {
	d.checks.mu.Lock()
	kept := d.suggestionsLocked(folder)
	d.checks.mu.Unlock()
	ids := slices.SortedFunc(maps.Keys(kept), func(x, y string) int {
		return cmp.Or(cmp.Compare(kept[y].At, kept[x].At), cmp.Compare(kept[y].Seq, kept[x].Seq))
	})
	var out []wire.CheckSuggestion
	for _, id := range ids {
		s := kept[id]
		if at.Sub(s.At.Time()) >= checksReach {
			continue // the next sweep takes it out
		}
		c := wire.CheckSuggestion{ID: id, Title: s.Title, Prompt: s.Prompt, At: s.At}
		if s.Link != "" {
			c.Link = new(wire.URL(s.Link))
		}
		if s.Urgent {
			c.Urgent = new(true)
		}
		out = append(out, c)
	}
	return out
}

// takeSuggestion takes one of a folder's suggestions out (#3331): started from, or dismissed,
// which is kept so that it does not come back for 90 days. False when there is no such suggestion.
func (d *Daemon) takeSuggestion(folder, id string, dismiss bool, at time.Time) (checkSuggestion, bool, error) {
	d.checks.mu.Lock()
	defer d.checks.mu.Unlock()
	s, ok := d.suggestionsLocked(folder)[id]
	if !ok {
		return checkSuggestion{}, false, nil
	}
	if dismiss {
		if err := d.checks.dismissed.Update(func(m map[string]dismissedSuggestion) error {
			m[dismissalKey(s.Folder, s.Title)] = dismissedSuggestion{Folder: s.Folder, Title: s.Title, At: wire.DateOf(at)}
			return nil
		}); err != nil {
			return s, true, err
		}
	}
	return s, true, d.updateSuggestionsLocked(func(m map[string]checkSuggestion) { delete(m, id) })
}

// findSuggestion is one of a folder's suggestions, by its id.
func (d *Daemon) findSuggestion(folder, id string) (checkSuggestion, bool) {
	d.checks.mu.Lock()
	defer d.checks.mu.Unlock()
	s, ok := d.suggestionsLocked(folder)[id]
	return s, ok
}

func (d *Daemon) registerSuggestions() {
	gone := func() error {
		return Invalid("That suggestion is not there any more: it was started or dismissed, or is over a week old.")
	}
	On(d, wire.CallSuggestionsDismiss, func(ctx context.Context, c *Client, r wire.SuggestionRequest) (wire.Empty, error) {
		folder, err := d.knownPinFolder(r.Folder)
		if err != nil {
			return wire.Empty{}, err
		}
		_, ok, err := d.takeSuggestion(folder, r.ID, true, time.Now())
		if err != nil {
			return wire.Empty{}, &Error{Message: "The dismissal was not kept: " + err.Error()}
		}
		d.pinsChanged(folder)
		if !ok {
			return wire.Empty{}, gone()
		}
		return wire.Empty{}, nil
	})
	// Start: a session in the suggestion's folder with its prompt, on the Auto runtime, which
	// picks the runtime as a new session's page does; only then is the suggestion taken out.
	On(d, wire.CallSuggestionsStart, func(ctx context.Context, c *Client, r wire.SuggestionRequest) (wire.UUID, error) {
		folder, err := d.knownPinFolder(r.Folder)
		if err != nil {
			return "", err
		}
		s, ok := d.findSuggestion(folder, r.ID)
		if !ok {
			d.pinsChanged(folder)
			return "", gone()
		}
		id, err := d.startAs(wire.StartRequest{RuntimeID: autoRuntimeID, Cwd: store.FolderURL(folder), Prompt: s.Prompt,
			Attachments: []wire.Attachment{}, StartOptions: wire.StartOptions{Values: map[string]wire.JSONValue{}, ExtraArguments: []string{}},
			AdditionalDirectories: []wire.URL{}, McpServers: []wire.MCPServer{}, Labels: []string{}}, c.actor())
		if err != nil {
			return "", err
		}
		if _, _, err := d.takeSuggestion(folder, r.ID, false, time.Now()); err != nil {
			log.Printf("daily checks: a started suggestion was not taken out: %v", err)
		}
		d.pinsChanged(folder)
		return id, nil
	})
}
