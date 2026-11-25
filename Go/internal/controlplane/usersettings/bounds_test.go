package usersettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func keepRecord(t *testing.T, s store.Store, key string, v any) {
	t.Helper()
	data, err := records.Encode(v)
	must(t, err)
	_, err = s.Put(context.Background(), key, data, store.Always)
	must(t, err)
}

func envelope(n int) wire.SealedEnvelope {
	return wire.SealedEnvelope{Encapsulated: make([]byte, 65), Ciphertext: make([]byte, n)}
}

// A part at its largest is kept, one over it is refused, and one that is over it already can
// be changed to one no larger (#6780).
func TestAPartOverItsLargest(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	b := localBook(t, s)
	set := func(part wire.UserSettingsPartName, size int) error {
		value := `{"a":"` + strings.Repeat("x", size-len(`{"a":""}`)) + `"}`
		_, err := b.SetPart(ctx, "ana", part, json.RawMessage(value))
		return err
	}
	for part, most := range map[wire.UserSettingsPartName]int{wire.UserSettingsPartNameRuntimes: MaxPart, wire.UserSettingsPartNameAuto: MaxSmallPart} {
		must(t, set(part, most))
		if err := set(part, most+1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", part, err)
		}
	}
	// The whole record, which is indented: many short items take more there.
	if _, err := encodeSettings(wire.UserSettings{Auto: wire.JSONValue("[" + strings.Repeat("1,", 1<<20) + "1]")}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	over := json.RawMessage(`{"a":"` + strings.Repeat("x", MaxSmallPart+100) + `"}`)
	keepRecord(t, s, settingsKey("bea"), wire.UserSettings{Auto: wire.JSONValue(over)})
	smaller := json.RawMessage(`{"a":"` + strings.Repeat("x", MaxSmallPart+50) + `"}`)
	_, err := b.SetPart(ctx, "bea", wire.UserSettingsPartNameAuto, smaller)
	must(t, err)
	if _, err := b.SetPart(ctx, "bea", wire.UserSettingsPartNameAuto, over); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
}

// A person's secrets, their removed ones, a secret's copies and the machines it names have a
// limit; what a person has can still be set again (#6780).
func TestSecretsHaveALimit(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	b := localBook(t, s)
	allowed := map[string]bool{"m:A": true}
	set := func(name string, version int64) error {
		return b.SetSecret(ctx, "ana", wire.UserSecretSet{Name: wire.UserSecretName(name), Version: version,
			Values: map[string]wire.SealedEnvelope{"m:A": envelope(20)}}, allowed)
	}
	for n := range MaxSecrets - 1 {
		keepRecord(t, s, secretKey("ana", fmt.Sprintf("S%d", n)), secretRecord{Name: fmt.Sprintf("S%d", n), Version: 1, Sealed: map[string]wire.SealedEnvelope{"m:A": envelope(20)}})
	}
	must(t, set("LAST", 1))
	if err := set("MORE", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	must(t, set("LAST", 2))
	_, err := b.RemoveSecret(ctx, "ana", "LAST")
	must(t, err)
	for n := range MaxRemovedSecrets - 1 {
		name := fmt.Sprintf("R%d", n)
		keepRecord(t, s, secretKey("ana", name), secretRecord{Name: name, Version: 1, Updated: b.stamp()})
	}
	if err := set("MORE", 1); err == nil || !strings.Contains(err.Error(), "were removed") {
		t.Fatalf("%v", err)
	}
	must(t, set("LAST", 3))

	// Copies: at the most one more is refused.
	many := map[string]wire.SealedEnvelope{}
	for n := range MaxRecipients {
		many[fmt.Sprintf("m:%d", n)] = envelope(MaxSecret + 16)
		allowed[fmt.Sprintf("m:%d", n)] = true
	}
	allowed["m:one-more"] = true
	must(t, b.SetSecret(ctx, "bea", wire.UserSecretSet{Name: "BIG", Version: 1, Values: many}, allowed))
	if _, err := b.AddSealed(ctx, "bea", "BIG", 1, map[string]wire.SealedEnvelope{"m:one-more": envelope(20)}, allowed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if o, _ := s.Get(ctx, secretKey("bea", "BIG")); len(o.Data) > MaxSecretRecord {
		t.Fatalf("%d bytes", len(o.Data))
	}
	// The record's own limit, over and not growing.
	big := secretRecord{Name: "X", Description: strings.Repeat("x", MaxSecretRecord)}
	if _, err := encodeSecret(big, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if _, err := encodeSecret(big, make([]byte, MaxSecretRecord+1000)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Offer(ctx, "bea", "box", "Box", wire.AgentSecretOffer{Offer: "E621E1F8-C36C-495A-93FC-000000000001", Name: "O",
		SavedAt: strings.Repeat("x", MaxSecretRecord), Sealed: map[string]wire.SealedEnvelope{"c:A": envelope(20)}}, map[string]bool{"c:A": true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}

	// The machines it was left on: the newest.
	left := secretRecord{Name: "LEFT", Version: 1, Sealed: map[string]wire.SealedEnvelope{"m:A": envelope(20)}, Machines: []string{"new"}}
	for n := range MaxLeftOn {
		left.LeftOn = append(left.LeftOn, wire.SecretLeftOn{Host: wire.MachineRef(fmt.Sprint(n))})
	}
	keepRecord(t, s, secretKey("cas", "LEFT"), left)
	_, err = b.Left(ctx, "cas", "new", strings.Repeat("é", 500))
	must(t, err)
	rows, err := b.Names(ctx, "cas", nil)
	must(t, err)
	if l := rows[0].LeftOn; len(l) != MaxLeftOn || l[0].Host != "1" || l[MaxLeftOn-1].Host != "new" || len([]rune(l[MaxLeftOn-1].Name)) != maxLeftOnName {
		t.Fatalf("%+v", l)
	}
}

// A removed secret's tombstone goes 90 days on, and a set at the same time is kept or
// refused, never lost (#6780).
func TestTheSweepDeletesRemovedSecrets(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	b := localBook(t, s)
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }
	sealed := map[string]wire.SealedEnvelope{"m:A": envelope(20)}
	keepRecord(t, s, secretKey("ana", "OLD"), secretRecord{Name: "OLD", Version: 5, Updated: ago(RemovedSecretsKept + time.Minute)})
	keepRecord(t, s, secretKey("ana", "NEW"), secretRecord{Name: "NEW", Version: 5, Updated: ago(RemovedSecretsKept - time.Minute)})
	keepRecord(t, s, secretKey("ana", "LIVE"), secretRecord{Name: "LIVE", Version: 5, Updated: ago(2 * RemovedSecretsKept), Sealed: sealed})
	keepRecord(t, s, secretKey("ana", "GOING"), secretRecord{Name: "GOING", Version: 5, Updated: ago(2 * RemovedSecretsKept), Going: true})
	// One being deleted is not written over.
	set := wire.UserSecretSet{Name: "GOING", Version: 6, Values: sealed}
	if err := b.SetSecret(ctx, "ana", set, map[string]bool{"m:A": true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if n, err := SweepRemovedSecrets(ctx, s, now, 1); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if n, err := SweepRemovedSecrets(ctx, s, now, 10); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	for name, want := range map[string]bool{"OLD": false, "NEW": true, "LIVE": true, "GOING": false} {
		if o, _ := s.Get(ctx, secretKey("ana", name)); (o != nil) != want {
			t.Errorf("%s: kept %v", name, o != nil)
		}
	}
	must(t, b.SetSecret(ctx, "ana", set, map[string]bool{"m:A": true}))
}
