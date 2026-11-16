package envfile

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseReadsNamesValuesAndDescriptions(t *testing.T) {
	f := Parse([]byte("# The region we deploy to\nREGION=eu-west-1\n\nexport QUOTED=\"two words\"\nSINGLE='a#b'\n# not above anything\n\nBARE=x\nnot a line\n1BAD=x\nREGION=us-east-1\n"))
	if f.Problem != "" {
		t.Fatal(f.Problem)
	}
	got := map[string]Variable{}
	for _, v := range f.Variables {
		got[v.Name] = v
	}
	if len(f.Variables) != 4 {
		t.Fatalf("variables %+v", f.Variables)
	}
	if v := got["REGION"]; v.Value != "us-east-1" || v.Line != 11 || v.Description != "" {
		t.Errorf("the later REGION wins, with no description: %+v", v)
	}
	if v := got["QUOTED"]; v.Value != "two words" {
		t.Errorf("export and quotes are taken off: %+v", v)
	}
	if v := got["SINGLE"]; v.Value != "a#b" {
		t.Errorf("single quotes: %+v", v)
	}
	if v := got["BARE"]; v.Description != "" {
		t.Errorf("a comment after a blank line describes nothing: %+v", v)
	}
}

func TestParseDescription(t *testing.T) {
	f := Parse([]byte("# Where the API is\nAPI_URL=https://example.test\n"))
	if len(f.Variables) != 1 || f.Variables[0].Description != "Where the API is" || f.Variables[0].Line != 2 {
		t.Fatalf("%+v", f.Variables)
	}
}

func TestParseBounds(t *testing.T) {
	if f := Parse([]byte(strings.Repeat("A=1\n", FileMost/4+1))); f.Problem == "" || len(f.Variables) != 0 {
		t.Errorf("a file over 64 KB is not read: %q", f.Problem)
	}
	var b strings.Builder
	for i := range VariablesMost + 1 {
		fmt.Fprintf(&b, "V_%d=1\n", i)
	}
	if f := Parse([]byte(b.String())); f.Problem == "" {
		t.Error("a file with over 500 variables is not read")
	}
}

func ptr(s string) *string { return &s }

func TestEditKeepsOtherLines(t *testing.T) {
	in := "# Top comment\n\n# The region\nREGION=eu\nOTHER=1 # trailing\n"
	got := string(Edit([]byte(in), "REGION", ptr("us"), nil))
	if got != "# Top comment\n\n# The region\nREGION=us\nOTHER=1 # trailing\n" {
		t.Errorf("change keeps the description and the rest:\n%s", got)
	}
	got = string(Edit([]byte(in), "REGION", ptr("us"), ptr("Where we run")))
	if got != "# Top comment\n\n# Where we run\nREGION=us\nOTHER=1 # trailing\n" {
		t.Errorf("a new description replaces the old:\n%s", got)
	}
	got = string(Edit([]byte(in), "REGION", nil, nil))
	if got != "# Top comment\n\nOTHER=1 # trailing\n" {
		t.Errorf("remove takes its description too:\n%s", got)
	}
	got = string(Edit([]byte(in), "NEW", ptr("a b"), ptr("A new one")))
	if got != in+"\n# A new one\nNEW='a b'\n" {
		t.Errorf("add goes at the end:\n%s", got)
	}
	if v := Parse([]byte(got)).Variables; v[len(v)-1].Value != "a b" {
		t.Errorf("a quoted value reads back: %+v", v)
	}
	if got := Edit([]byte("ONLY=1\n"), "ONLY", nil, nil); got != nil {
		t.Errorf("the last variable removed leaves an empty file: %q", got)
	}
	if got := string(Edit(nil, "A", ptr("1"), nil)); got != "A=1\n" {
		t.Errorf("a new file: %q", got)
	}
	if got := string(Edit([]byte("A=1\nB=2\nA=3\n"), "A", ptr("4"), nil)); got != "B=2\nA=4\n" {
		t.Errorf("one line per name after a change: %q", got)
	}
}

func TestSecretWords(t *testing.T) {
	for _, c := range []struct {
		name, value string
		secret      bool
	}{
		{"REGION", "eu-west-1", false},
		{"GITHUB_TOKEN", "abc", true},
		{"API_KEY", "", false},
		{"SOMETHING", "ghp_" + strings.Repeat("a", 36), true},
		{"LOG_LEVEL", "debug", false},
	} {
		if got := SecretWords(c.name, c.value) != ""; got != c.secret {
			t.Errorf("%s=%s: secret %v, want %v", c.name, c.value, got, c.secret)
		}
	}
}
