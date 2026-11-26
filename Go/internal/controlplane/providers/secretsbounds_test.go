package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #7249: the provider secrets have limits, refuse with no value in the words, and lose no key.
func TestProviderSecretsHaveLimitsAndKeepWhatTheyHave(t *testing.T) {
	home := t.TempDir()
	s := NewSecrets(home)
	at := secretsRecordBytes - len("p") - len(`{"T":""}`)
	must(t, s.Set("p", map[string]string{"T": strings.Repeat("a", at)}))
	err := s.Set("p", map[string]string{"T": strings.Repeat("a", at+1)})
	if err == nil || strings.Contains(err.Error(), "aaa") {
		t.Fatal(err)
	}
	if v := NewSecrets(home).Values("p")["T"]; len(v) != at {
		t.Fatalf("the earlier value is %d bytes", len(v))
	}
	// A refreshed token is kept whatever its size, and can shrink after.
	must(t, s.Renew("p", map[string]string{"T": strings.Repeat("b", at+10)}))
	must(t, s.Set("p", map[string]string{"T": strings.Repeat("b", at+5)}))
	must(t, s.Set("q", map[string]string{"T": "1"}))
	for i := 2; i < secretsMost; i++ {
		must(t, s.Set(fmt.Sprint("p", i), map[string]string{"T": "1"}))
	}
	if s.Set("more", map[string]string{"T": "1"}) == nil {
		t.Fatal("one more provider than the count was kept")
	}
	must(t, s.Remove("p"))
	must(t, s.Set("more", map[string]string{"T": "1"}))
	// A file that does not read is not written over.
	path := filepath.Join(home, SecretsFileName)
	must(t, os.WriteFile(path, []byte("{torn"), 0o600))
	s.Reload()
	if s.Set("p", map[string]string{"T": "1"}) == nil {
		t.Fatal("a file that does not read was written over")
	}
	if b, _ := os.ReadFile(path); string(b) != "{torn" {
		t.Fatalf("%q", b)
	}
}
