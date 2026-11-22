package providers

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestRedaction(t *testing.T) {
	redact := NewRedaction(map[string]string{"TOKEN": "dop_v1_abc/def+ghi", "PIN": "1234", "EMPTY": ""})
	for in, want := range map[string]string{
		"Bearer dop_v1_abc/def+ghi failed": "Bearer ${TOKEN} failed",
		"t=dop_v1_abc%2Fdef%2Bghi":         "t=${TOKEN}",
		"Basic " + base64.StdEncoding.EncodeToString([]byte("dop_v1_abc/def+ghi")): "Basic ${TOKEN}",
		"pin 1234 at port 12345": "pin ${PIN} at port 12345",
		"nothing here":           "nothing here",
	} {
		if got := redact.Text(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	got := redact.Value(mustParse(`{"a": ["dop_v1_abc/def+ghi", 3], "dop_v1_abc/def+ghi": true}`))
	if !jsonEqual(got, mustParse(`{"a": ["${TOKEN}", 3], "${TOKEN}": true}`)) {
		t.Errorf("%v", got)
	}
}

func TestSecretsAreLaidOverAndTakenAway(t *testing.T) {
	home := t.TempDir()
	s := NewSecrets(home)
	if err := s.Set("p", map[string]string{"A": "1", "B": "2"}); err != nil {
		t.Fatal(err)
	}
	must(t, s.Set("p", map[string]string{"A": "", "C": "3"}))
	again := NewSecrets(home)
	if v := again.Values("p"); len(v) != 2 || v["B"] != "2" || v["C"] != "3" {
		t.Errorf("%v", v)
	}
	must(t, again.Remove("p"))
	if v := NewSecrets(home).Values("p"); len(v) != 0 {
		t.Errorf("%v", v)
	}
	if info, err := os.Stat(filepath.Join(home, SecretsFileName)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("%v", err)
	}
	// As Swift writes it.
	must(t, os.WriteFile(filepath.Join(home, SecretsFileName), []byte("{\n  \"q\" : {\n    \"T\" : \"x\\/y\"\n  }\n}"), 0o600))
	if NewSecrets(home).Values("q")["T"] != "x/y" {
		t.Error("Swift's file not read")
	}
}
