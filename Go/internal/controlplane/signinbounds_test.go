package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/signin/signintest"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/internal/secretrelease"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7249: the largest records of the sign-in, lock and secret stores. A refusal signs nobody
// out and loses no key, and what is over a limit already still opens.

func sizeOf(t *testing.T, st store.Store, key string) int {
	t.Helper()
	object, err := st.Get(t.Context(), key)
	must(t, err)
	if object == nil {
		return 0
	}
	return len(object.Data)
}

func TestACodeHasALargestSizeAndAnOldOneOverItStillWorks(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	trust := func(n int) wire.MachineInstall {
		return wire.MachineInstall{Provider: new(wire.ProviderID("codespaces")), Workspace: new("w"), Trust: new(strings.Repeat("k", n))}
	}
	if _, _, err := p.s.codes.issue(withInstallWorkspace(t.Context(), trust(codeMostBytes-1<<10)), true, false, time.Hour, p.url); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.s.codes.issue(withInstallWorkspace(t.Context(), trust(codeMostBytes)), true, false, time.Hour, p.url); err == nil || !strings.Contains(err.Error(), "at most 16 KiB") {
		t.Fatal(err)
	}
	old, err := records.Encode(storedCode{Purpose: "host", Workspace: &codeWorkspace{Trust: strings.Repeat("k", 2*codeMostBytes)},
		Expires: records.At(time.Now().Add(time.Hour))})
	must(t, err)
	_, err = st.Put(t.Context(), codeKey("OLD"), old, store.Absent)
	must(t, err)
	// Who spends it is refused over 4 KiB, and the code is not spent by that.
	if _, err := p.s.codes.Spend(t.Context(), "OLD", spent{PublicKey: make([]byte, spentMostBytes)}); err == nil {
		t.Fatal("a large spender was kept")
	}
	if _, err := p.s.codes.Stored(t.Context(), "OLD", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.codes.Spend(t.Context(), "OLD", spent{PublicKey: make([]byte, 65)}); err != nil {
		t.Fatal(err)
	}
}

// A request at its largest can be answered, and the answer stays within the largest record.
func TestALockJoinRequestAtItsLargestIsAnswered(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	j := p.s.lockJoins
	now := time.Now()
	r := lockJoinRecord{ID: newJoinID(), Person: "p", Client: "c", Name: strings.Repeat("n", 1000), Created: records.At(now), Expires: records.At(now.Add(time.Minute))}
	if base := len(must1(records.Encode(r))); base < requestCreateBytes {
		r.Name += strings.Repeat("n", requestCreateBytes-base)
	}
	must(t, j.write(t.Context(), r, store.Absent))
	if n := sizeOf(t, st, lockJoinKey(r.ID)); n != requestCreateBytes {
		t.Fatalf("%d bytes", n)
	}
	over := r
	over.ID, over.Name = newJoinID(), r.Name+"n"
	if err := j.write(t.Context(), over, store.Absent); err == nil || sizeOf(t, st, lockJoinKey(over.ID)) != 0 {
		t.Fatalf("kept: %v", err)
	}
	kept, etag, err := j.read(t.Context(), r.ID)
	must(t, err)
	kept.Nonce, kept.Reveal = make([]byte, machinelock.NonceSize), make([]byte, machinelock.NonceSize)
	kept.AnsweredBy, kept.Result = strings.Repeat("A", 36), "approved"
	must(t, j.write(t.Context(), *kept, store.Matching(etag)))
	if n := sizeOf(t, st, lockJoinKey(r.ID)); n > requestMostBytes {
		t.Fatalf("%d bytes answered", n)
	}
	// One from before the limit is answered too.
	old := over
	old.Name = strings.Repeat("n", 2*requestMostBytes)
	data, err := records.Encode(old)
	must(t, err)
	_, err = st.Put(t.Context(), lockJoinKey(old.ID), data, store.Absent)
	must(t, err)
	kept, etag, err = j.read(t.Context(), old.ID)
	must(t, err)
	kept.Result = "denied"
	must(t, j.write(t.Context(), *kept, store.Matching(etag)))
}

func must1[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Over the count, the oldest ended requests go. One that waits never does.
func TestTheOldestEndedRequestsGoOverTheCount(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	now := time.Now()
	ids := make([]string, requestsKeptMost+2)
	for i := range ids {
		ids[i] = newJoinID()
		at := records.At(now.Add(time.Duration(i-len(ids)) * time.Second / 10))
		result := "denied"
		if i == 0 {
			result = "" // the oldest, which waits
		}
		must(t, p.s.lockJoins.write(t.Context(), lockJoinRecord{ID: ids[i], Result: result, Created: at, Expires: records.At(now.Add(time.Minute))}, store.Absent))
		must(t, p.s.signInApprovals.write(t.Context(), signInRequest{ID: ids[i], Result: result, Created: at, Expires: records.At(now.Add(time.Minute))}, store.Absent))
	}
	joins, err := p.s.lockJoins.all(t.Context())
	must(t, err)
	signIns, err := p.s.signInApprovals.all(t.Context())
	must(t, err)
	if len(joins) != requestsKeptMost || len(signIns) != requestsKeptMost {
		t.Fatalf("%d and %d kept", len(joins), len(signIns))
	}
	for _, prefix := range []string{lockJoinsPrefix, signInRequestsPrefix} {
		for i, want := range map[int]bool{0: true, 1: false, 2: false, len(ids) - 1: true} {
			if got := sizeOf(t, st, prefix+ids[i]+".json") > 0; got != want {
				t.Errorf("%s request %d: kept %v", prefix, i, got)
			}
		}
	}
}

func TestLinkRequestsAreAtMostACount(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	p := startPlane(t, st, controllink.NewPrivateKey())
	now := time.Now()
	keep := func(i int) {
		r := linkRequestRecord{ID: newJoinID(), Channel: "slack", Identity: fmt.Sprint("U", i), Result: "denied", Created: records.At(now), Expires: records.At(now.Add(time.Hour))}
		_, err := st.Put(t.Context(), linkRequestKey(r.ID), must1(records.Encode(r)), store.Absent)
		must(t, err)
	}
	for i := range requestsKeptMost - 1 {
		keep(i)
	}
	// The largest sender there is, at the last place.
	escaped := strings.Repeat("<", 320)
	must(t, p.s.links.ask(t.Context(), wire.ChannelSender{Channel: "slack", Identity: escaped, Name: new(strings.Repeat("<", 80)), Email: &escaped}))
	must(t, p.s.links.ask(t.Context(), wire.ChannelSender{Channel: "slack", Identity: "one more"}))
	p.s.links.readAgain()
	all, err := p.s.links.all(t.Context())
	must(t, err)
	if len(all) != requestsKeptMost {
		t.Fatalf("%d requests", len(all))
	}
	for _, r := range all {
		if n := sizeOf(t, st, linkRequestKey(r.ID)); n > requestMostBytes {
			t.Fatalf("%d bytes", n)
		}
	}
}

// A refresh token over the largest is not kept at a sign-in, which still works. A renewed one is
// kept whatever its size: the old one is spent, and would suspend its person.
func TestARefreshTokenHasALargestSizeAndNeverSignsAnyoneOut(t *testing.T) {
	t.Parallel()
	prov := signintest.NewProvider(t, "RS256", "")
	p, _, owner, _ := orgPlane(t, prov, nil)
	addOrg(t, owner, prov, wire.OrgProviderJoinAnyone)
	si := p.s.signIns
	if si.sealTicketRefresh(t.Context(), "k", strings.Repeat("r", refreshTokenMost+1)) != nil {
		t.Fatal("kept one over the largest")
	}
	sealed := si.sealTicketRefresh(t.Context(), "k", strings.Repeat("r", refreshTokenMost))
	if sealed == nil {
		t.Fatal("refused one at the largest")
	}
	// A ticket with it, and the longest account a person's record can hold, fits.
	ticket, err := records.Encode(orgTicket{Account: strings.Repeat("a", 16<<10), Refresh: sealed})
	must(t, err)
	if len(ticket) > oidcTicketMostBytes {
		t.Fatalf("a ticket is %d bytes", len(ticket))
	}
	must(t, si.writeRefresh(t.Context(), "p", "acme-okta", strings.Repeat("r", refreshTokenMost), records.Now(), nil, store.Always))
	if n := sizeOf(t, p.store, refreshKey("p", "acme-okta")); n > 32<<10 {
		t.Fatalf("a record is %d bytes", n)
	}
	b := &browser{prov: prov}
	prov.SignInAs(signintest.Claims{"sub": "00u-kim", "email": "kim@acme.test", "name": "Kim"})
	kim, err := p.signInOrg(t, b)
	must(t, err)
	id := personOf(t, p, kim)
	must(t, si.writeRefresh(t.Context(), id, "acme-okta", strings.Repeat("r", 2*refreshTokenMost), records.Now(), nil, store.Always))
	if got := si.checkRefresh(t.Context(), id, "acme-okta"); got != refreshGood || p.s.records.Person(id).IsSuspended() {
		t.Fatalf("an old large token: %v", got)
	}
}

func TestAnAccountNameTooLongForATicketIsRefused(t *testing.T) {
	t.Parallel()
	prov := signintest.NewProvider(t, "RS256", "")
	p, _, owner, _ := orgPlane(t, prov, nil)
	addOrg(t, owner, prov, wire.OrgProviderJoinAnyone)
	prov.SignInAs(signintest.Claims{"sub": strings.Repeat("s", oidcTicketMostBytes), "email": "kim@acme.test", "name": "Kim"})
	if _, err := p.signInOrg(t, &browser{prov: prov}); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatal(err)
	}
	if keys, err := p.store.List(t.Context(), oidcTicketPrefix); err != nil || len(keys) != 0 {
		t.Fatalf("%v %v", keys, err)
	}
}

func TestABudgetRequestIsCutToItsLargestAndCanBeAnswered(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("<", 4000)
	r := wire.BudgetRequest{ID: "i", User: "p", UserName: long, Why: &long, DecidedBy: &long}
	data, err := boundedBudgetRequest(&r)
	must(t, err)
	if len(data) > requestMostBytes || r.UserName == "" || deref(r.DecidedBy) == "" {
		t.Fatalf("%d bytes", len(data))
	}
}

func TestReleaseKeysHaveACapAndKeptOnesOpen(t *testing.T) {
	t.Parallel()
	p, _, _, _ := orgPlane(t, signintest.NewProvider(t, "RS256", ""), nil)
	first, err := p.s.releaseKey(t.Context(), "BOX-1", true)
	must(t, err)
	if n := sizeOf(t, p.store, releaseKeyKey("BOX-1")); n > releaseKeyMostBytes {
		t.Fatalf("a key is %d bytes", n)
	}
	// Over the cap, from before it: a new one is refused and the old one opens.
	_, err = p.store.Put(t.Context(), releaseKeyKey("BOX-BIG"), make([]byte, releaseKeysMostBytes), store.Absent)
	must(t, err)
	if _, err := p.s.releaseKey(t.Context(), "BOX-2", true); err == nil || !strings.Contains(err.Error(), "no room") {
		t.Fatal(err)
	}
	again, err := p.s.releaseKey(t.Context(), "BOX-1", true)
	if err != nil || !again.Equal(first) {
		t.Fatalf("the kept key: %v", err)
	}
}

// No mark of an approval is evicted: one more release is refused until old ones end.
func TestTheApprovalsTakenAreCounted(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	now := time.Now()
	put := func(i int, at time.Time) {
		data, _ := json.Marshal(releaseSeen{At: at.Unix()})
		_, err := p.store.Put(t.Context(), releaseSeenPrefix("BOX")+fmt.Sprint(i)+".json", data, store.Absent)
		must(t, err)
		if len(data) > 64 {
			t.Fatalf("a mark is %d bytes", len(data))
		}
	}
	for i := range releaseSeenMost {
		put(i, now)
	}
	put(releaseSeenMost, now.Add(-3*secretrelease.ApprovalLifetime))
	if n, err := p.s.forgetOldReleases(t.Context(), "BOX", now); err != nil || n != releaseSeenMost {
		t.Fatalf("%d left: %v", n, err)
	}
	if n, err := p.s.forgetOldReleases(t.Context(), "BOX", now.Add(3*secretrelease.ApprovalLifetime)); err != nil || n != 0 {
		t.Fatalf("%d left: %v", n, err)
	}
}

// A removed person's SCIM name goes, and no one else's, nor one claimed for a person not made yet.
func TestTheSweepDropsARemovedPersonsSCIMName(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	yes := true
	for _, q := range []records.Person{{ID: "GONE", Name: "Gone", Kind: records.KindPerson, Role: records.RoleMember, Removed: &yes}, {ID: "HERE", Name: "Here", Kind: records.KindPerson, Role: records.RoleMember}} {
		must(t, p.s.records.SavePerson(t.Context(), q))
	}
	d := scimDirectory{p.s}
	for _, user := range []string{"GONE", "HERE", "NOT-YET"} {
		must(t, d.claimName(t.Context(), "acme", user+"@acme.test", user))
	}
	n, _, err := sweepScimNames(t.Context(), p.s.records, time.Now(), 10)
	must(t, err)
	if n != 1 || sizeOf(t, p.store, scimNameKey("acme", "GONE@acme.test")) != 0 || sizeOf(t, p.store, scimNameKey("acme", "HERE@acme.test")) == 0 ||
		sizeOf(t, p.store, scimNameKey("acme", "NOT-YET@acme.test")) == 0 {
		t.Fatalf("%d went", n)
	}
	for _, k := range must1(p.store.List(t.Context(), scimPrefix)) {
		if k.Size > 1<<10 {
			t.Fatalf("a name is %d bytes", k.Size)
		}
	}
}
