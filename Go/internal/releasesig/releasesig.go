// Package releasesig signs and checks a release's SHA256SUMS. SHA256SUMS.sig is the Ed25519
// signature, in base64, of Prefix and the bytes of SHA256SUMS. Each file's name in
// SHA256SUMS holds the release's version, so the signature covers the version too.
//
// release.yml makes the signature (cmd/release-sign). A Linux machine checks it before it
// opens a release (selfupdate_linux.go), the control plane before it unpacks one for an
// install over SSH, and install.sh where the machine has OpenSSL 3
// (docs/how-to/release-agents-host.md).
package releasesig

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// Prefix is signed before SHA256SUMS, so the signature means nothing for another file.
const Prefix = "agents-release/1\n"

// SumsName and SigName are a release's checksums and their signature.
const (
	SumsName = "SHA256SUMS"
	SigName  = SumsName + ".sig"
)

// Limit is the most bytes a signature or a public key file has.
const Limit = 4 << 10

// KeyVariable names the file of one more public key that this account trusts, beside the
// release's own two: for an own build, or a mirror that signs again.
const KeyVariable = "AGENTS_RELEASE_KEY"

// ErrNotSigned is a signature that none of the keys made.
var ErrNotSigned = errors.New("no key that this machine trusts made the signature")

// Verify checks sums against the signature in sig with the keys Keys gives.
func Verify(sums, sig []byte) error {
	keys, err := Keys()
	if err != nil {
		return err
	}
	return verifyWith(keys, sums, sig)
}

func verifyWith(keys []ed25519.PublicKey, sums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return ErrNotSigned
	}
	message := append([]byte(Prefix), sums...)
	for _, key := range keys {
		if ed25519.Verify(key, message, raw) {
			return nil
		}
	}
	return ErrNotSigned
}

// Sign is the contents of SHA256SUMS.sig for sums.
func Sign(key ed25519.PrivateKey, sums []byte) []byte {
	sig := ed25519.Sign(key, append([]byte(Prefix), sums...))
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

// Keys are the public keys trusted here: the release's own, and the one KeyVariable names.
// A key file that cannot be trusted is an error, not one key fewer.
func Keys() ([]ed25519.PublicKey, error) {
	keys, err := builtIn(testing.Testing())
	if err != nil {
		return nil, err
	}
	if path := os.Getenv(KeyVariable); path != "" {
		key, err := ReadPublicKey(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", KeyVariable, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// builtIn are the keys in keys.go. A Go test trusts the test keys in their place, so the
// tests stay the same when the release's keys change. Nothing else trusts a test key, also
// not a build whose keys.go still holds them.
func builtIn(test bool) ([]ed25519.PublicKey, error) {
	texts := []string{CurrentKey, SpareKey}
	if test {
		texts = []string{TestCurrentKey, TestSpareKey}
	}
	var keys []ed25519.PublicKey
	for _, text := range texts {
		if !test && isTestKey(text) {
			continue
		}
		key, err := ParsePublicKey(text)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func isTestKey(text string) bool { return text == TestCurrentKey || text == TestSpareKey }

// CheckRelease says why a build with these two public keys is not to be released.
func CheckRelease(current, spare string) error {
	var parsed []ed25519.PublicKey
	for _, k := range []struct{ name, text string }{{"CurrentKey", current}, {"SpareKey", spare}} {
		key, err := ParsePublicKey(k.text)
		if err != nil {
			return fmt.Errorf("%s: %w", k.name, err)
		}
		// Compared as keys, so another spelling of the same key does not pass.
		for _, test := range []string{TestCurrentKey, TestSpareKey} {
			if t, err := ParsePublicKey(test); err == nil && key.Equal(t) {
				return fmt.Errorf("%s is a test key: put the release's public key in Go/internal/releasesig/keys.go and install.sh (docs/how-to/release-agents-host.md)", k.name)
			}
		}
		parsed = append(parsed, key)
	}
	if parsed[0].Equal(parsed[1]) {
		return errors.New("CurrentKey and SpareKey are the same key")
	}
	return nil
}

// ParsePublicKey reads a key as keys.go has it: base64 of its SubjectPublicKeyInfo, which
// is the middle line of its PEM file.
func ParsePublicKey(text string) (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, errors.New("not a public key in base64")
	}
	return publicKey(der)
}

func publicKey(der []byte) (ed25519.PublicKey, error) {
	any, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, errors.New("not a public key")
	}
	key, ok := any.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an Ed25519 public key")
	}
	return key, nil
}

// PublicText is a public key as keys.go has it.
func PublicText(key ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// ReadPublicKey reads a PEM public key from a file that this account or root owns, and that
// no other account can write.
func ReadPublicKey(path string) (ed25519.PublicKey, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the account's own choice of key file
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("%s belongs to another account", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("another account can write %s", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, Limit))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("%s holds no PEM public key", path)
	}
	return publicKey(block.Bytes)
}

// ParsePrivateKey reads a PEM private key (PKCS #8), as NewPrivateKey writes it.
func ParsePrivateKey(b []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PEM private key")
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("not a private key")
	}
	key, ok := any.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 private key")
	}
	return key, nil
}

// NewPrivateKey makes a key, and its PEM file.
func NewPrivateKey() (ed25519.PrivateKey, []byte, error) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// CheckSums says why sums is not the checksums of release version: a line that is not a
// checksum and a file, a file without the version in its name, or no Linux host.
func CheckSums(sums []byte, version string) error {
	var names []string
	for line := range strings.Lines(string(sums)) {
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 || strings.Trim(f[0], "0123456789abcdef") != "" {
			return fmt.Errorf("not a line of %s: %q", SumsName, line)
		}
		name := strings.TrimPrefix(f[1], "*")
		if !strings.Contains(name, "-"+version+".") || slices.Contains(names, name) {
			return fmt.Errorf("%s names %s, which is not one file of release %s", SumsName, name, version)
		}
		names = append(names, name)
	}
	for _, arch := range []string{"x86_64", "aarch64"} {
		if want := "agentsd-linux-" + arch + "-" + version + ".tar.gz"; !slices.Contains(names, want) {
			return fmt.Errorf("%s does not name %s", SumsName, want)
		}
	}
	return nil
}
