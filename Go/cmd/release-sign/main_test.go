package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
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

func TestTheKeyIsTheKeyOfABuild(t *testing.T) {
	if err := checkKey(testKeyFile, releasesig.TestSpareKey, releasesig.TestCurrentKey); err != nil {
		t.Fatal(err)
	}
	if checkKey(testKeyFile, releasesig.TestSpareKey) == nil || checkKey("main.go", releasesig.TestCurrentKey) == nil {
		t.Fatal("another key passed")
	}
	// A test key is never the key of a release, which has the test keys in the same file.
	if check(testKeyFile) == nil {
		t.Fatal("a test key passed")
	}
}

func TestFilesAreTheBuildsAndNoOther(t *testing.T) {
	names := []string{"agentsd-linux-x86_64-0.2.0.tar.gz", "agentsd-linux-aarch64-0.2.0.tar.gz", "Agent-Loops-0.2.0.zip"}
	for name, change := range map[string]func(dir string) error{
		"good":     func(string) error { return nil },
		"no file":  func(dir string) error { return os.Remove(filepath.Join(dir, names[0])) },
		"one more": func(dir string) error { return os.WriteFile(filepath.Join(dir, "x-0.2.0.zip"), nil, 0o600) },
		"changed":  func(dir string) error { return os.WriteFile(filepath.Join(dir, names[1]), nil, 0o600) },
		"a link": func(dir string) error {
			p := filepath.Join(dir, names[2])
			return errors.Join(os.Rename(p, p+"-"), os.Symlink(p+"-", p))
		},
		"other sums": func(dir string) error {
			p := filepath.Join(dir, "SHA256SUMS")
			b, err := os.ReadFile(p) //nolint:gosec // G304: a test's own file
			return errors.Join(err, os.WriteFile(p, append(b, '\n'), 0o600)) //nolint:gosec // G703: a test's own file
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			sums := ""
			for _, n := range names {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o600); err != nil {
					t.Fatal(err)
				}
				sums += fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(n)), n)
			}
			file := filepath.Join(dir, "SHA256SUMS")
			if err := os.WriteFile(file, []byte(sums), 0o600); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(sums)))
			if err := change(dir); err != nil {
				t.Fatal(err)
			}
			if err := run([]string{"files", digest, "0.2.0", file}, &bytes.Buffer{}); (err == nil) != (name == "good") {
				t.Fatal(err)
			}
		})
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
