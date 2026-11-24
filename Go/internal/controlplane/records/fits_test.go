package records

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// A record at its largest is kept, one over it is refused, and one that is over it already
// can be changed to one no larger (#6771).
func TestARecordFits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := store.NewMemory()
	fits := func(n int) error { return Fits(ctx, s, "k", "The record", make([]byte, n), 2048) }
	if err := fits(2048); err != nil {
		t.Fatal(err)
	}
	var large *TooLargeError
	if err := fits(2049); !errors.As(err, &large) || !strings.Contains(large.Words, "the most is 2 KiB") {
		t.Fatalf("%v", err)
	}
	if _, err := s.Put(ctx, "k", make([]byte, 3000), store.Absent); err != nil {
		t.Fatal(err)
	}
	if err := fits(3000); err != nil {
		t.Fatal(err)
	}
	if err := fits(3001); !errors.As(err, &large) {
		t.Fatalf("%v", err)
	}
}

// The folders a person sets are refused past the settings' largest; what the control plane
// writes itself is not, so it starts and its key rotates (#6771).
func TestTheSettingsHaveALargestSize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := New(store.NewMemory())
	_, err := r.SettingsOrMake(ctx, func() Settings { return Settings{Name: "c"} })
	must(t, err)
	folders := func(n int) error {
		raw, _ := json.Marshal(strings.Repeat("x", n))
		_, err := r.ChangeSettings(ctx, func(s *Settings) { s.FolderDetection = raw })
		return err
	}
	var large *TooLargeError
	if err := folders(SettingsMostBytes); !errors.As(err, &large) {
		t.Fatalf("%v", err)
	}
	must(t, folders(1))
	is, err := settingsSize(*r.Settings())
	must(t, err)
	room := SettingsMostBytes - is + 1
	must(t, folders(room))
	if now, _ := settingsSize(*r.Settings()); now != SettingsMostBytes {
		t.Fatalf("%d", now)
	}
	if err := folders(room + 1); !errors.As(err, &large) {
		t.Fatalf("%v", err)
	}
	url := "https://" + strings.Repeat("a", 200)
	_, err = r.ChangeSettings(ctx, func(s *Settings) { s.URL, s.Endpoints = &url, []Endpoint{{URL: url}} })
	must(t, err)
	_, err = r.ChangeSettings(ctx, func(s *Settings) { s.Endpoints = []Endpoint{{URL: url}, {URL: url + "b"}} })
	if !errors.As(err, &large) {
		t.Fatalf("%v", err)
	}
	must(t, folders(10))
}
