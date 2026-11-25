package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #5462: a machine's approval is kept for its person; the first of a kind and digest stays,
// and a malformed one is refused.
func TestApprovalsAreKeptForThePersonAndTheFirstStays(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	a, b := p.host("MAC-A").member.Host, p.host("MAC-B").member.Host
	p.waitFor(func() bool { return p.s.router.Holds(a) && p.s.router.Holds(b) }, "the machines held")
	digest := strings.Repeat("ab", 32)
	first := wire.PersonApproval{Kind: "workflow", Digest: digest, Host: a, At: 1, Key: wire.Data("k"), Sig: wire.Data("s")}
	second := first
	second.Host, second.At = b, 2
	busSay[wire.PersonApprovals](t, p, a, wire.CallApprovalsPut.Name, wire.PersonApprovalsPut{Approvals: []wire.PersonApproval{first}})
	busSay[wire.PersonApprovals](t, p, b, wire.CallApprovalsPut.Name, wire.PersonApprovalsPut{Approvals: []wire.PersonApproval{second}})
	got := busSay[wire.PersonApprovals](t, p, b, wire.CallApprovalsList.Name, wire.Empty{})
	if len(got.Approvals) != 1 || got.Approvals[0].Host != a {
		t.Fatalf("%+v", got.Approvals)
	}
	raw := []byte(`{"approvals":[{"kind":"workflow","digest":"x","host":"h","at":1,"key":"aw==","sig":"aw=="}]}`)
	if _, err := p.s.methods.HostSaid(a, wire.CallApprovalsPut.Name, raw); err == nil {
		t.Fatal("a malformed approval was kept")
	}
}

// #6970: past the most a person keeps, a new approval is refused and none is deleted; one
// that is kept already is not refused.
func TestApprovalsPastTheMostAreRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	host := p.host("MAC-A").member.Host
	p.waitFor(func() bool { return p.s.router.Holds(host) }, "the machine held")
	of := func(i int) wire.PersonApproval {
		return wire.PersonApproval{Kind: "workflow", Digest: fmt.Sprintf("%064x", i), Host: host, At: 1, Key: wire.Data("k"), Sig: wire.Data("s")}
	}
	put := func(as ...wire.PersonApproval) error {
		_, err := p.s.putApprovals(ctx, host, wire.PersonApprovalsPut{Approvals: as})
		return err
	}
	person := p.s.ownerOf(host)
	for i := range approvalsKeptMost - 1 {
		if _, err := p.store.Put(ctx, approvalKey(person, of(i)), []byte("{}"), store.Always); err != nil {
			t.Fatal(err)
		}
	}
	if err := put(of(5000), of(5001)); err == nil {
		t.Fatal("two approvals took the last place")
	}
	if err := put(of(5000), of(5000), of(0)); err != nil {
		t.Fatal(err)
	}
	if err := put(of(5001)); err == nil || !strings.Contains(err.Error(), "at most 2000") {
		t.Fatalf("an approval past the most: %v", err)
	}
	if err := put(of(5000)); err != nil {
		t.Fatalf("one kept already: %v", err)
	}
	if keys, err := p.store.List(ctx, approvalsOf(person)); err != nil || len(keys) != approvalsKeptMost {
		t.Fatalf("%d %v", len(keys), err)
	}
}
