package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func foldersFake(t *testing.T, add func(string, any) (any, error)) *fake {
	t.Helper()
	return &fake{answer: map[string]func(string, any) (any, error){
		"machines/list": func(string, any) (any, error) {
			return []wire.ControlMachine{{ID: "h1", Name: "alex-mac"}}, nil
		},
		"folders/add": add,
	}}
}

func TestFoldersAddSaysWhatItDid(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "src", "billing")
	f := foldersFake(t, func(string, any) (any, error) {
		return wire.FolderSummary{Name: "billing", Folder: wire.Folder{Folder: wire.URL("file://" + dir)}}, nil
	})
	var say bytes.Buffer
	if err := addFolder(t.Context(), &say, f, options{machine: "alex-mac"}, dir); err != nil {
		t.Fatal(err)
	}
	if want := "Added ~/src/billing as a folder on alex-mac.\n"; say.String() != want {
		t.Fatalf("add: %q, want %q", say.String(), want)
	}
}

func TestFoldersAddRefusalChangesNothing(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "src", "billing")
	f := foldersFake(t, func(string, any) (any, error) {
		return nil, errors.New("it is in the folder ~/src already.")
	})
	var say bytes.Buffer
	err = addFolder(t.Context(), &say, f, options{machine: "alex-mac"}, dir)
	want := "Cannot add ~/src/billing: it is in the folder ~/src already.\nNothing changed."
	if codeOf(err) != exitError || err.Error() != want {
		t.Fatalf("refusal: %v, want %q", err, want)
	}
	if say.Len() != 0 {
		t.Fatalf("a refusal says nothing on stderr: %q", say.String())
	}
}

func TestFoldersAddNeedsPairing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := run(t.Context(), []string{"folders", "add"}, &bytes.Buffer{})
	if codeOf(err) != exitNotPair || !strings.Contains(err.Error(), "loops auth login '<code>'") {
		t.Fatalf("not paired: %v", err)
	}
}
