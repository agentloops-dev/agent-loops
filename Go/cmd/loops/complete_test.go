package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestTabIsAKeyAtThePrompt(t *testing.T) {
	var r keyReader
	got := r.feed([]byte("a\tb\t"))
	want := []key{{kind: keyText, text: "a"}, {kind: keyTab}, {kind: keyText, text: "b"}, {kind: keyTab}}
	if len(got) != len(want) {
		t.Fatalf("Tab is its own key: %v", got)
	}
	for i := range want {
		if got[i].kind != want[i].kind || got[i].text != want[i].text {
			t.Fatalf("Tab is its own key: %v", got)
		}
	}
}

func TestCompletionQueryAtTheCursor(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		cursor int
		want   string
	}{
		{"slash", "/mo", 3, "/:0:mo"},
		{"slash after words", "say /mo", 7, "/:4:mo"},
		{"slash at the cursor", "say /mo now", 7, "/:4:mo"},
		{"file", "@RE", 3, "@:0:RE"},
		{"last mention", "/help @RE", 9, "@:6:RE"},
		{"not inside a word", "url/x", 5, ""},
		{"not after a term", "/mode fast", 10, ""},
		{"not in an email", "mail a@b", 8, ""},
		{"no mention", "hello", 5, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := lineEditor{text: c.text, cursor: c.cursor}
			got := editorCompletionKey(e)
			if got != c.want {
				t.Fatalf("%q at %d completes %q, want %q", c.text, c.cursor, got, c.want)
			}
		})
	}
}

func TestSlashTabCompletion(t *testing.T) {
	f := &live{sc: &screen{prompt: true}, commands: []wire.SlashCommand{
		{Name: "compact", Description: ptr("Compact the conversation")},
	}}
	f.sc.input.set("/hel")
	if err := f.key(context.Background(), key{kind: keyTab}); err != nil {
		t.Fatal(err)
	}
	if f.sc.input.text != "/help " || f.sc.input.cursor != len("/help ") {
		t.Fatalf("one match completes with a space: %q at %d", f.sc.input.text, f.sc.input.cursor)
	}
	if len(f.sc.completions) != 0 {
		t.Fatalf("a unique completion closes the list: %+v", f.sc.completions)
	}

	f.sc.input.set("/mo")
	if err := f.key(context.Background(), key{kind: keyTab}); err != nil {
		t.Fatal(err)
	}
	if f.sc.input.text != "/mode" {
		t.Fatalf("the common prefix is inserted: %q", f.sc.input.text)
	}
	if len(f.sc.completions) != 2 || f.sc.completionShown {
		t.Fatalf("both commands wait, without showing yet: %+v", f.sc.completions)
	}
	if strings.Contains(strings.Join(f.sc.live(), "\n"), "/model") {
		t.Fatal("the first Tab does not show the matches")
	}
	if err := f.key(context.Background(), key{kind: keyTab}); err != nil {
		t.Fatal(err)
	}
	if !f.sc.completionShown || f.sc.input.text != "/mode" {
		t.Fatalf("a second Tab shows the matches: %q, shown %v", f.sc.input.text, f.sc.completionShown)
	}
	lines := strings.Join(f.sc.live(), "\n")
	for _, want := range []string{"/mode", "/model", "show or set the permission mode"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("no %q in the shown matches:\n%s", want, lines)
		}
	}
	if err := f.key(context.Background(), key{kind: keyText, text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(f.sc.completions) != 0 {
		t.Fatal("editing the prompt clears the matches")
	}
}

func TestFileTabCompletionInAFolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := newChatFake()
	f := &live{c: fake, p: place{machine: wire.ControlMachine{ID: "H"}, dir: dir}, sc: &screen{}}
	f.sc.input.set("@REA")
	if err := f.key(context.Background(), key{kind: keyTab}); err != nil {
		t.Fatal(err)
	}
	if f.sc.input.text != "@README.md " {
		t.Fatalf("one file completes with a space: %q", f.sc.input.text)
	}
	if len(f.attachments) != 1 {
		t.Fatalf("the file is attached: %+v", f.attachments)
	}
	a := f.attachments[0].attachment
	if a.Block.Type != "resource_link" || a.Block.URI == nil || *a.Block.URI != string(store.PathURL(path)) ||
		a.DisplayName != "README.md" || a.ByteCount == nil || *a.ByteCount != 5 {
		t.Fatalf("the attachment is a resource link to the file: %+v", a)
	}

	f.id = wire.UUID("B21E9000-0000-0000-0000-000000000000")
	if err := f.send(context.Background(), "read this"); err != nil {
		t.Fatal(err)
	}
	calls := fake.called("agents/prompt")
	if len(calls) != 1 {
		t.Fatalf("one prompt was sent: %d", len(calls))
	}
	req := calls[0].(wire.PromptRequest)
	if len(req.Attachments) != 1 || req.Attachments[0].DisplayName != "README.md" {
		t.Fatalf("the prompt carries the file: %+v", req)
	}
	if len(f.attachments) != 0 {
		t.Fatalf("the attachment is used once: %+v", f.attachments)
	}
}

func TestFileTabCompletionSharesAPrefixAndShowsMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"report.go", "report.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := &live{p: place{dir: dir}, sc: &screen{prompt: true}}
	f.sc.input.set("@rep")
	if err := f.key(context.Background(), key{kind: keyTab}); err != nil {
		t.Fatal(err)
	}
	if f.sc.input.text != "@report." {
		t.Fatalf("the common path prefix is inserted: %q", f.sc.input.text)
	}
	if len(f.sc.completions) != 2 || len(f.attachments) != 0 {
		t.Fatalf("two files wait; neither is attached: %+v, %+v", f.sc.completions, f.attachments)
	}
	if err := f.key(context.Background(), key{kind: keyTab}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(f.sc.live(), "\n")
	for _, want := range []string{"report.go", "report.md"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("no %q in the shown matches:\n%s", want, lines)
		}
	}
}

func TestFileTabCompletionUsesTheSessionFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := wire.UUID("C21E9000-0000-0000-0000-000000000000")
	fake := newChatFake()
	fake.answer[wire.CallFilesMention.Name] = func(params any) any {
		req := params.(wire.FileMentionRequest)
		if req.AgentID != id || req.Term != "not" {
			t.Errorf("the mention call: %+v", req)
		}
		return []wire.FileMentionDTO{{Path: path, RelativePath: "notes.txt"}}
	}
	f := &live{c: fake, p: place{machine: wire.ControlMachine{ID: "H"}}, sc: &screen{}}
	f.id = id
	f.sc.input.set("@not")
	if err := f.key(context.Background(), key{kind: keyTab}); err != nil {
		t.Fatal(err)
	}
	if f.sc.input.text != "@notes.txt " || len(f.attachments) != 1 {
		t.Fatalf("the session's file completes: %q, %+v", f.sc.input.text, f.attachments)
	}
	if len(fake.called(wire.CallFilesMention.Name)) != 1 {
		t.Fatal("the session was asked for file matches")
	}
}

func TestChangedRefreshesSlashCommands(t *testing.T) {
	f := &live{}
	f.commands = []wire.SlashCommand{{Name: "old"}}
	f.changed(wire.Agent{State: wire.AgentStateRunning, AvailableCommands: []wire.SlashCommand{{Name: "compact"}}})
	if len(f.commands) != 1 || f.commands[0].Name != "compact" {
		t.Fatalf("the runtime's commands replace the old ones: %+v", f.commands)
	}
}
