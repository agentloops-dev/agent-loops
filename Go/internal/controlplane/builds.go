package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/citimings"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The copy of the build timings (#5232, spec 5166, "Where"): each machine that reads a
// repository's CI runs sends each day of them that changed (builds/sync) a few seconds after
// the change, and the control plane keeps them per machine and repository, under
// v1/build-timings/<host>/<forge>/<path>/<YYYY-MM-DD>.json, with meta.json for the default
// branch, the last read and the backfill's reach. Only that machine writes its objects, so
// nothing locks; a read merges the machines' runs, one for each ci-run ref and attempt.
// Only refs, times, attempts, conclusions, runner labels and names (principle 7).
//
//   - The machine's store bounds what it sends (citimings: 90 days, 20,000 runs a repository,
//     a pull request's steps for 14 days), and each day sent is bounded again here
//     (citimings.BoundDay).
//   - The hourly sweep applies the bounds (citimings.SweepDays) to a machine that stopped
//     sending, once a day, and deletes a removed machine's objects.
//   - builds/historyAll and builds/runAll answer as the machine's builds/history and
//     builds/run do, over the machines in the scope (as reports/value's).

const (
	// store-bounds: kind=time series; count=31 days a repository of a machine; size=4MiB a record (a day's runs); age=90d; applied=on write (the oldest runs of a day are cut to fit), the hourly sweep deletes days past the age and a removed machine's
	buildsPrefix = "v1/build-timings/"
	buildsMeta   = "meta.json"
	// mostBuildsDays and mostBuildsRuns bound one send; mostBuildsJobs and mostBuildsSteps one run.
	mostBuildsDays  = 31
	mostBuildsRuns  = 5_000
	mostBuildsJobs  = 500
	mostBuildsSteps = 1_000
	// buildsStopped is how long after its last send a machine's copy is swept here, and
	// buildsSweepEvery how often then.
	buildsStopped    = 2 * time.Hour
	buildsSweepEvery = 23 * time.Hour
)

// storedBuildsDay is one machine's runs of one repository made on one UTC day.
type storedBuildsDay struct {
	Runs []citimings.Run `json:"runs"`
	At   records.Stamp   `json:"at"`
}

// storedBuildsMeta is what a machine last said of a repository.
type storedBuildsMeta struct {
	DefaultBranch string         `json:"defaultBranch,omitempty"`
	ReadAt        *time.Time     `json:"readAt,omitempty"`
	FilledFrom    *time.Time     `json:"filledFrom,omitempty"`
	At            records.Stamp  `json:"at"`
	SweptAt       *records.Stamp `json:"sweptAt,omitempty"`
}

// buildsDir is a machine's folder of a repository's objects, ending in /.
func buildsDir(host string, r citimings.Repo) string {
	return buildsPrefix + strings.ToLower(host) + "/" + r.Forge + "/" + r.Path + "/"
}

// now is Config.Now, or the wall clock.
func (s *Service) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// keepBuildsSync is builds/sync: each day sent in place of the machine's copy of it, or in
// place of the whole copy with reset. Full says there is no copy to put them over.
func (s *Service) keepBuildsSync(ctx context.Context, host string, sync wire.BuildsSync, now time.Time) (wire.BuildsSyncAnswer, error) {
	if s.records.Host(host) == nil {
		return wire.BuildsSyncAnswer{}, fail(noSuchHost, "No machine is called %s.", host)
	}
	repo, err := citimings.ParseRepo(sync.Repo)
	if err != nil {
		return wire.BuildsSyncAnswer{}, fail(invalidParams, "builds/sync: %v.", err)
	}
	days, err := checkBuildsSync(sync)
	if err != nil {
		return wire.BuildsSyncAnswer{}, err
	}
	dir := buildsDir(host, repo)
	if sync.Reset != nil && *sync.Reset {
		if err := s.deleteUnsentBuildsDays(ctx, dir, days); err != nil {
			return wire.BuildsSyncAnswer{}, err
		}
	} else {
		object, err := s.cfg.Store.Get(ctx, dir+buildsMeta)
		if err != nil {
			return wire.BuildsSyncAnswer{}, err
		}
		if object == nil {
			// Nothing to put a change over: the machine sends all it has, the first with reset.
			return wire.BuildsSyncAnswer{Full: new(true)}, nil
		}
	}
	if err := s.putBuildsDays(ctx, dir, sync.Days, days, now); err != nil {
		return wire.BuildsSyncAnswer{}, err
	}
	if err := s.putBuildsMeta(ctx, dir, sync, now); err != nil {
		return wire.BuildsSyncAnswer{}, err
	}
	return wire.BuildsSyncAnswer{}, nil
}

// deleteUnsentBuildsDays deletes each day object in dir that the reset does not send again.
func (s *Service) deleteUnsentBuildsDays(ctx context.Context, dir string, days map[string][]citimings.Run) error {
	listed, err := s.cfg.Store.List(ctx, dir)
	if err != nil {
		return err
	}
	for _, k := range listed {
		day, ok := strings.CutSuffix(strings.TrimPrefix(k.Key, dir), ".json")
		if _, sent := days[day]; ok && citimings.DayOK(day) && !sent {
			if err := s.cfg.Store.Delete(ctx, k.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// putBuildsDays writes each day sent, bounded, in place of the copy's day; a day with no runs
// left is deleted.
func (s *Service) putBuildsDays(ctx context.Context, dir string, sent []wire.BuildsSyncDay, days map[string][]citimings.Run, now time.Time) error {
	for _, d := range sent {
		runs := citimings.BoundDay(d.Day, days[d.Day], now)
		if len(runs) == 0 {
			if err := s.cfg.Store.Delete(ctx, dir+d.Day+".json"); err != nil {
				return err
			}
			continue
		}
		data, err := encodeBuildsDay(runs, now)
		if err != nil {
			return err
		}
		if _, err := s.cfg.Store.Put(ctx, dir+d.Day+".json", data, store.Always); err != nil {
			return err
		}
	}
	return nil
}

// encodeBuildsDay is a day's runs as stored. Runs are oldest first, so those over buildsDayBytes
// that the machine sent last go: it sends them again (#7248).
func encodeBuildsDay(runs []citimings.Run, now time.Time) ([]byte, error) {
	_, data, err := fitting(runs, buildsDayBytes, func(some []citimings.Run) ([]byte, error) {
		return records.Encode(storedBuildsDay{Runs: some, At: records.At(now)})
	})
	return data, err
}

// putBuildsMeta writes the copy's meta.json: what the machine said of the repository, and now.
func (s *Service) putBuildsMeta(ctx context.Context, dir string, sync wire.BuildsSync, now time.Time) error {
	meta := storedBuildsMeta{At: records.At(now)}
	if sync.DefaultBranch != nil {
		meta.DefaultBranch = *sync.DefaultBranch
	}
	if sync.ReadAt != nil {
		meta.ReadAt = new(sync.ReadAt.Time().UTC())
	}
	if sync.FilledFrom != nil {
		meta.FilledFrom = new(sync.FilledFrom.Time().UTC())
	}
	meta.DefaultBranch = cut(meta.DefaultBranch, 1<<10)
	data, err := records.Encode(meta)
	if err != nil {
		return err
	}
	if err := within("The repository's meta", data, buildsMetaBytes); err != nil {
		return err
	}
	_, err = s.cfg.Store.Put(ctx, dir+buildsMeta, data, store.Always)
	return err
}

// checkBuildsSync is the runs of each day sent, or why the send is refused.
func checkBuildsSync(sync wire.BuildsSync) (map[string][]citimings.Run, error) {
	if len(sync.Days) > mostBuildsDays {
		return nil, fail(invalidParams, "Send at most %d days at a time.", mostBuildsDays)
	}
	days := make(map[string][]citimings.Run, len(sync.Days))
	n := 0
	for _, d := range sync.Days {
		if !citimings.DayOK(d.Day) {
			return nil, fail(invalidParams, "%q is not a day (YYYY-MM-DD).", d.Day)
		}
		if _, twice := days[d.Day]; twice {
			return nil, fail(invalidParams, "The day %s is sent twice.", d.Day)
		}
		if n += len(d.Runs); n > mostBuildsRuns {
			return nil, fail(invalidParams, "Send at most %d runs at a time.", mostBuildsRuns)
		}
		runs := make([]citimings.Run, 0, len(d.Runs))
		for _, w := range d.Runs {
			r := citimings.RunFromWire(w)
			if r.Ref == "" || r.Attempt < 1 {
				return nil, fail(invalidParams, "A run with no ref or attempt: %d.", r.ID)
			}
			if citimings.DayOf(r.MadeAt()) != d.Day {
				return nil, fail(invalidParams, "The run %s was not made on %s.", r.Ref, d.Day)
			}
			if len(r.Jobs) > mostBuildsJobs {
				return nil, fail(invalidParams, "The run %s has more than %d jobs.", r.Ref, mostBuildsJobs)
			}
			for _, j := range r.Jobs {
				if len(j.Steps) > mostBuildsSteps {
					return nil, fail(invalidParams, "The run %s has a job with more than %d steps.", r.Ref, mostBuildsSteps)
				}
			}
			runs = append(runs, r)
		}
		days[d.Day] = runs
	}
	return days, nil
}

// MARK: Reads

// keptBuilds is the copies of one repository from the machines in a scope: every run, once
// for each ref and attempt, oldest first, and what the machines said of it.
type keptBuilds struct {
	runs []citimings.Run
	// tags names what was read, so the change points are found again only when it moved.
	tags string
	def  string
	// readAt is the newest last read; filledFrom the oldest reach.
	readAt, filledFrom *time.Time
}

// buildsChangesCache holds each repository's change points as last found, by what was read.
type buildsChangesCache struct {
	mu   sync.Mutex
	held map[string]buildsChangesFound
}

type buildsChangesFound struct {
	tags    string
	changes []citimings.Change
}

// mostBuildsChangesHeld bounds the repositories whose change points are held.
const mostBuildsChangesHeld = 64

func (c *buildsChangesCache) of(repo string, kept keptBuilds) []citimings.Change {
	c.mu.Lock()
	found, ok := c.held[repo]
	c.mu.Unlock()
	if ok && found.tags == kept.tags {
		return found.changes
	}
	changes := citimings.Changes(kept.runs)
	c.mu.Lock()
	if c.held == nil || len(c.held) >= mostBuildsChangesHeld {
		c.held = map[string]buildsChangesFound{}
	}
	c.held[repo] = buildsChangesFound{kept.tags, changes}
	c.mu.Unlock()
	return changes
}

func (m *Methods) buildsReadMethod(ctx context.Context, method string, params json.RawMessage, from Caller) (any, error) {
	user := m.s.actorFor(from.Client).User
	if method == wire.CallBuildsHistoryAll.Name {
		return m.buildsHistoryAll(ctx, params, user)
	}
	return m.buildsRunAll(ctx, params, user)
}

// buildsHistoryAll is builds/historyAll: the history of a repository from the scope's copies.
func (m *Methods) buildsHistoryAll(ctx context.Context, params json.RawMessage, user string) (any, error) {
	request, err := require[wire.BuildsHistoryRequest](params, "BuildsHistoryRequest")
	if err != nil {
		return nil, err
	}
	a, err := citimings.ReadHistoryAsk(request)
	if err != nil {
		return nil, fail(invalidParams, "builds/historyAll: %v.", err)
	}
	kept, err := m.s.scopeBuilds(ctx, request.Scope, user, a.Repo)
	if err != nil {
		return nil, err
	}
	out := citimings.EmptyHistory(a.Repo)
	if !citimings.Reads(a.Repo.Forge) {
		return out, nil
	}
	out.Reader = a.Repo.Forge
	if kept.def != "" {
		out.DefaultBranch = new(kept.def)
	}
	if kept.readAt != nil {
		out.ReadAt = new(wire.DateOf(*kept.readAt))
	}
	if kept.filledFrom != nil {
		out.FilledFrom = new(wire.DateOf(*kept.filledFrom))
	}
	runs := runsMadeBetween(kept.runs, a.From, a.To)
	return citimings.History(out, a, runs, m.s.buildsChanges.of(a.Repo.String(), kept), nil), nil
}

// buildsRunAll is builds/runAll: one attempt of a run from the scope's copies, with the runs
// made near it.
func (m *Methods) buildsRunAll(ctx context.Context, params json.RawMessage, user string) (any, error) {
	request, err := require[wire.BuildsRunRequest](params, "BuildsRunRequest")
	if err != nil {
		return nil, err
	}
	repo, err := citimings.ParseRepo(request.Repo)
	if err != nil {
		return nil, fail(invalidParams, "builds/runAll: %v.", err)
	}
	kept, err := m.s.scopeBuilds(ctx, request.Scope, user, repo)
	if err != nil {
		return nil, err
	}
	var attempts []citimings.Run
	for _, r := range kept.runs {
		if r.Ref == request.Ref {
			attempts = append(attempts, r)
		}
	}
	want := 1
	if request.Attempt != nil {
		want = int(*request.Attempt)
	}
	out, ok, err := citimings.RunAnswer(attempts, want, func(run citimings.Run) ([]citimings.Run, error) {
		from, to := citimings.NearFrom(run)
		return runsMadeBetween(kept.runs, from, to), nil
	})
	if !ok {
		return nil, fail(invalidParams, "No attempt %d of %s.", want, request.Ref)
	}
	return out, err
}

// runsMadeBetween is the runs made from from up to (not at) to, in their order.
func runsMadeBetween(runs []citimings.Run, from, to time.Time) []citimings.Run {
	var out []citimings.Run
	for _, r := range runs {
		if t := r.MadeAt(); !t.Before(from) && t.Before(to) {
			out = append(out, r)
		}
	}
	return out
}

// scopeBuilds is the copies of repo from the machines whose people the scope takes.
func (s *Service) scopeBuilds(ctx context.Context, scope *wire.ReportScope, user string, repo citimings.Repo) (keptBuilds, error) {
	_, keep, err := s.reportScope(ctx, scope, user, s.isAdmin(user))
	if err != nil {
		return keptBuilds{}, err
	}
	listed, err := s.cfg.Store.List(ctx, buildsPrefix)
	if err != nil {
		return keptBuilds{}, fail(storeUnavailable, "The build timings could not be read: %v", err)
	}
	known := map[string]string{}
	for _, h := range s.records.Hosts() {
		known[strings.ToLower(h.ID)] = h.ID
	}
	read, tags := s.scopeBuildsKeys(listed, repo, known, keep)
	objects, err := s.storeReads.read(ctx, s.cfg.Store, buildsPrefix, listed, read)
	if err != nil {
		return keptBuilds{}, fail(storeUnavailable, "The build timings could not be read: %v", err)
	}
	out := keptBuilds{tags: tags}
	for i, data := range objects {
		if data == nil {
			continue
		}
		if strings.HasSuffix(read[i].Key, "/"+buildsMeta) {
			var meta storedBuildsMeta
			if json.Unmarshal(data, &meta) == nil {
				out.addMeta(meta)
			}
			continue
		}
		var day storedBuildsDay
		if json.Unmarshal(data, &day) == nil {
			out.runs = append(out.runs, day.Runs...)
		}
	}
	out.runs = citimings.Once(out.runs)
	return out, nil
}

// scopeBuildsKeys is the objects of repo, among listed, whose machine's person keep takes,
// and the tags that name them. known maps a machine's lowercase name to its name.
func (s *Service) scopeBuildsKeys(listed []store.Key, repo citimings.Repo, known map[string]string, keep func(person string) bool) ([]store.Key, string) {
	tail := "/" + repo.Forge + "/" + repo.Path + "/"
	var read []store.Key
	var tags strings.Builder
	for _, k := range listed {
		host, rest, ok := strings.Cut(strings.TrimPrefix(k.Key, buildsPrefix), "/")
		if !ok || !strings.HasPrefix("/"+rest, tail) || strings.Contains(strings.TrimPrefix("/"+rest, tail), "/") {
			continue
		}
		// A machine removed since: its person is not known, so only an admin sees it.
		person := ""
		if id := known[host]; id != "" {
			person = s.ownerOf(id)
		}
		if keep(person) {
			read = append(read, k)
			tags.WriteString(k.Key + "\t" + k.ETag + "\n")
		}
	}
	return read, tags.String()
}

// addMeta merges one machine's meta.json in: the first default branch, the newest last read
// and the oldest reach.
func (k *keptBuilds) addMeta(meta storedBuildsMeta) {
	if k.def == "" {
		k.def = meta.DefaultBranch
	}
	if meta.ReadAt != nil && (k.readAt == nil || meta.ReadAt.After(*k.readAt)) {
		k.readAt = meta.ReadAt
	}
	if meta.FilledFrom != nil && (k.filledFrom == nil || meta.FilledFrom.Before(*k.filledFrom)) {
		k.filledFrom = meta.FilledFrom
	}
}

// MARK: The sweep

// sweepBuildTimings deletes the objects of machines removed a day since, and applies the
// bounds to the copies of machines that stopped sending, once a day each; at most most
// deletions and writes. A write is only over the object it read: a machine that wrote since
// keeps what it wrote.
func sweepBuildTimings(ctx context.Context, st store.Store, rs *records.Records, now time.Time, most int) (int, error) {
	listed, err := st.List(ctx, buildsPrefix)
	if err != nil {
		return 0, err
	}
	known := map[string]bool{}
	for _, h := range rs.Hosts() {
		known[strings.ToLower(h.ID)] = true
	}
	dirs, byDir := buildsDirs(listed)
	n := 0
	for _, dir := range dirs {
		if n >= most {
			break
		}
		if n, err = sweepBuildsDir(ctx, st, known, dir, byDir[dir], now, n, most); err != nil {
			return n, err
		}
	}
	return n, nil
}

// buildsDirs is each copy's objects, by its folder, and the folders in the order listed.
func buildsDirs(listed []store.Key) ([]string, map[string][]store.Key) {
	var dirs []string
	byDir := map[string][]store.Key{}
	for _, k := range listed {
		i := strings.LastIndex(k.Key, "/")
		dir := k.Key[:i+1]
		if _, ok := byDir[dir]; !ok {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], k)
	}
	return dirs, byDir
}

// sweepBuildsDir sweeps one copy's folder: it deletes the copy of a machine removed a day
// since, or applies the bounds to a copy that stopped sending. n is the count of deletions
// and writes so far, and the new count is returned.
func sweepBuildsDir(ctx context.Context, st store.Store, known map[string]bool, dir string, keys []store.Key, now time.Time, n, most int) (int, error) {
	host, _, _ := strings.Cut(strings.TrimPrefix(dir, buildsPrefix), "/")
	var meta storedBuildsMeta
	metaTag := ""
	object, err := st.Get(ctx, dir+buildsMeta)
	if err != nil {
		return n, err
	}
	hasMeta := object != nil && json.Unmarshal(object.Data, &meta) == nil
	if object != nil {
		metaTag = object.ETag
	}
	if !known[host] {
		// A machine another copy has just added is not in these records yet: only a copy
		// a day old goes.
		if hasMeta && meta.At.After(now.Add(-24*time.Hour)) {
			return n, nil
		}
		return deleteBuildsKeys(ctx, st, keys, n, most)
	}
	if !hasMeta || meta.At.After(now.Add(-buildsStopped)) || meta.SweptAt != nil && meta.SweptAt.After(now.Add(-buildsSweepEvery)) {
		return n, nil
	}
	return sweepStoppedBuilds(ctx, st, dir, keys, meta, metaTag, now, n)
}

// deleteBuildsKeys deletes keys until the count n reaches most, and is the new count.
func deleteBuildsKeys(ctx context.Context, st store.Store, keys []store.Key, n, most int) (int, error) {
	for _, k := range keys {
		if n >= most {
			return n, nil
		}
		if err := st.Delete(ctx, k.Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// sweepStoppedBuilds applies the bounds to the days of a copy that stopped sending and marks
// its meta.json swept, only over the meta.json it read. It is the new count.
func sweepStoppedBuilds(ctx context.Context, st store.Store, dir string, keys []store.Key, meta storedBuildsMeta, metaTag string, now time.Time, n int) (int, error) {
	days := &objectDays{ctx: ctx, st: st, dir: dir, keys: keys, now: now, tags: map[string]string{}}
	_, err := citimings.SweepDays(days, now)
	n += days.writes
	if err != nil {
		return n, err
	}
	meta.SweptAt = new(records.At(now))
	data, err := records.Encode(meta)
	if err != nil {
		return n, err
	}
	if _, err := st.Put(ctx, dir+buildsMeta, data, store.Matching(metaTag)); err != nil && !isConflict(err) {
		return n, err
	}
	n++
	return n, nil
}

func isConflict(err error) bool {
	var conflict *store.ConflictError
	return errors.As(err, &conflict)
}

// objectDays is one copy's day objects as citimings.Days. A write that finds the object
// changed since it was read is left out: the machine sent it since.
type objectDays struct {
	ctx    context.Context
	st     store.Store
	dir    string
	keys   []store.Key
	now    time.Time
	tags   map[string]string
	writes int
}

func (o *objectDays) List() ([]string, error) {
	var days []string
	for _, k := range o.keys {
		day, ok := strings.CutSuffix(strings.TrimPrefix(k.Key, o.dir), ".json")
		if ok && citimings.DayOK(day) {
			days = append(days, day)
		}
	}
	return days, nil
}

func (o *objectDays) Read(day string) ([]citimings.Run, error) {
	object, err := o.st.Get(o.ctx, o.dir+day+".json")
	if err != nil || object == nil {
		return nil, err
	}
	o.tags[day] = object.ETag
	var kept storedBuildsDay
	if json.Unmarshal(object.Data, &kept) != nil {
		return nil, nil
	}
	return kept.Runs, nil
}

func (o *objectDays) Write(day string, runs []citimings.Run) error {
	data, err := encodeBuildsDay(runs, o.now)
	if err != nil {
		return err
	}
	when := store.Always
	if tag, ok := o.tags[day]; ok {
		when = store.Matching(tag)
	}
	tag, err := o.st.Put(o.ctx, o.dir+day+".json", data, when)
	if isConflict(err) {
		return nil
	}
	if err == nil {
		o.tags[day] = tag
		o.writes++
	}
	return err
}

func (o *objectDays) Remove(day string) error {
	o.writes++
	return o.st.Delete(o.ctx, o.dir+day+".json")
}
