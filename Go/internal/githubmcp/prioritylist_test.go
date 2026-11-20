package githubmcp

import "testing"

func TestAPriorityCursorReadsBackItsPriorityAndPlace(t *testing.T) {
	t.Parallel()
	for _, want := range []struct {
		bucket int
		after  string
	}{{0, ""}, {1, "Y3Vyc29y"}, {3, "a:b:c"}} {
		bucket, after, err := readPriorityCursor(priorityCursor(want.bucket, want.after))
		if err != nil || bucket != want.bucket || after != want.after {
			t.Fatalf("read %d %q, %v; want %d %q", bucket, after, err, want.bucket, want.after)
		}
	}
	if b, a, err := readPriorityCursor(""); err != nil || b != 0 || a != "" {
		t.Fatalf("the start: %d %q %v", b, a, err)
	}
	for _, bad := range []string{"Y3Vyc29y", "prio:9:x", "prio:x:y", "prio:1"} {
		if _, _, err := readPriorityCursor(bad); err == nil {
			t.Fatalf("%q read as a priority cursor", bad)
		}
	}
}

func TestAPriorityListReadsGitHubsListOnlyWhenItCan(t *testing.T) {
	t.Parallel()
	one := []string{"o/r"}
	cases := []struct {
		repos []string
		f     SearchFilter
		want  bool
	}{
		{one, SearchFilter{}, true},
		{one, SearchFilter{Assignee: "sam"}, true},
		{one, SearchFilter{PRs: true}, true},
		// GitHub's lists match labels any of, so another label needs search, whose labels match all.
		{one, SearchFilter{Label: "epic"}, false},
		{one, SearchFilter{PRs: true, Assignee: "sam"}, false},
		{one, SearchFilter{Query: "crash"}, false},
		{[]string{"o/r", "o/s"}, SearchFilter{}, false},
	}
	for _, c := range cases {
		if got := readsList(c.repos, c.f); got != c.want {
			t.Fatalf("readsList(%v, %+v) = %v, want %v", c.repos, c.f, got, c.want)
		}
	}
}

func TestAnItemWithAPriorityLabelIsLeftOutOfTheNoneList(t *testing.T) {
	t.Parallel()
	if !hasPriorityLabel(Item{Labels: []string{"bug", "Priority:High"}}) {
		t.Fatal("priority:high not seen")
	}
	if hasPriorityLabel(Item{Labels: []string{"bug", "priority"}}) {
		t.Fatal("a label that is no priority's was taken for one")
	}
}
