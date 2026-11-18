package conflictrisk

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scratch is a clone of a bare origin whose main has a.txt, b.txt, c.txt and x_gen.go, and a
// branch for each request, pushed to origin.
type scratch struct {
	t    *testing.T
	repo string
}

func lots(word string) string { return strings.Repeat(word+"\n", 10) }

func newScratch(t *testing.T) *scratch {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	run(t, root, "init", "-q", "--bare", "-b", "main", origin)
	repo := filepath.Join(root, "repo")
	run(t, root, "init", "-q", "-b", "main", repo)
	run(t, repo, "remote", "add", "origin", origin)
	for _, f := range []string{"a.txt", "b.txt", "c.txt", "x_gen.go"} {
		write(t, repo, f, lots("base"))
	}
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-qm", "base")
	run(t, repo, "push", "-q", "origin", "main")
	run(t, repo, "fetch", "-q", "origin")
	run(t, repo, "remote", "set-head", "origin", "main")
	return &scratch{t: t, repo: repo}
}

// branch makes a branch off from with the files, pushed, and goes back to main.
func (s *scratch) branch(name, from string, files map[string]string) {
	run(s.t, s.repo, "checkout", "-q", "-b", name, from)
	for f, body := range files {
		write(s.t, s.repo, f, body)
	}
	run(s.t, s.repo, "add", ".")
	run(s.t, s.repo, "commit", "-qm", name)
	run(s.t, s.repo, "push", "-q", "origin", name)
	run(s.t, s.repo, "checkout", "-q", "main")
}

// mainMoves changes a file on main and pushes it.
func (s *scratch) mainMoves(file, body string) {
	write(s.t, s.repo, file, body)
	run(s.t, s.repo, "commit", "-qam", "main moves")
	run(s.t, s.repo, "push", "-q", "origin", "main")
	run(s.t, s.repo, "fetch", "-q", "origin")
}

func (s *scratch) analyse(o Options) Report {
	s.t.Helper()
	o.Repo = s.repo
	rep, err := Analyse(context.Background(), o)
	if err != nil {
		s.t.Fatal(err)
	}
	return rep
}

func pr(n int, soon bool) PR {
	return PR{Number: n, FetchRef: "refs/heads/pr" + strconv.Itoa(n), LandsSoon: soon}
}

func byNumber(r Report, n int) Entry {
	for _, e := range r.PRs {
		if e.Number == n {
			return e
		}
	}
	return Entry{}
}

func TestAdviceForEachRequest(t *testing.T) {
	s := newScratch(t)
	s.branch("lane", "main", map[string]string{"a.txt": lots("lane"), "c.txt": lots("lane"), "x_gen.go": lots("lane")})
	s.branch("pr1", "main", map[string]string{"a.txt": lots("one")})      // same file, lands soon
	s.branch("pr2", "main", map[string]string{"b.txt": lots("two")})      // another file
	s.branch("pr3", "main", map[string]string{"x_gen.go": lots("three")}) // only a regenerating file
	s.branch("pr5", "main", map[string]string{"c.txt": lots("five")})     // overlaps, not near landing
	rep := s.analyse(Options{Head: "lane", PRs: []PR{pr(1, true), pr(2, true), pr(3, true), pr(5, false)}})

	if e := byNumber(rep, 1); e.State != Conflict || e.Advice != Wait || len(e.Files) != 1 || e.Files[0] != "a.txt" {
		t.Errorf("#1 = %+v", e)
	}
	if e := byNumber(rep, 2); e.State != Clean || e.Advice != Go {
		t.Errorf("#2 = %+v", e)
	}
	if e := byNumber(rep, 3); e.State != Clean || e.Advice != Go || len(e.Ignored) != 1 {
		t.Errorf("#3 = %+v", e)
	}
	// #5 overlaps, its order is not known and the overlap is small: go, with a note.
	if e := byNumber(rep, 5); e.State != Conflict || e.Advice != Go || !strings.Contains(e.Note, "small overlap") {
		t.Errorf("#5 = %+v", e)
	}
	if rep.Advice != Wait || rep.AdviceFor != 1 || rep.RebaseNow {
		t.Errorf("overall = %s for #%d, rebase %v", rep.Advice, rep.AdviceFor, rep.RebaseNow)
	}
	if !strings.Contains(rep.Words(), "wait for #1") {
		t.Error(rep.Words())
	}
}

func TestConflictsARequestAlreadyHasWithMainAreNotItsPairs(t *testing.T) {
	s := newScratch(t)
	s.branch("stale", "main", map[string]string{"a.txt": lots("stale")})
	s.mainMoves("a.txt", lots("moved")) // the stale request now conflicts with main by itself
	s.branch("lane", "origin/main", map[string]string{"a.txt": lots("lane")})
	rep := s.analyse(Options{Head: "lane", PRs: []PR{{Number: 4, FetchRef: "refs/heads/stale", LandsSoon: true}}})
	if e := rep.PRs[0]; e.State != Clean || e.Advice != Go || len(e.MainConflicts) != 1 {
		t.Errorf("%+v", e)
	}
}

func TestTheLanesOwnConflictWithMainIsRebaseNow(t *testing.T) {
	s := newScratch(t)
	s.branch("lane", "main", map[string]string{"a.txt": lots("lane")})
	s.mainMoves("a.txt", lots("moved"))
	rep := s.analyse(Options{Head: "lane"})
	if !rep.RebaseNow || rep.Advice != Go || len(rep.MainConflicts) != 1 {
		t.Errorf("%+v", rep)
	}
}

func TestStackWhenTheChangeBuildsOnARequestNotLanding(t *testing.T) {
	s := newScratch(t)
	big := lots("one") + strings.Repeat("more\n", 60)
	s.branch("pr1", "main", map[string]string{"a.txt": big})
	// The lane builds on pr1's first commit, and pr1 has moved on in the same file since.
	s.branch("lane", "pr1", map[string]string{"a.txt": big + "lane\n"})
	run(t, s.repo, "checkout", "-q", "pr1")
	write(t, s.repo, "a.txt", lots("one")+strings.Repeat("changed\n", 60))
	run(t, s.repo, "commit", "-qam", "pr1 again")
	run(t, s.repo, "push", "-q", "origin", "pr1")
	run(t, s.repo, "checkout", "-q", "main")
	rep := s.analyse(Options{Head: "lane", PRs: []PR{pr(1, false)}})
	if e := rep.PRs[0]; e.State != Conflict || e.Advice != Stack {
		t.Errorf("%+v", e)
	}
	if rep.Advice != Stack || rep.AdviceFor != 1 {
		t.Errorf("%s %d", rep.Advice, rep.AdviceFor)
	}
	// The lead can say the change depends on a request too.
	s.branch("lane2", "main", map[string]string{"b.txt": lots("lane") + strings.Repeat("big\n", 100)})
	s.branch("pr6", "main", map[string]string{"b.txt": lots("six") + strings.Repeat("big2\n", 100)})
	rep = s.analyse(Options{Head: "lane2", DependsOn: []int{6}, PRs: []PR{pr(6, false)}})
	if rep.Advice != Stack {
		t.Errorf("%+v", rep)
	}
}

func TestTheWorkingTreeIsTheDiffAndNothingChanges(t *testing.T) {
	s := newScratch(t)
	s.branch("pr1", "main", map[string]string{"a.txt": lots("one"), "new.txt": "theirs\n"})
	write(t, s.repo, "a.txt", lots("mine"))
	write(t, s.repo, "new.txt", "mine\n") // a new file, never added
	before := run(t, s.repo, "status", "--porcelain")
	rep := s.analyse(Options{PRs: []PR{pr(1, true)}})
	if e := rep.PRs[0]; e.State != Conflict || e.Advice != Wait || !strings.Contains(strings.Join(e.Files, ","), "a.txt") {
		t.Errorf("%+v", e)
	}
	if after := run(t, s.repo, "status", "--porcelain"); after != before {
		t.Errorf("status changed: %q -> %q", before, after)
	}
}

func TestAProjectNamesFilesThatRegenerateAndAnUnfetchableRequestIsNotChecked(t *testing.T) {
	s := newScratch(t)
	s.branch("lane", "main", map[string]string{"b.txt": lots("lane")})
	s.branch("pr2", "main", map[string]string{"b.txt": lots("two")})
	rep := s.analyse(Options{Head: "lane", Ignore: []string{"b.*"}, PRs: []PR{pr(2, true), pr(9, false)}})
	if e := byNumber(rep, 2); e.State != Clean || len(e.Ignored) != 1 {
		t.Errorf("#2 = %+v", e)
	}
	if e := byNumber(rep, 9); e.State != Errored || e.Advice != Go {
		t.Errorf("#9 = %+v", e)
	}
	if !strings.Contains(rep.Words(), "#9: not checked") {
		t.Error(rep.Words())
	}
}

func TestTheLanesOwnRequestIsLeftOut(t *testing.T) {
	s := newScratch(t)
	s.branch("pr1", "main", map[string]string{"a.txt": lots("one")})
	rep := s.analyse(Options{Head: "pr1", Self: 1, PRs: []PR{pr(1, true)}})
	if len(rep.PRs) != 0 || rep.Advice != Go {
		t.Errorf("%+v", rep)
	}
}

// #5661: when git cannot find the default branch, the error says git's own words, so a folder
// git cannot run in (a file URL, a cancelled call) is not taken for a repo with no main.
func TestNoDefaultBranchSaysWhatGitSaid(t *testing.T) {
	s := newScratch(t)
	_, err := Analyse(context.Background(), Options{Repo: "file://" + s.repo})
	if err == nil || !strings.Contains(err.Error(), "git rev-parse:") || !strings.Contains(err.Error(), "file://") {
		t.Fatalf("a folder git cannot run in: %v", err)
	}
	empty := t.TempDir()
	run(t, empty, "init", "-q", "-b", "main")
	_, err = Analyse(context.Background(), Options{Repo: empty})
	if err == nil || !strings.Contains(err.Error(), "git rev-parse: fatal:") {
		t.Fatalf("a repo with no default branch: %v", err)
	}
}

// A worktree of the clone finds the default branch, as a lane's worktree does.
func TestAWorktreeFindsTheDefaultBranch(t *testing.T) {
	s := newScratch(t)
	wt := filepath.Join(t.TempDir(), "lane")
	run(t, s.repo, "worktree", "add", "-q", "-b", "lane", wt, "origin/main")
	write(t, wt, "a.txt", lots("lane"))
	rep, err := Analyse(context.Background(), Options{Repo: wt})
	if err != nil || rep.Main != run(t, s.repo, "rev-parse", "origin/main") || rep.Advice != Go {
		t.Fatalf("%v %+v", err, rep)
	}
}

// convRefs are the refs a run of the tool keeps: refs/conflict-risk/*.
func convRefs(t *testing.T, repo string) []string {
	t.Helper()
	out := run(t, repo, "for-each-ref", "--format=%(refname)", "refs/conflict-risk/")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// manyRequests makes n requests pr1…prn, each with a file of its own, pushed in one push.
func (s *scratch) manyRequests(n int) {
	for i := 1; i <= n; i++ {
		name := "pr" + strconv.Itoa(i)
		run(s.t, s.repo, "checkout", "-q", "-b", name, "main")
		write(s.t, s.repo, name+".txt", lots(name))
		run(s.t, s.repo, "add", ".")
		run(s.t, s.repo, "commit", "-qm", name)
	}
	run(s.t, s.repo, "checkout", "-q", "main")
	run(s.t, s.repo, "push", "-q", "origin", "--all")
}

// open is the requests from..to, as the forge lists them.
func open(from, to int) []PR {
	var prs []PR
	for i := from; i <= to; i++ {
		prs = append(prs, pr(i, false))
	}
	return prs
}

// P20261117-2: a run leaves no ref per request behind it, so the refs of closed requests
// (and the objects they keep from git gc) do not grow with every request the tool ever saw.
// The flat refs/conflict-risk/<n> that runs before the fix left go on the next run.
func TestARunLeavesNoRefsOfClosedRequests(t *testing.T) {
	s := newScratch(t)
	s.branch("lane", "main", map[string]string{"a.txt": lots("lane")})
	s.manyRequests(4)
	run(t, s.repo, "update-ref", "refs/conflict-risk/99", "main") // an older run's
	for i, prs := range [][]PR{open(1, 3), open(2, 4), open(4, 4)} {
		rep := s.analyse(Options{Head: "lane", PRs: prs})
		for _, e := range rep.PRs {
			if e.State != Clean {
				t.Errorf("run %d: #%d = %+v", i, e.Number, e)
			}
		}
		if refs := convRefs(t, s.repo); len(refs) > len(prs) {
			t.Errorf("run %d: %d requests open, %d refs kept: %v", i, len(prs), len(refs), refs)
		}
	}
	if refs := convRefs(t, s.repo); len(refs) != 0 {
		t.Errorf("refs left after the runs: %v", refs)
	}
}

// Two lanes can call the tool at once in one repository: neither run deletes a head that the
// other reads, so every entry of both is checked.
func TestRunsAtOnceReadTheirOwnHeads(t *testing.T) {
	s := newScratch(t)
	s.branch("lane", "main", map[string]string{"a.txt": lots("lane")})
	s.manyRequests(8)
	errs := make(chan string, 64)
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 5; i++ {
				// Each run's list differs: one request closed, another opened.
				prs := open(1+(g+i)%4, 5+(g+i)%4)
				rep, err := Analyse(context.Background(), Options{Repo: s.repo, Head: "lane", PRs: prs})
				if err != nil {
					errs <- err.Error()
					continue
				}
				for _, e := range rep.PRs {
					if e.State != Clean {
						errs <- "#" + strconv.Itoa(e.Number) + ": " + e.State + " " + e.Note
					}
				}
			}
		}(g)
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if refs := convRefs(t, s.repo); len(refs) != 0 {
		t.Errorf("refs left after the runs: %v", refs)
	}
}

// The measurement of P20261117-2: 100 runs, the open list moving by one request each run (one
// closes, one opens), and the count of refs/conflict-risk/* after each. With AGENTS_RUN_PERF=1
// it is 100 runs and it logs the numbers; else 10, to keep the suite fast.
func TestRefsStayBoundedOverManyRuns(t *testing.T) {
	runs, width := 10, 10
	if os.Getenv("AGENTS_RUN_PERF") == "1" {
		runs = 100
	}
	s := newScratch(t)
	s.branch("lane", "main", map[string]string{"a.txt": lots("lane")})
	s.manyRequests(runs + width)
	most := 0
	start := time.Now()
	for i := 1; i <= runs; i++ {
		prs := open(i, i+width-1)
		s.analyse(Options{Head: "lane", PRs: prs})
		n := len(convRefs(t, s.repo))
		if n > most {
			most = n
		}
		if n > len(prs) {
			t.Errorf("run %d: %d requests open, %d refs kept", i, len(prs), n)
		}
	}
	t.Logf("MEASURE runs=%d open=%d refs_after=%d most=%d elapsed_ms=%d", runs, width,
		len(convRefs(t, s.repo)), most, time.Since(start).Milliseconds())
}

// A run deletes the folder of a run older than staleRun (one that was killed), and keeps the
// folder of a run that may still be going, and a ref that is not a run's.
func TestARunDeletesOnlyLeftOverRuns(t *testing.T) {
	s := newScratch(t)
	s.branch("lane", "main", map[string]string{"a.txt": lots("lane")})
	old := runRefs(time.Now().Add(-2*staleRun)) + "7"
	live := runRefs(time.Now().Add(-time.Minute)) + "7"
	other := "refs/conflict-risk/someone/7"
	for _, r := range []string{old, live, other} {
		run(t, s.repo, "update-ref", r, "main")
	}
	s.analyse(Options{Head: "lane"})
	refs := strings.Join(convRefs(t, s.repo), " ")
	if strings.Contains(refs, old) || !strings.Contains(refs, live) || !strings.Contains(refs, other) {
		t.Errorf("refs after a run: %s", refs)
	}
}
