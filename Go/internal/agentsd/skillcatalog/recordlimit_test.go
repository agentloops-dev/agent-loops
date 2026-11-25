package skillcatalog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
)

// #6908: a record at its largest is taken, and one a byte over it is refused with the file as it was.
func TestRecordLimitOfTheSidecar(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "catalog-skills.json")
	put := func(n int) error {
		return UpdateSidecar(path, func(m map[string]SidecarRecord) { m["k"] = SidecarRecord{Commit: strings.Repeat("x", n)} })
	}
	n := sidecarRecordMost
	for put(n) != nil {
		n--
	}
	if n < sidecarRecordMost-500 {
		t.Fatalf("the longest commit is %d", n)
	}
	before, err := os.ReadFile(path)
	must(t, err)
	if err := put(n + 1); !errors.Is(err, store.ErrLimit) || !strings.Contains(err.Error(), "8192") {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a refused write changed the file")
	}
}

// The sweep marks and drops an item that went, with its record at the largest size.
func TestTheSweepsMarkFitsARecordAtItsLargest(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "catalog-skills.json")
	n := sidecarRecordMost
	for UpdateSidecar(path, func(m map[string]SidecarRecord) { m["k"] = SidecarRecord{Commit: strings.Repeat("x", n)} }) != nil {
		n--
	}
	if err := UpdateSidecar(path, func(m map[string]SidecarRecord) {
		for k, r := range m {
			at := time.Now().UTC().Truncate(time.Second)
			r.GoneSince = &at
			m[k] = r
		}
	}); err != nil {
		t.Fatalf("the sweep of every item is refused: %v", err)
	}
}

// A record over its largest from before there was one is marked too, and only marked; the mark
// makes no room for a longer record.
func TestARecordOverItsLargestIsOnlyMarked(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC().Truncate(time.Second)
	records := map[string]SidecarRecord{"k": {Commit: strings.Repeat("x", 2*sidecarRecordMost)}, "small": {}}
	for _, since := range []*time.Time{&at, nil} {
		changed, err := ChangeRecords(records, func(m map[string]SidecarRecord) {
			for k, r := range m {
				r.GoneSince = since
				m[k] = r
			}
		})
		if err != nil || !changed {
			t.Fatalf("%v: %v", changed, err)
		}
	}
	if _, err := ChangeRecords(records, func(m map[string]SidecarRecord) {
		m["k"] = SidecarRecord{Commit: strings.Repeat("y", 2*sidecarRecordMost), GoneSince: &at}
	}); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("a changed record over its largest: %v", err)
	}
}

// #7247: marketplaces.json at its limits and above them.
func TestTheLimitsOfTheMarketplaces(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "marketplaces.json")
	entry := func(n int) MarketplaceEntry { return MarketplaceEntry{Name: "n", URL: strings.Repeat("u", n)} }
	many := func(n int) (out []MarketplaceEntry) {
		for i := range n {
			out = append(out, MarketplaceEntry{Name: "n", URL: strconv.Itoa(i)})
		}
		return out
	}
	n := marketplaceRecordMost
	for SaveMarketplaces(MarketplaceFile{Marketplaces: []MarketplaceEntry{entry(n)}}, path) != nil {
		n--
	}
	must(t, SaveMarketplaces(MarketplaceFile{Marketplaces: many(marketplacesMost), Off: make([]string, marketplaceMarksMost)}, path))
	before, err := os.ReadFile(path)
	must(t, err)
	for _, f := range []MarketplaceFile{
		{Marketplaces: []MarketplaceEntry{entry(n + 1)}},
		{Marketplaces: many(marketplacesMost + 1)},
		{Off: make([]string, marketplaceMarksMost+1)},
		{On: make([]string, marketplaceMarksMost+1)},
		{On: []string{strings.Repeat("x", marketplaceRecordMost+1)}},
	} {
		if err := SaveMarketplaces(f, path); !errors.Is(err, store.ErrLimit) {
			t.Fatal(err)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a refused write changed the file")
	}
	// A file over its limits from before there were any can be made smaller.
	old := MarketplaceFile{Marketplaces: append(many(marketplacesMost+5), entry(marketplaceRecordMost))}
	b, err := json.Marshal(old)
	must(t, err)
	must(t, os.WriteFile(path, b, 0o600))
	old.Marketplaces = old.Marketplaces[1:]
	must(t, SaveMarketplaces(old, path))
}

// #7247: a lock at its limits and above them, for each of its collections.
func TestTheLimitsOfALock(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	path := filepath.Join(t.TempDir(), "skills-lock.json")
	lock := func() *Lock {
		l, err := LoadLock(FolderScope, path)
		must(t, err)
		return l
	}
	l := lock()
	n := lockRecordMost
	for l.Upsert("k", "o/r", strings.Repeat("p", n), "h", now); l.Write() != nil; l.Upsert("k", "o/r", strings.Repeat("p", n), "h", now) {
		n--
	}
	if n < lockRecordMost-500 {
		t.Fatalf("the longest path is %d", n)
	}
	for i := 1; i < lockMost; i++ {
		l.Upsert(strconv.Itoa(i), "o/r", "p", "h", now)
	}
	for i := range lockMost {
		l.UpsertPersona(strconv.Itoa(i), PersonaEntry{Source: "o/r"}, now)
	}
	must(t, l.Write())
	before, err := os.ReadFile(path)
	must(t, err)
	for _, change := range []func(*Lock){
		func(l *Lock) { l.Upsert("k", "o/r", strings.Repeat("p", n+1), "h", now) },
		func(l *Lock) { l.Upsert("more", "o/r", "p", "h", now) },
		func(l *Lock) { l.UpsertPersona("more", PersonaEntry{Source: "o/r"}, now) },
	} {
		l := lock()
		change(l)
		if err := l.Write(); !errors.Is(err, store.ErrLimit) || !strings.Contains(l.writeFailed(err).Error(), "at most") {
			t.Fatal(err)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a refused write changed the file")
	}
	// The CLI may have put more in it: it can be made smaller, and what is over stays.
	l = lock()
	l.Upsert("more", "o/r", strings.Repeat("p", 2*lockRecordMost), "h", now)
	must(t, os.WriteFile(path, []byte(l.Text()), 0o600))
	l = lock()
	l.Remove("1")
	must(t, l.Write())
	if _, ok := lock().Entry("more"); !ok {
		t.Fatal("an entry over its largest went")
	}
}

// #7247: secrets.env at its limits and above them, with no name or value in the words.
func TestTheLimitsOfSecretsEnv(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "secrets.env")
	var s Secrets
	most := strings.Repeat("v", secretLineMost-len("NAME="))
	s.Set("NAME", most)
	must(t, s.Save(path))
	s.Set("NAME", most+"v")
	if err := s.Save(path); !errors.Is(err, store.ErrLimit) || strings.Contains(err.Error(), "NAME") {
		t.Fatal(err)
	}
	s.Set("NAME", most)
	var err error
	for i := 0; err == nil && i < 100; i++ {
		s.Set("NAME_"+strconv.Itoa(i), most[:len(most)-20])
		err = s.Save(path)
	}
	if !errors.Is(err, store.ErrLimit) {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(path)
	must(t, err)
	if len(kept) > secretsFileMost || LoadSecrets(path).Names() == nil {
		t.Fatalf("%d bytes", len(kept))
	}
	// A file over its size can be made smaller.
	must(t, os.WriteFile(path, []byte(s.text()+"# "+strings.Repeat("x", secretsFileMost)+"\n"), 0o600))
	s = LoadSecrets(path)
	s.Remove("NAME")
	must(t, s.Save(path))
}
