package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func putLink(t *testing.T, st store.Store, identity, user string, used *time.Time) {
	t.Helper()
	r := linkRecord{Channel: "slack", Identity: identity, User: user, ApprovedBy: user, ApprovedAt: records.At(time.Now())}
	if used != nil {
		at := records.At(*used)
		r.UsedAt = &at
	}
	data, err := records.Encode(r)
	must(t, err)
	_, err = st.Put(context.Background(), linkKey("slack", identity), data, store.Always)
	must(t, err)
}

// #6763: a link not used for 90 days goes; one in use stays, and one kept before counts
// from the first sweep.
func TestALinkNotUsedFor90DaysGoes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, owner := p.client("Alex's Mac")
	sam := p.invite(owner, "Sam", wire.UserRoleMember)
	host := p.machine(sam, "Sam's box").member.Host
	now := time.Now()
	old, recent := now.Add(-linkUnusedKept-time.Hour), now.Add(-time.Hour)
	putLink(t, st, "unused", sam.id, &old)
	putLink(t, st, "used", sam.id, &old)
	putLink(t, st, "recent", sam.id, &recent)
	putLink(t, st, "undated", sam.id, nil)

	// A message renews its link, whatever its age; another the same day writes nothing.
	used := wire.ChannelSender{Channel: "slack", Identity: "used"}
	for range 2 {
		if got := checkLink(t, p, host, used); deref(got.User) != sam.id {
			t.Fatalf("%+v", got)
		}
	}
	_, before, err := p.s.links.read(ctx, "slack", "recent")
	must(t, err)
	checkLink(t, p, host, wire.ChannelSender{Channel: "slack", Identity: "recent"})
	if _, after, _ := p.s.links.read(ctx, "slack", "recent"); after != before {
		t.Fatal("a link used an hour ago was written")
	}

	left := func() string {
		every, err := p.s.links.every(ctx)
		must(t, err)
		names := []string{}
		for _, r := range every {
			names = append(names, r.Identity)
		}
		return strings.Join(names, " ")
	}
	must(t, p.s.links.expire(ctx, now, mostPerSweep))
	if got := left(); got != "recent undated used" {
		t.Fatal(got)
	}
	waitNote(t, sam.window, "control/linksChanged")
	must(t, p.s.links.expire(ctx, now.Add(linkUnusedKept-time.Minute), mostPerSweep))
	if got := left(); got != "undated used" {
		t.Fatal(got)
	}
	must(t, p.s.links.expire(ctx, now.Add(linkUnusedKept+time.Hour), 1))
	if got := left(); got != "used" {
		t.Fatal(got)
	}
}

// usedMeanwhile is a store where a link is used between the sweep's read and its write.
type usedMeanwhile struct {
	store.Store
	use func()
}

func (s *usedMeanwhile) Put(ctx context.Context, key string, data []byte, when store.Condition) (string, error) {
	if use := s.use; use != nil && strings.HasPrefix(key, linksPrefix) {
		s.use = nil
		use()
	}
	return s.Store.Put(ctx, key, data, when)
}

func TestALinkUsedWhileTheSweepRemovesItStays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := &usedMeanwhile{Store: store.NewMemory()}
	now := time.Now()
	old := now.Add(-linkUnusedKept - time.Hour)
	putLink(t, st, "U1", "sam", &old)
	st.use = func() { putLink(t, st.Store, "U1", "sam", &now) }
	l := &links{s: &Service{cfg: Config{Store: st}}, now: time.Now}
	must(t, l.expire(ctx, now, mostPerSweep))
	if r, _, err := l.read(ctx, "slack", "U1"); err != nil || r == nil || r.User != "sam" {
		t.Fatalf("%+v, %v", r, err)
	}
}

// #6763: an approval over the limit is refused and its request still waits; a replacement
// needs no more room, and none is evicted.
func TestALinkOverTheLimitIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	_, owner := p.client("Alex's Mac")
	sam := p.invite(owner, "Sam", wire.UserRoleMember)
	host := p.machine(sam, "Sam's box").member.Host
	full := linkKey("slack", "full")
	_, err := st.Put(ctx, full, make([]byte, linksMostBytes), store.Always)
	must(t, err)

	checkLink(t, p, host, wire.ChannelSender{Channel: "slack", Identity: "U1"})
	asked := linkRequests(t, owner)
	call(t, owner, "", "links/approve", wire.ChannelLinkDecide{Request: asked[0].Request, For: &sam.id})
	approve := wire.ChannelLinkDecide{Request: asked[0].Request}
	if e := callError(t, sam.window, "", "links/approve", approve); !strings.Contains(e, "as many channel links as it can") {
		t.Fatal(e)
	}
	if o, _ := st.Get(ctx, full); o == nil {
		t.Fatal("a link was evicted")
	}
	must(t, p.s.links.room(ctx, full, linkMostBytes))
	if err := p.s.links.room(ctx, full, linkMostBytes+1); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatal(err)
	}
	must(t, st.Delete(ctx, full))
	call(t, sam.window, "", "links/approve", approve)
	if got := channelLinks(t, sam.window); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}
