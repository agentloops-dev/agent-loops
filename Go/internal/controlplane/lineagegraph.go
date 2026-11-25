package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The merged copy of lineage (#4273, #4275): each machine sends its links and its sessions'
// changes of state (lineage/sync) a few seconds after they change, and the control plane
// keeps them per machine, under v1/lineage-graph/<host>/. Only that machine sends its
// objects; a read merges them (#4276). Only refs, states and times: no titles or text
// (principle 7).
//
//   - links.json and states.json are the machine's links and states as of the last roll-up.
//   - changes/<time>-<id>.json is one send's links and states since then (#6584), so one send
//     writes what it brought, not the machine's whole copy: a read merges them in, in order,
//     over the two files. A send that finds mostLineageChanges of them merges them into
//     one change (#6863), which costs what changed and not what the machine holds. It rolls
//     them up into the two files only when they come to lineageRollUpBytes, and the hourly
//     sweep rolls them up too. A read merges at most mostLineageChanges.
//   - Links are bounded as the machine bounds its own file (lineage.Bound): an ended link
//     90 days after its end, at most lineage.MostLinks a machine.
//   - States are each change with its time, bounded as the machine bounds its own (#4274):
//     the last 90 days and each session's latest whatever its age, at most
//     lineage.MostStates a machine, those that are not a session's latest going first.
//   - The hourly sweep rolls up a machine that stopped sending, applies both bounds, and
//     deletes a removed machine's objects.
//
// A roll-up writes the two files only over the objects it read, and then deletes the changes
// it merged, oldest first: a change merged twice is the same answer, as each link is put over
// its copy and each state is kept once.

const (
	lineageGraphPrefix = "v1/lineage-graph/"
	// mostSyncLinks and mostSyncStates bound one send.
	mostSyncLinks  = 2_000
	mostSyncStates = 1_000
	// mostLineageChanges bounds the changes a machine has that are not rolled up yet.
	mostLineageChanges = 32
	// lineageRollUpBytes is the changes' size from which they are rolled up. The two files
	// at their bounds are about 11 MB: a roll-up of a few small changes cost the same as one
	// of thousands, and used the live control plane's whole CPU (#7052).
	lineageRollUpBytes = 256 << 10
)

// storedLinks is one machine's links as the store keeps them, oldest start first.
type storedLinks struct {
	Links []wire.LineageLink `json:"links"`
	At    records.Stamp      `json:"at"`
}

// storedStates is one machine's state changes, oldest first.
type storedStates struct {
	States []wire.LineageStateChange `json:"states"`
	At     records.Stamp             `json:"at"`
}

// storedChanges is one send's links and states, until a roll-up merges them in.
type storedChanges struct {
	Links  []wire.LineageLink        `json:"links,omitempty"`
	States []wire.LineageStateChange `json:"states,omitempty"`
	At     records.Stamp             `json:"at"`
}

func lineageHostPrefix(host string) string {
	return lineageGraphPrefix + strings.ToLower(host) + "/"
}

func lineageLinksKey(host string) string {
	return lineageHostPrefix(host) + "links.json"
}

func lineageStatesKey(host string) string {
	return lineageHostPrefix(host) + "states.json"
}

func lineageChangesPrefix(host string) string {
	return lineageHostPrefix(host) + "changes/"
}

// lineageChangeKey names a change in the order of its time, with an id so that two copies
// never write the same one.
func lineageChangeKey(host string, now time.Time) string {
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	return fmt.Sprintf("%s%020d-%s.json", lineageChangesPrefix(host), now.UnixNano(), hex.EncodeToString(id))
}

var sessionStates = map[wire.LineageSessionStateName]bool{
	wire.LineageSessionStateNameWorking: true, wire.LineageSessionStateNameWaiting: true, wire.LineageSessionStateNameNeedsYou: true,
	wire.LineageSessionStateNameStopped: true, wire.LineageSessionStateNameArchived: true, wire.LineageSessionStateNameDeleted: true,
}

// keepLineageSync is lineage/sync: the links over this machine's copies of them, and the
// state changes after its own. Full says the control plane has no copy of its links.
func (s *Service) keepLineageSync(ctx context.Context, host string, sync wire.LineageSync, now time.Time) (wire.LineageSyncAnswer, error) {
	if s.records.Host(host) == nil {
		return wire.LineageSyncAnswer{}, fail(noSuchHost, "No machine is called %s.", host)
	}
	if len(sync.Links) > mostSyncLinks || len(sync.States) > mostSyncStates {
		return wire.LineageSyncAnswer{}, fail(invalidParams, "Send at most %d links and %d states at a time.", mostSyncLinks, mostSyncStates)
	}
	for _, l := range sync.Links {
		if err := checkSyncLink(l); err != nil {
			return wire.LineageSyncAnswer{}, fail(invalidParams, "A lineage link: %v.", err)
		}
	}
	for _, c := range sync.States {
		if c.Node.Kind != lineage.KindSession || lineage.CheckNode(c.Node) != nil {
			return wire.LineageSyncAnswer{}, fail(invalidParams, "A state is a session node's, not %s %s.", c.Node.Kind, c.Node.Ref)
		}
		if !sessionStates[c.State] {
			return wire.LineageSyncAnswer{}, fail(invalidParams, "%q is not a session's state.", c.State)
		}
	}
	reset := sync.Reset != nil && *sync.Reset
	full, err := s.keepSyncChanges(ctx, host, sync.Links, sync.States, reset, now)
	if err != nil {
		return wire.LineageSyncAnswer{}, err
	}
	if len(sync.Links) > 0 && !full || len(sync.States) > 0 {
		kept := sync
		if full {
			kept.Links = nil
		}
		s.lineageChanged(host, kept)
	}
	if full {
		t := true
		return wire.LineageSyncAnswer{Full: &t}, nil
	}
	return wire.LineageSyncAnswer{}, nil
}

func checkSyncLink(l wire.LineageLink) error {
	if err := lineage.CheckNode(l.From); err != nil {
		return err
	}
	if err := lineage.CheckNode(l.To); err != nil {
		return err
	}
	if err := lineage.CheckWord("rel", l.Rel); err != nil {
		return err
	}
	return lineage.CheckWord("source", l.Source)
}

// syncLinkKey is a link apart from its end: the same from, to and rel can start again
// after it ended.
type syncLinkKey struct {
	from, to wire.LineageNode
	rel      string
	at       wire.Date
}

// keepSyncLinks puts links over the machine's copies of them, or in place of them all with
// reset. It says full when there is no copy to put them over.
func (s *Service) keepSyncLinks(ctx context.Context, host string, links []wire.LineageLink, reset bool, now time.Time) (bool, error) {
	return s.keepSyncChanges(ctx, host, links, nil, reset, now)
}

// keepSyncChanges keeps one send: links over the machine's copies of them, or in place of
// them all with reset, and the changes of state. It says full when there is no copy of the
// links to put them over; the states are kept all the same. Without reset it writes only what
// was sent, as a change; with reset, or past mostLineageChanges, it merges them.
func (s *Service) keepSyncChanges(ctx context.Context, host string, links []wire.LineageLink, states []wire.LineageStateChange, reset bool, now time.Time) (bool, error) {
	if reset {
		if links == nil {
			links = []wire.LineageLink{}
		}
		return false, s.rollUpLineageSoon(ctx, host, links, states, now)
	}
	keys, err := s.cfg.Store.List(ctx, lineageHostPrefix(host))
	if err != nil {
		return false, err
	}
	full := true
	var changes []store.Key
	for _, k := range keys {
		switch {
		case k.Key == lineageLinksKey(host):
			full = false
		case strings.HasPrefix(k.Key, lineageChangesPrefix(host)):
			changes = append(changes, k)
		}
	}
	if full {
		// Nothing to put a change over: the machine sends all it has, the first with reset.
		// What it sent now comes again with it.
		links = nil
	}
	if len(links) == 0 && len(states) == 0 {
		return full, nil
	}
	data, err := records.Encode(storedChanges{Links: links, States: states, At: records.At(now)})
	if err != nil {
		return false, err
	}
	if _, err := s.cfg.Store.Put(ctx, lineageChangeKey(host, now), data, store.Absent); err != nil {
		return false, err
	}
	if len(changes)+1 < mostLineageChanges {
		return full, nil
	}
	size := int64(len(data))
	for _, k := range changes {
		size += max(k.Size, 0)
	}
	if size < lineageRollUpBytes {
		// The send's own change stays apart: the next merge takes it.
		return full, compactLineageChanges(ctx, s.cfg.Store, host, changes, now)
	}
	// The send is kept: a roll-up that loses to another is the next one's.
	if err := s.rollUpLineageSoon(ctx, host, nil, nil, now); err != nil && !store.IsConflict(err) {
		return full, err
	}
	return full, nil
}

// compactLineageChanges merges a machine's changes (keys, oldest first) into one, each link
// and each state once, and deletes them, oldest first. The one goes just before the last of
// them, so a stop part way leaves changes that merge to the same answer. A roll-up at the
// same time is safe: it deletes only what it read, and mergeLinks starts no ended link again.
func compactLineageChanges(ctx context.Context, st store.Store, host string, keys []store.Key, now time.Time) error {
	var one storedChanges
	for _, k := range keys {
		object, err := st.Get(ctx, k.Key)
		if err != nil {
			return err
		}
		var change storedChanges
		if object == nil || json.Unmarshal(object.Data, &change) != nil {
			continue
		}
		one.Links = append(one.Links, change.Links...)
		one.States = append(one.States, change.States...)
	}
	one.Links = mergeLinks(nil, one.Links)
	one.States = boundStates(one.States, now)
	one.At = records.At(now)
	if len(one.Links) > 0 || len(one.States) > 0 {
		data, err := records.Encode(one)
		if err != nil {
			return err
		}
		id := make([]byte, 4)
		_, _ = rand.Read(id)
		// "-" sorts before ".": before the last one, after the rest.
		key := fmt.Sprintf("%s-%s.json", strings.TrimSuffix(keys[len(keys)-1].Key, ".json"), hex.EncodeToString(id))
		if _, err := st.Put(ctx, key, data, store.Absent); err != nil {
			return err
		}
	}
	for _, k := range keys {
		if err := st.Delete(ctx, k.Key); err != nil {
			return err
		}
	}
	return nil
}

// rollUpLineageSoon is rollUpLineage, again while another copy's roll-up (or the sweep's)
// wins over the objects it read.
func (s *Service) rollUpLineageSoon(ctx context.Context, host string, reset []wire.LineageLink, states []wire.LineageStateChange, now time.Time) error {
	var err error
	for range 3 {
		if _, err = rollUpLineage(ctx, s.cfg.Store, host, reset, states, now); !store.IsConflict(err) {
			return err
		}
	}
	return err
}

// rollUpLineage merges a machine's changes into its links.json and states.json, bounded at
// now, over the objects it read, then deletes the changes, oldest first. With reset (not nil)
// the links are reset in place of them all, and the changes' links are older than it; states
// are more to add. It says whether it wrote, and a ConflictError when another wrote first.
func rollUpLineage(ctx context.Context, st store.Store, host string, reset []wire.LineageLink, states []wire.LineageStateChange, now time.Time) (bool, error) {
	changeKeys, err := st.List(ctx, lineageChangesPrefix(host))
	if err != nil {
		return false, err
	}
	if reset == nil && len(states) == 0 && len(changeKeys) == 0 {
		return false, nil
	}
	linksObject, err := st.Get(ctx, lineageLinksKey(host))
	if err != nil {
		return false, err
	}
	statesObject, err := st.Get(ctx, lineageStatesKey(host))
	if err != nil {
		return false, err
	}
	var keptLinks storedLinks
	linksRead := linksObject != nil && json.Unmarshal(linksObject.Data, &keptLinks) == nil
	var keptStates storedStates
	if statesObject != nil {
		// What cannot be read starts again from what was sent.
		_ = json.Unmarshal(statesObject.Data, &keptStates)
	}
	if reset != nil {
		keptLinks, linksRead = storedLinks{}, true
		keptLinks.Links = mergeLinks(nil, reset)
	}
	var merged []string
	// The changes' links are merged once, after the loop: a merge per change rebuilt its map
	// over every kept link and sorted them all again, the control plane's largest allocation
	// on the live heap (#6597).
	var changed []wire.LineageLink
	for _, k := range changeKeys {
		object, err := st.Get(ctx, k.Key)
		if err != nil {
			return false, err
		}
		merged = append(merged, k.Key)
		var change storedChanges
		if object == nil || json.Unmarshal(object.Data, &change) != nil {
			continue
		}
		if reset == nil && linksRead {
			changed = append(changed, change.Links...)
		}
		keptStates.States = append(keptStates.States, change.States...)
	}
	keptLinks.Links = mergeLinks(keptLinks.Links, changed)
	keptStates.States = boundStates(append(keptStates.States, states...), now)
	keptStates.At = records.At(now)
	if err := putOver(ctx, st, lineageStatesKey(host), statesObject, keptStates); err != nil {
		return false, err
	}
	switch {
	case linksRead:
		keptLinks.Links = lineage.Bound(keptLinks.Links, now)
		if keptLinks.Links == nil {
			keptLinks.Links = []wire.LineageLink{}
		}
		keptLinks.At = records.At(now)
		if err := putOver(ctx, st, lineageLinksKey(host), linksObject, keptLinks); err != nil {
			return false, err
		}
	case linksObject != nil:
		// Links that cannot be read: the next send is told full, and sends them all again.
		if err := st.Delete(ctx, lineageLinksKey(host)); err != nil {
			return false, err
		}
	}
	for _, k := range merged {
		if err := st.Delete(ctx, k); err != nil {
			return true, err
		}
	}
	return true, nil
}

// putOver writes v at key over the object read (nil: none was).
func putOver(ctx context.Context, st store.Store, key string, read *store.Object, v any) error {
	// Compact, and not through records.Encode: its sorted, indented form took three more
	// copies of megabytes and twice the bytes (#6863). Any JSON reader reads it.
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	when := store.Absent
	if read != nil {
		when = store.Matching(read.ETag)
	}
	_, err = st.Put(ctx, key, data, when)
	return err
}

// mergeLinks puts links over kept's copies of them, and the rest after, oldest start first.
// A link that ended stays ended: a new start is a new link.
func mergeLinks(kept, links []wire.LineageLink) []wire.LineageLink {
	if len(links) == 0 {
		return kept
	}
	at := make(map[syncLinkKey]int, len(kept)+len(links))
	for i, l := range kept {
		at[syncLinkKey{l.From, l.To, l.Rel, l.At}] = i
	}
	for _, l := range links {
		k := syncLinkKey{l.From, l.To, l.Rel, l.At}
		if i, ok := at[k]; ok {
			if l.EndedAt != nil || kept[i].EndedAt == nil {
				kept[i] = l
			}
			continue
		}
		at[k] = len(kept)
		kept = append(kept, l)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].At < kept[j].At })
	return kept
}

// boundStates is the changes oldest first, each once, within the bounds at now, as the
// machine bounds its own (lineage.Files.pruneStates): the last lineage.KeepEnded, and each
// session's latest whatever its age; past lineage.MostStates, the oldest that are not a
// session's latest go first, then the oldest.
func boundStates(changes []wire.LineageStateChange, now time.Time) []wire.LineageStateChange {
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].At < changes[j].At })
	seen := map[wire.LineageStateChange]bool{}
	once := changes[:0:0]
	for _, c := range changes {
		if !seen[c] {
			seen[c] = true
			once = append(once, c)
		}
	}
	latest := map[wire.LineageNode]int{}
	for i, c := range once {
		latest[c.Node] = i
	}
	oldest := wire.DateOf(now.Add(-lineage.KeepEnded))
	gone := map[int]bool{}
	for i, c := range once {
		if c.At < oldest && latest[c.Node] != i {
			gone[i] = true
		}
	}
	over := len(once) - len(gone) - lineage.MostStates
	for i := 0; i < len(once) && over > 0; i++ {
		if !gone[i] && latest[once[i].Node] != i {
			gone[i] = true
			over--
		}
	}
	for i := 0; i < len(once) && over > 0; i++ {
		if !gone[i] {
			gone[i] = true
			over--
		}
	}
	kept := make([]wire.LineageStateChange, 0, len(once)-len(gone))
	for i, c := range once {
		if !gone[i] {
			kept = append(kept, c)
		}
	}
	return kept
}

// sweepLineageGraph deletes the objects of machines removed since, rolls up each machine's
// changes, and bounds the rest again, so a machine that stopped sending loses its old links
// and states too; at most most deletions, roll-ups and writes. A write is only over the
// object it read: a machine that wrote since keeps what it wrote.
func sweepLineageGraph(ctx context.Context, st store.Store, rs *records.Records, now time.Time, most int) (int, error) {
	keys, err := st.List(ctx, lineageGraphPrefix)
	if err != nil {
		return 0, err
	}
	known := map[string]bool{}
	for _, h := range rs.Hosts() {
		known[strings.ToLower(h.ID)] = true
	}
	rolled := map[string]bool{}
	n := 0
	for _, k := range keys {
		if n >= most {
			break
		}
		host, name, ok := strings.Cut(strings.TrimPrefix(k.Key, lineageGraphPrefix), "/")
		if !ok || rolled[host] {
			continue
		}
		if known[host] && strings.HasPrefix(name, "changes/") {
			// The changes sort before links.json and states.json, which the roll-up bounds.
			rolled[host] = true
			done, err := rollUpLineage(ctx, st, host, nil, nil, now)
			if store.IsConflict(err) {
				continue
			}
			if err != nil {
				return n, err
			}
			if done {
				n++
			}
			continue
		}
		done, err := sweepLineageObject(ctx, st, known, k, now)
		if err != nil {
			return n, err
		}
		if done {
			n++
		}
	}
	return n, nil
}

// sweepLineageObject deletes one object of a machine removed a day since, or bounds it again
// over the object it read. It says whether it deleted or wrote.
func sweepLineageObject(ctx context.Context, st store.Store, known map[string]bool, k store.Key, now time.Time) (bool, error) {
	host, name, ok := strings.Cut(strings.TrimPrefix(k.Key, lineageGraphPrefix), "/")
	if !ok {
		return false, nil
	}
	object, err := st.Get(ctx, k.Key)
	if err != nil || object == nil {
		return false, err
	}
	var stamp struct {
		At records.Stamp `json:"at"`
	}
	readable := json.Unmarshal(object.Data, &stamp) == nil
	if !known[host] {
		// A machine another copy has just added is not in these records yet: only an
		// object a day old goes.
		if readable && stamp.At.After(now.Add(-24*time.Hour)) {
			return false, nil
		}
		if err := st.Delete(ctx, k.Key); err != nil {
			return false, err
		}
		return true, nil
	}
	data, changed, err := reboundLineageObject(name, object.Data, now)
	if err != nil || !changed {
		return false, err
	}
	if _, err := st.Put(ctx, k.Key, data, store.Matching(object.ETag)); err != nil {
		var conflict *store.ConflictError
		if errors.As(err, &conflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// reboundLineageObject is a machine's links.json or states.json (by name) bounded again, and
// whether the bounds took anything out. An object it cannot read is left as it is.
func reboundLineageObject(name string, raw []byte, now time.Time) ([]byte, bool, error) {
	switch name {
	case "links.json":
		var kept storedLinks
		if json.Unmarshal(raw, &kept) != nil {
			return nil, false, nil
		}
		bounded := lineage.Bound(kept.Links, now)
		if len(bounded) == len(kept.Links) {
			return nil, false, nil
		}
		kept.Links = bounded
		if kept.Links == nil {
			kept.Links = []wire.LineageLink{}
		}
		data, err := records.Encode(kept)
		return data, true, err
	case "states.json":
		var kept storedStates
		if json.Unmarshal(raw, &kept) != nil {
			return nil, false, nil
		}
		before := len(kept.States)
		kept.States = boundStates(kept.States, now)
		if len(kept.States) == before {
			return nil, false, nil
		}
		data, err := records.Encode(kept)
		return data, true, err
	}
	return nil, false, nil
}
