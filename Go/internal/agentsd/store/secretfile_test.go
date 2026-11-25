package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretFileRefusesAndOpensOverItsLimits(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "s", "f.json")
	f := SecretFile[map[string]string]{Name: "f.json", Parts: []SecretPart[map[string]string]{
		{What: "things", MaxRecordBytes: 20, MaxCount: 2, Remedy: "remove one first", Entries: Entries[string]}}}
	write := func(m map[string]string) error { return f.Write(path, m) }
	ok := func(m map[string]string) {
		t.Helper()
		if err := write(m); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(m map[string]string) {
		t.Helper()
		before, _ := os.ReadFile(path)
		err := write(m)
		if !errors.Is(err, ErrLimit) {
			t.Fatalf("not refused: %v", err)
		}
		if strings.Contains(err.Error(), "KEY") || strings.Contains(err.Error(), "xxxx") {
			t.Fatal("a refusal holds a name or a value")
		}
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Fatal("a refused write changed the file")
		}
	}
	long := strings.Repeat("x", 40)
	ok(map[string]string{"KEY1": "a", "KEY2": strings.Repeat("x", 14)})
	refused(map[string]string{"KEY1": "a", "KEY2": strings.Repeat("x", 15)})
	refused(map[string]string{"KEY1": "a", "KEY2": "b", "KEY3": "c"})
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("not private")
	}

	// Over both limits already: it opens, changes and gets smaller, and does not grow.
	if err := os.WriteFile(path, []byte(`{"KEY1":"a","KEY2":"b","KEY3":"`+long+`","KEY4":"d"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := f.Read(path)
	if err != nil || len(m) != 4 {
		t.Fatalf("%d %v", len(m), err)
	}
	m["KEY1"] = "e"
	ok(m)
	refused(map[string]string{"KEY1": "e", "KEY2": "b", "KEY3": long + "x", "KEY4": "d"})
	ok(map[string]string{"KEY1": "e", "KEY3": long[:30], "KEY4": "d"})
	refused(map[string]string{"KEY1": "e", "KEY3": "c", "KEY4": "d", "KEY5": "f"})
	ok(nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an empty store keeps its file")
	}

	// A file that does not read is not written over.
	if err := os.WriteFile(path, []byte("KEY1 xxxx"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := write(map[string]string{"KEY1": "a"}); !errors.Is(err, ErrUndecodable) || strings.Contains(err.Error(), "KEY1") {
		t.Fatalf("%v", err)
	}
}
