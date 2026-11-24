package records

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #6757: a new client over the clients' limit is refused, and so is a push token over it;
// one paired is renewed all the same, and a forget makes room.
func TestClientsRefuseAtTheirLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	r := New(s)
	client := func(n int) ClientRecord {
		return ClientRecord{ID: fmt.Sprintf("E621E1F8-C36C-495A-93FC-%012X", n), Name: strings.Repeat("n", 15<<10), Kind: KindIPhone,
			PublicKey: []byte{1}, Paired: Now()}
	}
	n := 0
	var full *ClientLimitError
	for ; n < 100; n++ {
		if err := r.SaveClient(ctx, client(n)); errors.As(err, &full) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	keys, err := s.List(ctx, ClientsPrefix)
	if err != nil || n == 100 || len(keys) != n {
		t.Fatalf("%d clients, %d keys: %v", n, len(keys), err)
	}
	// Fill to the limit with an object nobody can read, which counts too.
	if _, err := s.Put(ctx, ClientKey("filler"), make([]byte, ClientMostBytes), store.Absent); err != nil {
		t.Fatal(err)
	}
	if err := r.ClientRoom(ctx, client(n).ID); !errors.As(err, &full) {
		t.Fatalf("room for one more: %v", err)
	}
	if err := r.ClientRoom(ctx, client(0).ID); err != nil {
		t.Fatalf("a paired client has its room: %v", err)
	}
	// In use: its key renews. A push token waits for room.
	later := Now()
	if err := r.ChangeClient(ctx, client(0).ID, func(c *ClientRecord) bool { c.Expires, c.LastSeen = &later, &later; return true }); err != nil {
		t.Fatalf("a client in use was not renewed: %v", err)
	}
	push := func(c *ClientRecord) bool {
		c.PushToken = &PushToken{Token: "ab", Environment: "production"}
		return true
	}
	if err := r.ChangeClient(ctx, client(0).ID, push); !errors.As(err, &full) {
		t.Fatalf("a push token over the limit: %v", err)
	}
	for _, i := range []int{1, 2} {
		if err := r.ForgetClient(ctx, client(i).ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ChangeClient(ctx, client(0).ID, push); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveClient(ctx, client(n)); err != nil {
		t.Fatalf("a forget made no room: %v", err)
	}
}

// A record over its largest is refused; one that is larger already, from before the
// limit, is renewed all the same.
func TestAClientRecordHasALargestSize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	r := New(s)
	c := ClientRecord{ID: "E621E1F8-C36C-495A-93FC-0C247A3E6E5F", Name: strings.Repeat("n", ClientMostBytes), Kind: KindMac, PublicKey: []byte{1}, Paired: Now()}
	var full *ClientLimitError
	if err := r.SaveClient(ctx, c); !errors.As(err, &full) {
		t.Fatalf("a record over the largest: %v", err)
	}
	data, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, ClientKey(c.ID), data, store.Absent); err != nil {
		t.Fatal(err)
	}
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	later := Now()
	if err := r.ChangeClient(ctx, c.ID, func(c *ClientRecord) bool { c.Expires = &later; return true }); err != nil {
		t.Fatalf("not renewed: %v", err)
	}
	if err := r.ChangeClient(ctx, c.ID, func(c *ClientRecord) bool { c.Name += "n"; return true }); !errors.As(err, &full) {
		t.Fatalf("grew: %v", err)
	}
}
