package main

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/releasesig"
)

const testKeyFile = "../../internal/releasesig/testdata/test-current.pem"

func sumsFile(t *testing.T) string {
	t.Helper()
	t.Setenv("AGENTS_RELEASE_KEY", "")
	t.Setenv("RELEASE_SIGNING_KEY", "")
	sum := strings.Repeat("a", 64)
	file := filepath.Join(t.TempDir(), "SHA256SUMS")
	text := sum + "  agentsd-linux-x86_64-0.2.0.tar.gz\n" + sum + "  agentsd-linux-aarch64-0.2.0.tar.gz\n"
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestSignAndVerify(t *testing.T) {
	sums := sumsFile(t)
	var out bytes.Buffer
	if err := run([]string{"sign", "0.2.0", sums}, &out); err == nil {
		t.Fatal("signed with no key")
	}
	if err := run([]string{"sign", "-key", testKeyFile, "0.3.0", sums}, &out); err == nil {
		t.Fatal("signed as another version")
	}
	key, err := os.ReadFile(testKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELEASE_SIGNING_KEY", string(key))
	if err := run([]string{"sign", "0.2.0", sums}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", sums}, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sums, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", sums}, &out); err == nil {
		t.Fatal("changed sums were good")
	}
}

func TestNewKeyShowsOnlyThePublicHalf(t *testing.T) {
	sums := sumsFile(t)
	file := filepath.Join(t.TempDir(), "key.pem")
	var out, public bytes.Buffer
	if err := run([]string{"new", file}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := releasesig.ParsePublicKey(strings.TrimSpace(out.String())); err != nil || strings.Contains(out.String(), "PRIVATE") {
		t.Fatalf("%v %q", err, out.String())
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if err := run([]string{"new", file}, &out); err == nil {
		t.Fatal("a key was written over")
	}
	if err := run([]string{"public", "-pem", file}, &public); err != nil {
		t.Fatal(err)
	}
	// A key that is not this build's signs only with -own, and its public half checks it.
	if err := run([]string{"sign", "-key", file, "0.2.0", sums}, &out); err == nil {
		t.Fatal("signed with a key that is not this build's")
	}
	if _, err := os.Stat(sums + ".sig"); err == nil {
		t.Fatal("a signature was written")
	}
	if err := run([]string{"sign", "-key", file, "-own", "0.2.0", sums}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", sums}, &out); err == nil {
		t.Fatal("trusted without AGENTS_RELEASE_KEY")
	}
	pub := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(pub, public.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTS_RELEASE_KEY", pub)
	if err := run([]string{"verify", sums}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestTheSecretIsTheKeyOfABuild(t *testing.T) {
	t.Setenv("RELEASE_SIGNING_KEY", "")
	if checkSecret(releasesig.TestCurrentKey) == nil {
		t.Fatal("no secret passed")
	}
	key, err := os.ReadFile(testKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELEASE_SIGNING_KEY", string(key))
	if err := checkSecret(releasesig.TestSpareKey, releasesig.TestCurrentKey); err != nil {
		t.Fatal(err)
	}
	if checkSecret(releasesig.TestSpareKey) == nil {
		t.Fatal("another key's private half passed")
	}
}

// release.yml's first job runs check: it passes only with the release's own keys.
func TestCheckSaysWhatTheKeysAre(t *testing.T) {
	err := run([]string{"check"}, &bytes.Buffer{})
	current, parse := releasesig.ParsePublicKey(releasesig.CurrentKey)
	if parse != nil {
		t.Fatal(parse)
	}
	test, _ := releasesig.ParsePublicKey(releasesig.TestCurrentKey)
	if current.Equal(ed25519.PublicKey(test)) && (err == nil || !strings.Contains(err.Error(), "test key")) {
		t.Fatalf("%v", err)
	}
}
