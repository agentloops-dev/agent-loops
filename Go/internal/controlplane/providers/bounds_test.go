package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func addNamed(h *harness, name string, commands map[string]any) (string, error) {
	out, err := h.call(MethodProvidersAdd, map[string]any{"name": name, "kind": "commands", "commands": commands})
	if err != nil {
		return "", err
	}
	return string(out.(ProviderSummary).ID), nil
}

// The eleventh provider is refused, with an OAuth connection's start too; those there can
// still be changed, and a removal makes room (#6762).
func TestAProviderPastTheMostIsRefused(t *testing.T) {
	h := newHarness(t)
	doctl := map[string]any{"preset": "doctl"}
	first := ""
	for n := range mostProviders {
		id, err := addNamed(h, fmt.Sprintf("Provider %d", n), doctl)
		must(t, err)
		if first == "" {
			first = id
		}
	}
	full := func(err error) bool { return err != nil && strings.Contains(err.Error(), "10 is the most") }
	if _, err := addNamed(h, "One more", doctl); !full(err) {
		t.Fatalf("%v", err)
	}
	if _, err := h.call(MethodProvidersOAuthStart, map[string]any{"preset": "digitalocean-api"}); !full(err) {
		t.Fatalf("%v", err)
	}
	h.must(MethodProvidersUpdate, map[string]any{"id": first, "name": "Renamed", "kind": "commands", "commands": doctl})
	h.must(MethodProvidersRemove, map[string]string{"id": first})
	if _, err := addNamed(h, "One more", doctl); err != nil {
		t.Fatal(err)
	}
}

// One being added has its place, so that two at once do not pass the limit; one that fails
// gives it back.
func TestAProviderBeingAddedHasItsPlace(t *testing.T) {
	h := newHarness(t)
	var releases []func()
	for n := range mostProviders {
		release, err := h.providers.reserve(fmt.Sprintf("p%d", n), false)
		must(t, err)
		releases = append(releases, release)
	}
	if _, err := addNamed(h, "One more", map[string]any{"preset": "doctl"}); err == nil {
		t.Fatal("added past the limit")
	}
	releases[0]()
	if _, err := addNamed(h, "No commands", nil); err == nil || strings.Contains(err.Error(), "the most") {
		t.Fatalf("%v", err)
	}
	if _, err := addNamed(h, "One more", map[string]any{"preset": "doctl"}); err != nil {
		t.Fatal(err)
	}
}

// A record over the largest is refused, and its secrets do not go to its machine; a change
// that does not grow one that is over already is kept.
func TestAProviderOverTheLargestIsRefused(t *testing.T) {
	h := newHarness(t)
	big := map[string]any{"preset": "doctl", "tag": strings.Repeat("x", providerMostBytes)}
	if _, err := addNamed(h, "Big", big); err == nil || !strings.Contains(err.Error(), "64 KiB at most") {
		t.Fatalf("%v", err)
	}
	if len(h.providers.Summaries()) != 0 {
		t.Fatal("it was added")
	}
	before := ProviderRecord{ID: "p", Name: "Big one", Kind: KindCommands, Commands: big}
	after := before
	after.Name = "Big"
	if _, err := encodeWithin(after, &before); err != nil {
		t.Fatal(err)
	}
	after.Name = "A bigger one"
	if _, err := encodeWithin(after, &before); err == nil {
		t.Fatal("one over the largest grew")
	}
}

// A removed provider keeps none of its configuration.
func TestARemovedProvidersTombstoneIsSmall(t *testing.T) {
	h := newHarness(t)
	id := h.add()
	h.must(MethodProvidersRemove, map[string]string{"id": id})
	object, err := h.store.Get(context.Background(), ProviderKey(id))
	must(t, err)
	var r ProviderRecord
	must(t, json.Unmarshal(object.Data, &r))
	if r.Forgotten == nil || !*r.Forgotten || r.ForgottenAt == nil || r.Commands != nil || len(r.SecretNames) != 0 || r.Machine != nil {
		t.Fatalf("%s", object.Data)
	}
}
