package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFolderStatsCountsRegularFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "two"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, size, err := NewFolder(root).Stats()
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || size != 8 {
		t.Fatalf("got %d files, %d bytes", files, size)
	}
}
