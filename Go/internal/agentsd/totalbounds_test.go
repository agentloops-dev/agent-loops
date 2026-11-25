package agentsd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/boundmap"
	"github.com/agentloops-dev/agent-loops/Go/internal/citimings"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7245: the largest record of each store that writeStore writes.

func TestUsageAndTimePastTheirRowsLoseNoTotal(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.costMu.Lock()
	defer d.costMu.Unlock()
	d.timeMu.Lock()
	defer d.timeMu.Unlock()
	const n = 100
	for i := range n {
		name := fmt.Sprintf("r%d", i)
		if i == n-1 {
			name = strings.Repeat("x", runtimeBytes+1)
		}
		d.recordUsageLocked(name, wire.UsageBucket{Cost: map[string]float64{fmt.Sprintf("C%d", i): 1}, Turns: 1})
		addToHour(d.timeLocked(), wire.TimeBucket{Agent: 1}, name, hourStamp(time.Now()))
	}
	var turns int64
	var cost, secs float64
	for _, rows := range d.usageLocked().Days {
		if len(rows) != periodRows {
			t.Errorf("%d runtimes", len(rows))
		}
		for _, b := range rows {
			turns += b.Turns
			if len(b.Cost) > boundmap.MostCurrencies {
				t.Errorf("%d currencies", len(b.Cost))
			}
			for _, v := range b.Cost {
				cost += v
			}
		}
	}
	for _, rows := range d.timeLocked().Days {
		for _, b := range rows {
			secs += b.Agent
		}
	}
	if turns != n || cost != n || secs != n {
		t.Fatalf("%d turns, %v cost, %v seconds of %d", turns, cost, secs, n)
	}
}

// A file from before the bounds is held to them when it is read.
func TestAnOldUsageFileIsHeldToItsRows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	day := dayStamp(time.Now())
	u := usageFile{Days: map[string]map[string]wire.UsageBucket{day: {}}}
	s := spendFile{Days: map[string]map[string]float64{day: {}}}
	for i := range 100 {
		u.Days[day][fmt.Sprintf("r%d", i)] = wire.UsageBucket{Turns: 1}
		s.Days[day][fmt.Sprintf("C%d", i)] = 1
	}
	must(t, writeStore(filepath.Join(root, "usage-history.json"), u))
	must(t, writeStore(filepath.Join(root, "spend.json"), s))
	d, _ := start(t, root)
	defer d.Shutdown()
	d.costMu.Lock()
	defer d.costMu.Unlock()
	var turns int64
	for _, b := range d.usageLocked().Days[day] {
		turns += b.Turns
	}
	var spent float64
	for _, v := range d.spendLocked().Days[day] {
		spent += v
	}
	if len(d.usageLocked().Days[day]) != periodRows || turns != 100 || len(d.spendLocked().Days[day]) != boundmap.MostCurrencies || spent != 100 {
		t.Fatalf("%d runtimes with %d turns, %v spent", len(d.usageLocked().Days[day]), turns, spent)
	}
}

func TestReportRowsPastTheirCountKeepTheRoutesTokens(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.reportMu.Lock()
	hour, one := utcHour(time.Now()), int64(1)
	for i := range 2 * reportRowsMost {
		d.addReportLocked(fmt.Sprintf("f%d", i), "claude", "w", "r1", hour, wire.UsageRollupRow{Turns: 1, LimitTokens: &one})
	}
	for i := range 2 * reportRowsMost {
		d.addReportLocked("f", fmt.Sprintf("rt%d", i), "", "", hour, wire.UsageRollupRow{Turns: 1})
	}
	rows := d.reportLocked().Hours[hour]
	boundReport(d.reportLocked())
	if again := d.reportLocked().Hours[hour]; len(again) != len(rows) {
		t.Errorf("%d rows when read again, and %d before", len(again), len(rows))
	}
	var turns int64
	for _, r := range rows {
		turns += r.Turns
	}
	d.reportMu.Unlock()
	if len(rows) > reportRowsMost || turns != 4*reportRowsMost {
		t.Errorf("%d rows with %d turns", len(rows), turns)
	}
	if got := d.routeTokensToday("claude", "r1", time.Now().UTC()); got != 2*reportRowsMost {
		t.Errorf("%d tokens of the route", got)
	}
}

func TestFeaturesPastTheirCountAreCountedTogether(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	for i := range featureRows + 10 {
		d.countFeature(fmt.Sprintf("m/%d", i), featureWeb)
	}
	d.featureMu.Lock()
	defer d.featureMu.Unlock()
	day := d.featuresLocked().Days[dayStamp(time.Now())]
	var uses int64
	for m, clients := range day {
		if strings.HasPrefix(m, "m/") || m == boundmap.Other {
			uses += clients[featureWeb]
		}
	}
	if len(day) > featureRows || uses != featureRows+10 || day[boundmap.Other][featureWeb] == 0 {
		t.Fatalf("%d methods, %d uses", len(day), uses)
	}
}

func TestALineageNodeOverItsLargestKeepsItsTotal(t *testing.T) {
	t.Parallel()
	var long []string
	for i := range 2_000 {
		long = append(long, fmt.Sprintf("github:issue:a/b#%d%s", i, strings.Repeat("<", 50)))
	}
	word := strings.Repeat("w", costWordBytes)
	c := wire.LineageCostNode{Node: wire.LineageNode{Kind: lineage.KindPR, Ref: "github:pr:a/b#1"},
		Banked: wire.LineageCost{Cost: map[string]float64{"USD": 3}, OutputTokens: 5}, Fixes: long, PartOf: long, Sessions: long, Files: long,
		Feature: &word, Resolved: &wire.LineageResolved{Title: strings.Repeat("é", 4<<10), Labels: long}}
	got := boundCostNode(c)
	if size := jsonSize(got); size > costNodeBytes {
		t.Errorf("%d bytes", size)
	}
	if got.Banked.Cost["USD"] != 3 || got.Banked.OutputTokens != 5 || len(got.Fixes) == 0 || got.Feature != nil ||
		jsonSize(got.Resolved.Title) > costTitleBytes || !strings.HasSuffix(got.Resolved.Title, "…") {
		t.Errorf("%+v", got.Banked)
	}
	// Read again and cut again, it is the same, so it is not sent again without end.
	if again := boundCostNode(c); string(mustJSON(again)) != string(mustJSON(got)) || string(mustJSON(boundCostNode(got))) != string(mustJSON(got)) {
		t.Error("cut again, it is another")
	}
	// At the limits nothing is cut.
	at := wire.LineageCostNode{Node: c.Node, Files: []string{strings.Repeat("x", costListBytes-5)}}
	if len(boundCostNode(at).Files) != 1 {
		t.Error("a list at its limit was cut")
	}
	// A part with no bound of its own.
	c.Ci = &wire.LineagePRCI{HeadSha: strings.Repeat("a", costNodeBytes)}
	if got := boundCostNode(c); jsonSize(got) > costNodeBytes || got.Banked.Cost["USD"] != 3 {
		t.Errorf("%d bytes", jsonSize(got))
	}
}

func TestTheNoticesToldAreBounded(t *testing.T) {
	t.Parallel()
	f := &costLedgerFile{Nodes: map[string]wire.LineageCostNode{"k5": {LastAt: 1}}, Overruns: map[string]int64{}, MilestonesLate: map[string]string{}}
	for i := range costNoticesKept + 10 {
		f.Overruns[fmt.Sprintf("k%d", i)] = 1
		f.MilestonesLate[fmt.Sprintf("m%d", i)] = fmt.Sprintf("2026-%04d", i)
	}
	boundNotices(f)
	if len(f.Overruns) != costNoticesKept || len(f.MilestonesLate) != costNoticesKept || f.Overruns["k5"] == 0 || f.MilestonesLate["m0"] != "" {
		t.Fatalf("%d and %d", len(f.Overruns), len(f.MilestonesLate))
	}
	if k := (milestoneOf{"r", strings.Repeat("n", 4<<10)}).key(); jsonSize(k) > costWordBytes {
		t.Errorf("a key of %d bytes", len(k))
	}
}

func TestUnsentLineagePastItsBoundsIsAWholeSend(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	now := nowFunc()
	link := wire.LineageLink{From: wire.LineageNode{Kind: "issue", Ref: "github:issue:a/b#1"}, To: wire.LineageNode{Kind: "pr", Ref: "github:pr:a/b#2"},
		Rel: "fixes", Source: "agent", At: wire.DateOf(now)}
	saved := func(l wire.LineageLink) lineageSyncFile {
		d.syncMu.Lock()
		d.syncFile = &lineageSyncFile{Kept: true, Refs: ledgerRefsFull, Links: map[string]wire.LineageLink{lineageSyncKey(l): l}}
		d.syncMu.Unlock()
		d.saveLineageSync()
		var f lineageSyncFile
		readStore(d.lineageSyncPath(), &f, "")
		return f
	}
	if f := saved(link); f.Full || len(f.Links) != 1 {
		t.Errorf("%+v", f)
	}
	large := link
	large.From.Ref += strings.Repeat("1", lineageSyncBytes)
	if f := saved(large); !f.Full || len(f.Links) != 0 {
		t.Error("a link over its largest is kept")
	}
	old, ended := link, wire.DateOf(now.Add(-lineage.KeepEnded-time.Hour))
	old.EndedAt = &ended
	if f := saved(old); !f.Full || len(f.Links) != 0 {
		t.Error("a link past its age is kept")
	}
}

func TestTheBuildTimingsStateIsBounded(t *testing.T) {
	t.Parallel()
	now := nowFunc()
	sync := &ciSyncFile{Kept: true, Repos: map[string]*ciSyncRepo{"github:a/unsent": {Days: map[string]int{now.UTC().Format(time.DateOnly): 1, "2020-01-01": 2}},
		"github:" + strings.Repeat("x", ciRepoKeyBytes): {Full: true}}}
	state := &ciTimingsFile{Repos: map[string]*ciRepoState{"old": {DefaultAt: now.AddDate(0, 0, -citimings.KeepDays-1)},
		"named": {DefaultAt: now, Default: strings.Repeat("b", ciNameBytes+1), Slowed: map[string]ciSlowedSent{}}, "new": {}}}
	for i := range ciSlowedKept + 1 {
		state.Repos["named"].Slowed[fmt.Sprint(i)] = ciSlowedSent{Made: int64(i)}
	}
	for i := range ciReposKept {
		sync.Repos[fmt.Sprintf("github:a/%d", i)] = &ciSyncRepo{}
		state.Repos[fmt.Sprintf("r%d", i)] = &ciRepoState{DefaultAt: now.Add(-time.Hour)}
	}
	boundCISync(sync, now)
	if r := sync.Repos["github:a/unsent"]; len(sync.Repos) != ciReposKept || r == nil || len(r.Days) != 1 {
		t.Errorf("%d repositories, %+v", len(sync.Repos), r)
	}
	boundCIFile(state, now)
	named := state.Repos["named"]
	if len(state.Repos) != ciReposKept || state.Repos["old"] != nil || state.Repos["new"] == nil || named == nil {
		t.Fatalf("%d repositories", len(state.Repos))
	}
	if _, first := named.Slowed["0"]; first || named.Default != "" || len(named.Slowed) != ciSlowedKept {
		t.Errorf("%d changes", len(named.Slowed))
	}
	c := citimings.Change{Workflow: strings.Repeat("w", 4<<10), Job: "j"}
	if k := ciSlowedKey(c); len(k) != 64 || !ciSlowedHas(map[string]ciSlowedSent{k: {}}, c) {
		t.Errorf("the key of a long name is %d bytes", len(k))
	}
}

func TestACostCommentOverItsLargestKeepsItsID(t *testing.T) {
	t.Parallel()
	r := &prCommentRecord{ID: "7", Body: strings.Repeat("x", prCommentBytes), Parts: []string{"github:pr:a/b#1"}}
	if !r.bound(10) || r.ID != "7" || r.Body != "" || len(r.Parts) != 1 {
		t.Errorf("%.100v", r)
	}
	r.Parts = []string{strings.Repeat("x", prCommentBytes)}
	if !r.bound(10) || r.ID != "7" || r.Parts != nil {
		t.Errorf("%.100v", r)
	}
	if r.Folder = strings.Repeat("x", prCommentBytes); r.bound(10) {
		t.Error("it fits")
	}
}

func TestADefaultAccountPastTheLimitsIsRefused(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	m := map[string]string{}
	for i := range defaultAccountsMost {
		m[fmt.Sprintf("r%d", i)] = "a"
	}
	must(t, d.writeDefaultAccounts(m))
	m["more"] = "a"
	if err := d.writeDefaultAccounts(m); !errors.Is(err, store.ErrLimit) {
		t.Errorf("one more: %v", err)
	}
	delete(m, "more")
	m["r0"] = strings.Repeat("a", defaultAccountBytes)
	if err := d.writeDefaultAccounts(m); !errors.Is(err, store.ErrLimit) {
		t.Errorf("a large one: %v", err)
	}
	if d.chosenDefault("r0") != "a" || d.chosenDefault("more") != "" {
		t.Error("a refused choice is in use")
	}
}

func TestAnOfferOverItsLargestLeavesNoEarlierOne(t *testing.T) {
	t.Parallel()
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	d.remember("k", optionEntry{Commands: []wire.SlashCommand{{Name: "a"}}})
	d.remember("other", optionEntry{Commands: []wire.SlashCommand{{Name: "a"}}})
	if _, ok := d.remembered("k"); !ok {
		t.Fatal("not kept")
	}
	d.remember("k", optionEntry{Commands: []wire.SlashCommand{{Name: strings.Repeat("a", 256<<10)}}})
	if _, ok := d.remembered("k"); ok {
		t.Error("the earlier offer is still shown")
	}
	if _, ok := d.remembered("other"); !ok {
		t.Error("the other offer is gone")
	}
	info, err := os.Stat(d.optionCachePath())
	must(t, err)
	if info.Size() > 256<<10 {
		t.Errorf("%d bytes", info.Size())
	}
}
