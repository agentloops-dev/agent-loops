package controlplane

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/kms"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/internal/secretrelease"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #4058: the control plane keeps a Linux machine's release key, and gives one value's key
// only for a fresh approval that a client of the machine's person's lock signed to that
// machine, once. No approval, an old one, one for another machine or name, one the machine
// signed itself, or one seen before is refused.
func TestTheControlPlaneReleasesOnlyForAFreshSignedApproval(t *testing.T) {
	t.Parallel()
	keeper, err := kms.LocalKeeper(filepath.Join(t.TempDir(), "secrets-key"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	p := startPlaneWith(t, st, controllink.NewPrivateKey(), func(c *Config) { c.SecretKeeper = keeper })
	_, mac := p.client("Mac")
	macKey := lockKey(t)
	signOnto(t, mac, macKey, wire.LockBody{Kind: wire.LockEntryKindRoot, Key: machinelock.PublicKey(macKey)})
	box := p.host("box")
	host := box.member.Host
	boxKey, _ := controllink.PublicKey(box.private)
	signOnto(t, mac, macKey, wire.LockBody{Kind: wire.LockEntryKindAddMachine, Key: boxKey, SealingKey: boxKey, ID: new(host)})
	ctx := context.Background()

	// The public half, the same each time; the private half only sealed in the store.
	var public, again wire.SecretReleaseKey
	b, err := box.uplink.Ask(ctx, wire.CallMachineSecretKey.Name, wire.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	must(t, json.Unmarshal(b, &public))
	b, _ = box.uplink.Ask(ctx, wire.CallMachineSecretKey.Name, wire.Empty{})
	must(t, json.Unmarshal(b, &again))
	if len(public.PublicKey) != 32 || string(public.PublicKey) != string(again.PublicKey) {
		t.Fatalf("%x %x", public.PublicKey, again.PublicKey)
	}
	o, _ := st.Get(ctx, releaseKeyKey(host))
	if o == nil || !strings.Contains(string(o.Data), "wrappedKey") {
		t.Fatalf("the release key is not kept sealed: %v", o)
	}

	ad := []byte("ad")
	layer, err := secretrelease.Seal(public.PublicKey, "GITLAB_PASSWORD", "hunter2", ad)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	approve := func(k *ecdsa.PrivateKey, to string, name string, at time.Time) wire.LockDelivery {
		t.Helper()
		if k == nil {
			k = macKey
		}
		d, err := secretrelease.SignApproval(k, to, secretrelease.Approval{Request: "R1", Name: name, Use: "fill", Place: "gitlab.com", Action: "once"}, at)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	release := func(name string, d wire.LockDelivery) ([]byte, error) {
		b, err := box.uplink.Ask(ctx, wire.CallMachineSecretRelease.Name, wire.SecretRelease{Name: name, Encapsulated: layer.Encapsulated, Approval: d})
		if err != nil {
			return nil, err
		}
		var r wire.SecretReleased
		must(t, json.Unmarshal(b, &r))
		return r.Key, nil
	}

	machineSigner, _ := machinelock.MachineSigner(box.private)
	for why, d := range map[string]wire.LockDelivery{
		"another machine":   approve(nil, "other", "GITLAB_PASSWORD", now),
		"another name":      approve(nil, host, "OTHER", now),
		"too old":           approve(nil, host, "GITLAB_PASSWORD", now.Add(-secretrelease.ApprovalLifetime-time.Minute)),
		"the machine's own": approve(machineSigner, host, "GITLAB_PASSWORD", now),
		"no lock key":       approve(lockKey(t), host, "GITLAB_PASSWORD", now),
		"unsigned":          {Method: secretrelease.ApproveMethod, Params: `{"request":"R1","name":"GITLAB_PASSWORD"}`},
	} {
		if _, err := release("GITLAB_PASSWORD", d); err == nil || !strings.Contains(err.Error(), "Nothing was released") {
			t.Errorf("%s: %v", why, err)
		}
	}

	good := approve(nil, host, "GITLAB_PASSWORD", now)
	key, err := release("GITLAB_PASSWORD", good)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := secretrelease.Open(key, layer, ad); err != nil || v != "hunter2" {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := release("GITLAB_PASSWORD", good); err == nil || !strings.Contains(err.Error(), "used before") {
		t.Fatalf("a replay: %v", err)
	}
	// Another machine of no lock cannot use this machine's approval.
	other := p.host("other")
	if _, err := other.uplink.Ask(ctx, wire.CallMachineSecretRelease.Name, wire.SecretRelease{Name: "GITLAB_PASSWORD",
		Encapsulated: layer.Encapsulated, Approval: approve(nil, host, "GITLAB_PASSWORD", now)}); err == nil {
		t.Fatal("another machine was released this machine's key")
	}
}

func TestRemovingMachineDeletesItsReleaseKeyAndSeenApprovals(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	box := p.host("RELEASE-BOX")
	ctx := context.Background()
	for _, key := range []string{releaseKeyKey(box.member.Host), releaseSeenPrefix(box.member.Host) + "approval.json"} {
		if _, err := p.s.cfg.Store.Put(ctx, key, []byte(`{"at":1}`), store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	if gone, err := p.s.methods.removeHost(ctx, box.member.Host, Caller{}); err != nil || !gone {
		t.Fatalf("remove machine: gone=%v err=%v", gone, err)
	}
	for _, key := range []string{releaseKeyKey(box.member.Host), releaseSeenPrefix(box.member.Host) + "approval.json"} {
		if object, err := p.s.cfg.Store.Get(ctx, key); err != nil || object != nil {
			t.Fatalf("%s remains: object=%v err=%v", key, object, err)
		}
	}
}

func TestHourlyReleaseKeySweepExpiresSeenAndRemovesOrphans(t *testing.T) {
	t.Parallel()
	p := startPlane(t, store.NewMemory(), controllink.NewPrivateKey())
	ctx, now := context.Background(), time.Now()
	active := p.host("ACTIVE-RELEASE-BOX").member.Host
	oldSeen := releaseSeenPrefix(active) + "old.json"
	newSeen := releaseSeenPrefix(active) + "new.json"
	orphanKey := releaseKeyKey("REMOVED-RELEASE-BOX")
	orphanSeen := releaseSeenPrefix("REMOVED-RELEASE-BOX") + "seen.json"
	for key, seen := range map[string]releaseSeen{
		oldSeen:    {At: now.Add(-3 * secretrelease.ApprovalLifetime).Unix()},
		newSeen:    {At: now.Unix()},
		orphanSeen: {At: now.Unix()},
	} {
		b, err := json.Marshal(seen)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.s.cfg.Store.Put(ctx, key, b, store.Absent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.s.cfg.Store.Put(ctx, orphanKey, []byte(`{}`), store.Absent); err != nil {
		t.Fatal(err)
	}
	n, err := sweepReleaseKeys(ctx, p.s.cfg.Store, p.s.records, now, 100)
	if err != nil || n != 3 {
		t.Fatalf("deleted %d release records, err %v", n, err)
	}
	for key, want := range map[string]bool{oldSeen: false, newSeen: true, orphanKey: false, orphanSeen: false} {
		object, err := p.s.cfg.Store.Get(ctx, key)
		if err != nil || (object != nil) != want {
			t.Errorf("%s exists=%v, want %v (err %v)", key, object != nil, want, err)
		}
	}
}
