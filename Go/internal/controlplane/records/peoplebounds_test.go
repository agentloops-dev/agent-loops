package records

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #7019: a new person over the people's limit is refused, and so is a longer name. A person
// who is here signs in, changes role, and is suspended and removed all the same, and a
// removal makes room.
func TestPeopleRefuseAtTheirLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	r := New(s)
	var full *PersonLimitError
	n := 0
	for ; n < 200; n++ {
		p := Person{ID: fmt.Sprintf("p%03d", n), Name: strings.Repeat("n", 15<<10), Provisioned: true, SCIM: &PersonSCIM{Active: true}}
		if err := r.SavePerson(ctx, p); errors.As(err, &full) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if n == 200 || !full.Full {
		t.Fatalf("no limit: %d %+v", n, full)
	}
	// Fill to the limit with an object nobody can read, which counts too.
	if _, err := s.Put(ctx, PersonKey("filler"), make([]byte, PersonMostBytes), store.Absent); err != nil {
		t.Fatal(err)
	}
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.PersonRoom(); !errors.As(err, &full) {
		t.Fatalf("room for one more: %v", err)
	}
	change := func(id string, f func(*Person)) error {
		return r.ChangePeople(ctx, func(all map[string]Person) ([]Person, error) {
			p := all[id]
			f(&p)
			return []Person{p}, nil
		})
	}
	longer := func(p *Person) { p.Name += "n" }
	if err := change("p000", longer); !errors.As(err, &full) {
		t.Fatalf("a longer name: %v", err)
	}
	for what, f := range map[string]func(*Person){
		"signs in": func(p *Person) {
			p.Provisioned, p.SignIns = false, []SignIn{{Provider: "okta", Account: strings.Repeat("a", 64)}}
		},
		"made inactive": func(p *Person) {
			sc := *p.SCIM
			sc.Active, p.SCIM = false, &sc
		},
		"suspended": func(p *Person) { p.Suspended = &Suspension{By: "scim", At: Now()} },
		"a role":    func(p *Person) { p.Role, p.RoleFrom = RoleAdmin, []string{"group"} },
	} {
		if err := change("p000", f); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	for _, id := range []string{"p001", "p002"} {
		if err := change(id, func(p *Person) { *p = p.Tombstone(Now()) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := change("p000", longer); err != nil {
		t.Fatalf("a removal made no room: %v", err)
	}
	if err := r.SavePerson(ctx, Person{ID: "new"}); err != nil {
		t.Fatal(err)
	}
}

// A record over its largest is refused; one that is larger already, from before the limit,
// opens, and its person still signs in.
func TestAPersonRecordHasALargestSize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	r := New(s)
	p := Person{ID: "big", Name: strings.Repeat("n", PersonMostBytes), Provisioned: true}
	var limit *PersonLimitError
	if err := r.SavePerson(ctx, p); !errors.As(err, &limit) || limit.Full {
		t.Fatalf("a record over the largest: %v", err)
	}
	data, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, PersonKey(p.ID), data, store.Absent); err != nil {
		t.Fatal(err)
	}
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	p.Provisioned, p.SignIns = false, []SignIn{{Provider: "okta", Account: "a"}}
	if err := r.SavePerson(ctx, p); err != nil {
		t.Fatalf("the first sign-in: %v", err)
	}
	p.SignIns = append(p.SignIns, SignIn{Provider: "google", Account: "b"})
	if err := r.SavePerson(ctx, p); !errors.As(err, &limit) {
		t.Fatalf("grew: %v", err)
	}
}
