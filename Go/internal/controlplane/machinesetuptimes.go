package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The durations of finished set-up steps, for "Slower than usual" (#6760): a time series for
// each step, all in one object, as a machine's row reads them. It does not roll up: a day's
// sum and count has no median, and 50 a step is small as it is.

const (
	// store-bounds: kind=time series; size=32KiB (50 points a step of 80 B, 8 steps); age=90d; applied=on write and hourly sweep
	machineSetupTimes = "v1/machine-setup-times.json"
	// mostSetupTimes bounds the durations kept for each step: the oldest go first.
	mostSetupTimes = 50
	setupTimesKept = 90 * 24 * time.Hour
	// setupTimesAhead allows for the clock of another copy.
	setupTimesAhead = time.Hour
)

// setupTime is one step's duration as kept, with when the step ended. Those kept before
// #6760 have no time, so their age is not known: they are not read, and the next write or
// sweep drops them.
type setupTime struct {
	At      time.Time `json:"at"`
	Seconds float64   `json:"seconds"`
}

type setupTimeLists map[wire.MachineSetupStepID][]setupTime

// setupTimeSeries is the set-up times as their kind. store.OpenTimeSeries does not fit: it
// keeps an object for each point and rolls each day up to a sum after 7 days.
type setupTimeSeries struct {
	store store.Store
}

var _ store.TimeSeries = setupTimeSeries{}

// read is the times, and the object they are in (nil for none).
func (t setupTimeSeries) read(ctx context.Context) (setupTimeLists, *store.Object, error) {
	object, err := t.store.Get(ctx, machineSetupTimes)
	if err != nil {
		return nil, nil, err
	}
	times := setupTimeLists{}
	if object != nil && (json.Unmarshal(object.Data, &times) != nil || times == nil) {
		times = setupTimeLists{}
	}
	return times, object, nil
}

// bounded keeps, of each step there is, the newest points that are no older than before.
func (times setupTimeLists) bounded(before time.Time) setupTimeLists {
	out := setupTimeLists{}
	for _, id := range setupSteps {
		var list []setupTime
		for _, p := range times[id] {
			if !p.At.Before(before) {
				list = append(list, p)
			}
		}
		slices.SortStableFunc(list, func(a, b setupTime) int { return a.At.Compare(b.At) })
		if over := len(list) - mostSetupTimes; over > 0 {
			list = list[over:]
		}
		if len(list) > 0 {
			out[id] = list
		}
	}
	return out
}

// change writes the times as change makes them, when that differs, again once when another
// write came between.
func (t setupTimeSeries) change(ctx context.Context, change func(setupTimeLists) setupTimeLists) error {
	for try := 0; ; try++ {
		times, object, err := t.read(ctx)
		if err != nil {
			return err
		}
		old, when := "{}", store.Absent
		if object != nil {
			old, when = string(object.Data), store.Matching(object.ETag)
		}
		data, err := json.Marshal(change(times))
		if err != nil {
			return err
		}
		if string(data) == old {
			return nil
		}
		if _, err = t.store.Put(ctx, machineSetupTimes, data, when); err == nil || !isConflict(err) || try > 0 {
			return err
		}
	}
}

// Append keeps a point once: the same one again, as a retry says it, changes nothing.
func (t setupTimeSeries) Append(ctx context.Context, p store.Point) error {
	id := wire.MachineSetupStepID(p.Series)
	if stepIndex(id) < 0 || p.At.IsZero() {
		return errors.New("a set-up time needs a step and a time")
	}
	point := setupTime{At: p.At.UTC(), Seconds: p.Value}
	return t.change(ctx, func(times setupTimeLists) setupTimeLists {
		if !slices.ContainsFunc(times[id], func(had setupTime) bool {
			return had.At.Equal(point.At) && had.Seconds == point.Seconds
		}) {
			times[id] = append(times[id], point)
		}
		return times.bounded(time.Now().Add(-setupTimesKept))
	})
}

// Range is a step's points, the oldest first. All of a step's are one page, so the cursor
// is always "".
func (t setupTimeSeries) Range(ctx context.Context, series string, from, to time.Time, granularity, _ string, limit int) ([]store.Point, string, error) {
	if granularity != "hour" {
		return nil, "", errors.New("the set-up times have no roll-up")
	}
	times, _, err := t.read(ctx)
	if err != nil {
		return nil, "", err
	}
	var out []store.Point
	for _, p := range times[wire.MachineSetupStepID(series)] {
		if len(out) < limit && !p.At.Before(from) && p.At.Before(to) {
			out = append(out, store.Point{Series: series, ID: p.At.Format(time.RFC3339Nano), At: p.At, Value: p.Seconds, Count: 1})
		}
	}
	return out, "", nil
}

// Rollup makes no summary: at before, it deletes the points past their age.
func (t setupTimeSeries) Rollup(ctx context.Context, before time.Time, _ time.Duration) error {
	return t.change(ctx, func(times setupTimeLists) setupTimeLists {
		return times.bounded(before.Add(-setupTimesKept))
	})
}

func (s *Service) setupTimes() store.TimeSeries { return setupTimeSeries{s.cfg.Store} }

// readSetupTimes is the durations of the steps that run now, which alone can be slower than
// usual. A step with none left has no median, so it is not marked.
func (s *Service) readSetupTimes(ctx context.Context, st *storedSetup) map[wire.MachineSetupStepID][]float64 {
	out := map[wire.MachineSetupStepID][]float64{}
	now := time.Now()
	for _, step := range st.Steps {
		if step.State != wire.MachineSetupStateRunning {
			continue
		}
		points, _, err := s.setupTimes().Range(ctx, string(step.ID), now.Add(-setupTimesKept), now.Add(setupTimesAhead), "hour", "", mostSetupTimes)
		if err != nil {
			continue
		}
		for _, p := range points {
			out[step.ID] = append(out[step.ID], p.Value)
		}
	}
	return out
}

// noteSetupTime keeps how long a step took, for the median. A lost write costs one sample.
func (s *Service) noteSetupTime(ctx context.Context, id wire.MachineSetupStepID, took time.Duration) {
	now := time.Now()
	_ = s.setupTimes().Append(ctx, store.Point{Series: string(id), ID: now.UTC().Format(time.RFC3339Nano), At: now, Value: took.Seconds(), Count: 1})
}
