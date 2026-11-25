package records

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #7248: a group and a project are made by a person, so one over its largest is refused, and so
// is a new group past the count.
func TestAGroupIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := New(store.NewMemory())
	make1 := func(id string, members int) error {
		_, err := r.ChangeGroup(ctx, id, func(*Group) (*Group, error) {
			g := &Group{ID: id, Name: id}
			for i := range members {
				g.Members = append(g.Members, fmt.Sprintf("%s%036d", "u", i))
			}
			return g, nil
		})
		return err
	}
	var large *TooLargeError
	if err := make1("big", GroupMostBytes/38); !errors.As(err, &large) {
		t.Fatalf("a group over its largest was kept: %v", err)
	}
	if err := make1("ok", 100); err != nil {
		t.Fatal(err)
	}
}

func TestGroupsAreAtMostTheirCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	r := New(st)
	for i := range GroupsMost {
		_, err := r.ChangeGroup(ctx, fmt.Sprint("g", i), func(*Group) (*Group, error) { return &Group{ID: fmt.Sprint("g", i), Name: "n"}, nil })
		if err != nil {
			t.Fatalf("group %d: %v", i, err)
		}
	}
	var large *TooLargeError
	if _, err := r.ChangeGroup(ctx, "one-more", func(*Group) (*Group, error) { return &Group{ID: "one-more", Name: "n"}, nil }); !errors.As(err, &large) {
		t.Fatalf("a group past the count was kept: %v", err)
	}
	// One that is there can still change.
	if _, err := r.ChangeGroup(ctx, "g1", func(g *Group) (*Group, error) { g.Name = "renamed"; return g, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestAProjectIsAtMostItsLargest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := New(store.NewMemory())
	_, err := r.ChangeProject(ctx, "p1", func(*Project) (*Project, error) {
		return &Project{ID: "p1", Name: strings.Repeat("n", ProjectMostBytes)}, nil
	})
	var large *TooLargeError
	if !errors.As(err, &large) {
		t.Fatalf("a project over its largest was kept: %v", err)
	}
	if _, err := r.ChangeProject(ctx, "p1", func(*Project) (*Project, error) { return &Project{ID: "p1", Name: "ok"}, nil }); err != nil {
		t.Fatal(err)
	}
}
