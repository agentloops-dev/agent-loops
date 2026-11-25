package releasesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T, name string) ed25519.PrivateKey {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "test-"+name+".pem")) //nolint:gosec // G304: a test's own data
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newPublic(t *testing.T) string {
	t.Helper()
	k, _, err := NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	text, err := PublicText(k.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestEitherKeySigns(t *testing.T) {
	t.Setenv(KeyVariable, "")
	sums := []byte("sums\n")
	for _, name := range []string{"current", "spare"} {
		if err := Verify(sums, Sign(testKey(t, name), sums)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	sig := Sign(testKey(t, "current"), sums)
	if Verify([]byte("sums"), sig) == nil || Verify(sums, nil) == nil || Verify(sums, sig[:40]) == nil {
		t.Error("a wrong signature was good")
	}
	bare := base64.StdEncoding.EncodeToString(ed25519.Sign(testKey(t, "current"), sums))
	if Verify(sums, []byte(bare)) == nil {
		t.Error("a signature of the bytes without the prefix was good")
	}
}

// Only a Go test trusts the test keys.
func TestABuildTrustsNoTestKey(t *testing.T) {
	keys, err := builtIn(false)
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte("sums\n")
	for _, name := range []string{"current", "spare"} {
		if verifyWith(keys, sums, Sign(testKey(t, name), sums)) == nil {
			t.Errorf("the %s test key was trusted", name)
		}
	}
}

func TestCheckRelease(t *testing.T) {
	a, b := newPublic(t), newPublic(t)
	if err := CheckRelease(a, b); err != nil {
		t.Fatal(err)
	}
	for _, k := range [][2]string{{TestCurrentKey, b}, {a, TestSpareKey}, {TestSpareKey, b}, {a, a}, {"", b}, {a, "x"},
		{TestCurrentKey + "\n", b}} {
		if CheckRelease(k[0], k[1]) == nil {
			t.Errorf("%v passed", k)
		}
	}
}

func TestReadPublicKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key.pem")
	write := func(text string, mode os.FileMode) {
		t.Helper()
		_ = os.Remove(file)
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
	}
	good := "-----BEGIN PUBLIC KEY-----\n" + newPublic(t) + "\n-----END PUBLIC KEY-----\n"
	write(good, 0o644)
	if _, err := ReadPublicKey(file); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o664, 0o646} {
		write(good, mode)
		if _, err := ReadPublicKey(file); err == nil {
			t.Errorf("%o passed", mode)
		}
	}
	write("no key", 0o644)
	t.Setenv(KeyVariable, file)
	// A key file that cannot be read is an error, also for what the built-in keys signed.
	if Verify([]byte("s"), Sign(testKey(t, "current"), []byte("s"))) == nil {
		t.Error("a bad key file was left out")
	}
	if _, err := ReadPublicKey(filepath.Dir(file)); err == nil {
		t.Error("a folder passed")
	}
}

func TestCheckSums(t *testing.T) {
	sum := strings.Repeat("a", 64)
	good := sum + "  agentsd-linux-x86_64-0.2.0.tar.gz\n" + sum + "  agentsd-linux-aarch64-0.2.0.tar.gz\n" + sum + "  Agent-Loops-0.2.0.zip\n"
	if err := CheckSums([]byte(good), "0.2.0"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", good + sum + "  Agent-Loops-0.1.0.zip\n", good + "short  a-0.2.0.zip\n", good + strings.Repeat("z", 64) + "  a-0.2.0.zip\n",
		good + sum + "  Agent-Loops-0.2.0.zip\n", strings.Replace(good, "aarch64", "riscv64", 1)} {
		if CheckSums([]byte(bad), "0.2.0") == nil {
			t.Errorf("%q passed", bad)
		}
	}
}
