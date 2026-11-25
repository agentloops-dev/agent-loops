package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	kinds "github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #6908: each kind of a machine's store takes a record at its largest size, and refuses one a
// byte over it on a normal and a bulk write with the file as it was.

// refusedWhole is that write is refused as over a limit of limit bytes, and the file is as it was.
func refusedWhole(t *testing.T, file string, limit int, write func() error) {
	t.Helper()
	before, _ := os.ReadFile(file)
	err := write()
	var l *kinds.LimitError
	if !errors.Is(err, ErrLimit) && !errors.As(err, &l) {
		t.Fatalf("not refused as over the limit: %v", err)
	}
	if said := err.Error(); !strings.Contains(said, fmt.Sprint(limit)) {
		t.Fatalf("the refusal does not say the limit of %d: %s", limit, said)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("a refused write changed the file")
	}
}

// entryOf is the entry whose key and value come to size bytes, dated or not.
func entryOf(t *testing.T, key string, size int) string {
	t.Helper()
	// {"at":"2001-01-01T00:00:00.000Z","text":""} is the rest of the value.
	rest := len(key) + len(`{"at":"2001-01-01T00:00:00.000Z","text":""}`)
	if size < rest {
		t.Fatal(size)
	}
	return strings.Repeat("x", size-rest)
}

func TestRecordLimitOfAJSONFile(t *testing.T) {
	t.Parallel()
	for _, action := range []Action{EvictOldest, Refuse} {
		f, file, _ := testJSONFile(t, action)
		if err := putEntry(f, "k", entryOf(t, "k", 100)); err != nil {
			t.Fatalf("%s: at the limit: %v", action, err)
		}
		// The store dates it, and it is not longer for it: the sweep keeps it.
		if n, err := f.Sweep(); n != 0 || err != nil || entryCount(t, f) != 1 {
			t.Fatalf("%s: the sweep took an entry at the limit: %d %v", action, n, err)
		}
		refusedWhole(t, file, 100, func() error { return putEntry(f, "k", entryOf(t, "k", 101)) })
		// A key counts, also in characters of more than one byte.
		refusedWhole(t, file, 100, func() error { return putEntry(f, "kk", entryOf(t, "k", 100)) })
		refusedWhole(t, file, 100, func() error { return putEntry(f, "é", strings.Repeat("é", 30)) })
		// In bulk, one over the limit refuses them all.
		refusedWhole(t, file, 100, func() error {
			return f.Update(func(m map[string]jsonEntry) error {
				maps.Copy(m, map[string]jsonEntry{"a": {Text: "a"}, "b": {Text: entryOf(t, "b", 101)}})
				return nil
			})
		})
		// Again, as a retry would.
		if err := putEntry(f, "k", entryOf(t, "k", 100)); err != nil {
			t.Fatalf("%s: again: %v", action, err)
		}
	}
}

// An entry that was over the limit before the store had one does not change, so a write of
// another one works. A change that leaves it over is refused.
func TestRecordLimitOfAJSONFilesOldEntry(t *testing.T) {
	t.Parallel()
	f, file, _ := testJSONFile(t, Refuse)
	old := fmt.Sprintf(`{"old": {"text": "%0200d"}}`, 0)
	if err := os.WriteFile(file, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := putEntry(f, "a", "a"); err != nil || entryCount(t, f) != 2 {
		t.Fatal(err)
	}
	if n, err := f.Sweep(); n != 0 || err != nil {
		t.Fatalf("a store that refuses lost an entry: %d %v", n, err)
	}
	refusedWhole(t, file, 100, func() error { return putEntry(f, "old", fmt.Sprintf("%0300d", 0)) })
	if err := putEntry(f, "old", "short"); err != nil {
		t.Fatal(err)
	}
}

func TestRecordLimitOfASecretFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "s", "f.json")
	f := SecretFile[map[string]string]{Name: "f.json", Parts: []SecretPart[map[string]string]{
		{What: "things", MaxRecordBytes: 20, MaxCount: 2, Remedy: "remove one first", Entries: Entries[string]}}}
	// KEY1 and its value in quotes.
	at := map[string]string{"KEY1": strings.Repeat("x", 14)}
	must(t, f.Write(path, at))
	must(t, f.Write(path, at))
	over := map[string]string{"KEY1": strings.Repeat("x", 15)}
	refusedWhole(t, path, 20, func() error { return f.Write(path, over) })
	refusedWhole(t, path, 20, func() error { return f.Check(path, over) })
	// In bulk, and a value that its JSON makes longer.
	refusedWhole(t, path, 20, func() error { return f.Write(path, map[string]string{"KEY1": "a", "KEY2": strings.Repeat("x", 15)}) })
	refusedWhole(t, path, 20, func() error { return f.Write(path, map[string]string{"KEY1": "<<<"}) })
	if m, err := f.Read(path); err != nil || !maps.Equal(m, at) {
		t.Fatal("the earlier value is gone")
	}
}

func TestRecordLimitOfAFolderLog(t *testing.T) {
	ctx := context.Background()
	l, line := logAt(t, smallBounds())
	at := func(id string, size int) []byte {
		b := line(id, logDay)
		return append(append(b[:len(b)-1:len(b)-1], bytes.Repeat([]byte(" "), size-len(b))...), '}')
	}
	must(t, l.Append(ctx, "a", logDay, at("a", 1000)))
	refusedWhole(t, l.file, 1000, func() error { return l.Append(ctx, "b", logDay, at("b", 1001)) })
	// In bulk, one over the limit writes none of them.
	refusedWhole(t, l.file, 1000, func() error {
		return l.write(append(append(line("c", logDay), '\n'), append(at("d", 1001), '\n')...))
	})
	// The replay of the one at the limit is one record.
	must(t, l.Append(ctx, "a", logDay, at("a", 1000)))
	if ids, _ := readIDs(t, l, "", 9); ids != "a" {
		t.Fatal(ids)
	}
	// A log opened again, at a smaller limit, reads what it has.
	small, err := OpenFolderLog(kinds.Spec{Name: "test", Kind: "log", Scope: "one", Bounds: kinds.Bounds{
		MaxRecordBytes: 500, MaxBytes: 1000, MaxAge: time.Hour}}, l.file, &sync.Mutex{}, RecordStamp)
	must(t, err)
	if ids, _ := readIDs(t, small, "", 9); ids != "a" {
		t.Fatal(ids)
	}
}

// A transcript's entry is kept whole at its largest, and as a notice that says so a byte over
// it, alone, in bulk, and when it waited for the disk.
func TestRecordLimitOfATranscript(t *testing.T) {
	s, err := Open(t.TempDir())
	must(t, err)
	id := NewUUID()
	entry := func(size int) wire.TranscriptEntry {
		e := wire.TranscriptEntry{ID: NewUUID(), At: wire.DateOf(logDay), Kind: wire.TranscriptEntryKind{
			AgentMessage: &wire.TranscriptEntryKindAgentMessage{}}}
		b, err := diskjson.Marshal(e)
		must(t, err)
		e.Kind.AgentMessage.Text = strings.Repeat("x", size-len(b))
		return e
	}
	must(t, errOnly(s.Append(id, entry(transcriptRecord))))
	must(t, errOnly(s.Append(id, entry(transcriptRecord+1))))
	must(t, errOnly(s.AppendMany(id, []wire.TranscriptEntry{entry(transcriptRecord + 1), entry(transcriptRecord)})))
	entries, _, err := s.Transcript(id, 0)
	must(t, err)
	if len(entries) != 4 {
		t.Fatalf("%d entries", len(entries))
	}
	for i, whole := range []bool{true, false, false, true} {
		k := entries[i].Kind
		if whole != (k.AgentMessage != nil) || !whole && (k.Notice == nil || !strings.Contains(k.Notice.Value.Title, "too large to keep")) {
			t.Fatalf("entry %d: whole is %v", i, k.AgentMessage != nil)
		}
	}
}
