package main

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The frame self-registers today's commands; the root does not change when one is added.
func TestRootRegistersTodaysCommands(t *testing.T) {
	root := newApp(context.Background(), io.Discard).root()
	var got []string
	for _, c := range root.Commands() {
		if c.Name() != "help" {
			got = append(got, c.Name())
		}
	}
	sort.Strings(got)
	want := []string{"auth", "folders", "ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands: %v, want %v", got, want)
	}
	var b bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, &b); err != nil {
		t.Fatal(err)
	}
	help := b.String()
	for _, s := range []string{"USAGE", "COMMANDS", "GLOBAL FLAGS", "EXAMPLES"} {
		if !strings.Contains(help, s) {
			t.Errorf("help is missing %s:\n%s", s, help)
		}
	}
	for _, future := range []string{"config", "completion", "-o, --output", "--profile", "--filter", "--limit"} {
		if strings.Contains(help, future) {
			t.Errorf("help has a noun or flag that does not exist yet: %s", future)
		}
	}
	for _, flag := range []string{"--folder", "--machine", "--control", "--all", "--runtime", "--mode", "--debug", "-c, --continue", "-r, --resume", "-p, --print", "--detach", "--json", "--on-ask", "--timeout"} {
		if !strings.Contains(help, flag) {
			t.Errorf("help is missing today's flag %s", flag)
		}
	}
}

func TestCommandsSayTheirOwnShape(t *testing.T) {
	for _, tc := range []struct {
		args []string
		help []string
		msg  string
	}{
		{[]string{"auth"}, []string{"auth", "--help"}, "loops auth <login|logout|status>"},
		{[]string{"folders"}, []string{"folders", "add", "--help"}, "loops folders add [dir]"},
	} {
		err := run(context.Background(), tc.args, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.msg)
		}
		var b bytes.Buffer
		if err := run(context.Background(), tc.help, &b); err != nil || !strings.Contains(b.String(), tc.msg) {
			t.Errorf("%v: %v\n%s", tc.help, err, b.String())
		}
	}
}
