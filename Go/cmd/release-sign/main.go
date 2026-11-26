// release-sign makes and checks the signature of a release's SHA256SUMS
// (docs/how-to/release-agents-host.md).
//
//	release-sign new FILE                  make a key pair: the private half in FILE, the public half shown
//	release-sign public [-pem] FILE        show the public half of the private key in FILE
//	release-sign check [-key FILE]         fail when this build's public keys are not the release's, or
//	                                       the private key in FILE is not the half of one
//	release-sign files DIGEST VERSION SHA256SUMS
//	                                       fail unless SHA256SUMS has the SHA-256 DIGEST, and the folder
//	                                       beside it holds the files it lists and no other
//	release-sign sign [-key FILE] [-own] VERSION SHA256SUMS
//	                                       write SHA256SUMS.sig; with no -key, the private key is in
//	                                       RELEASE_SIGNING_KEY; -own is a key that is not this build's
//	release-sign verify SHA256SUMS         check SHA256SUMS against SHA256SUMS.sig
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/linuxrelease"
	"github.com/agentloops-dev/agent-loops/Go/internal/releasesig"
)

const usage = "usage: release-sign new FILE | public [-pem] FILE | check [-key FILE] | files DIGEST VERSION SHA256SUMS | sign [-key FILE] [-own] VERSION SHA256SUMS | verify SHA256SUMS"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release-sign:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	asPEM := flags.Bool("pem", false, "show a PEM file")
	keyFile := flags.String("key", "", "the private key's file")
	own := flags.Bool("own", false, "a key that is not this build's")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch rest := flags.Args(); {
	case args[0] == "new" && len(rest) == 1:
		return newKey(rest[0], out)
	case args[0] == "public" && len(rest) == 1:
		return public(rest[0], *asPEM, out)
	case args[0] == "check" && len(rest) == 0:
		return check(*keyFile)
	case args[0] == "files" && len(rest) == 3:
		return checkFiles(rest[0], rest[1], rest[2])
	case args[0] == "sign" && len(rest) == 2:
		return sign(*keyFile, *own, rest[0], rest[1], out)
	case args[0] == "verify" && len(rest) == 1:
		return verify(rest[0], out)
	}
	return errors.New(usage)
}

func check(keyFile string) error {
	if err := releasesig.CheckRelease(releasesig.CurrentKey, releasesig.SpareKey); err != nil || keyFile == "" {
		return err
	}
	return checkKey(keyFile, releasesig.CurrentKey, releasesig.SpareKey)
}

func checkKey(keyFile string, keys ...string) error {
	text, err := os.ReadFile(keyFile) //nolint:gosec // G304: the file the person named
	if err != nil {
		return err
	}
	key, err := releasesig.ParsePrivateKey(text)
	if err != nil {
		return err
	}
	public, err := releasesig.PublicText(key.Public().(ed25519.PublicKey))
	if err != nil {
		return err
	}
	if !slices.Contains(keys, public) {
		return errors.New("this is not the private half of one of this build's public keys")
	}
	return nil
}

// checkFiles is what the build said of SHA256SUMS, and what SHA256SUMS says of its folder.
func checkFiles(digest, version, sumsFile string) error {
	sums, err := os.ReadFile(sumsFile) //nolint:gosec // G304: the file the person named
	if err != nil {
		return err
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(sums)); got != digest {
		return fmt.Errorf("%s is not the build's: its SHA-256 is %s, not %q", sumsFile, got, digest)
	}
	if err := releasesig.CheckSums(sums, version); err != nil {
		return err
	}
	listed := map[string]string{filepath.Base(sumsFile): digest}
	for line := range strings.Lines(string(sums)) {
		f := strings.Fields(line)
		listed[strings.TrimPrefix(f[1], "*")] = f[0]
	}
	dir := filepath.Dir(sumsFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		want, ok := listed[e.Name()]
		if !ok || !e.Type().IsRegular() {
			return fmt.Errorf("%s is not a file that %s lists", e.Name(), sumsFile)
		}
		if got := linuxrelease.FileSum(filepath.Join(dir, e.Name())); got != want {
			return fmt.Errorf("%s does not match its checksum", e.Name())
		}
		delete(listed, e.Name())
	}
	if len(listed) > 0 {
		return fmt.Errorf("%d of the files that %s lists are not there", len(listed), sumsFile)
	}
	return nil
}

func newKey(path string, out io.Writer) error {
	key, text, err := releasesig.NewPrivateKey()
	if err != nil {
		return err
	}
	// store-bounds: not a store: one small file where the person named it
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the file the person named
	if err != nil {
		return err
	}
	_, err = f.Write(text)
	if err := errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	line, err := releasesig.PublicText(key.Public().(ed25519.PublicKey))
	if err == nil {
		_, err = fmt.Fprintln(out, line)
	}
	return err
}

func public(path string, asPEM bool, out io.Writer) error {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the file the person named
	if err != nil {
		return err
	}
	key, err := releasesig.ParsePrivateKey(b)
	if err != nil {
		return err
	}
	if !asPEM {
		line, err := releasesig.PublicText(key.Public().(ed25519.PublicKey))
		if err == nil {
			_, err = fmt.Fprintln(out, line)
		}
		return err
	}
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return err
	}
	return pem.Encode(out, &pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func sign(keyFile string, own bool, version, sumsFile string, out io.Writer) error {
	text := []byte(os.Getenv("RELEASE_SIGNING_KEY"))
	if keyFile != "" {
		var err error
		if text, err = os.ReadFile(keyFile); err != nil { //nolint:gosec // G304: the file the person named
			return err
		}
	}
	if len(text) == 0 {
		return errors.New("no private key: set RELEASE_SIGNING_KEY, or give -key FILE")
	}
	key, err := releasesig.ParsePrivateKey(text)
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(sumsFile) //nolint:gosec // G304: the file the person named
	if err != nil {
		return err
	}
	if err := releasesig.CheckSums(sums, version); err != nil {
		return err
	}
	sig := releasesig.Sign(key, sums)
	if !own {
		if err := releasesig.Verify(sums, sig); err != nil {
			return fmt.Errorf("the private key is not the one of this build's public keys: %w", err)
		}
	}
	// A signature is public: G306 is no concern.
	// store-bounds: not a store: one small file where the person named it
	if err := os.WriteFile(sumsFile+".sig", sig, 0o644); err != nil { //nolint:gosec // G306: public file
		return err
	}
	_, err = fmt.Fprintln(out, "wrote "+sumsFile+".sig")
	return err
}

func verify(sumsFile string, out io.Writer) error {
	sums, err := os.ReadFile(sumsFile) //nolint:gosec // G304: the file the person named
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sumsFile + ".sig") //nolint:gosec // G304: beside it
	if err != nil {
		return err
	}
	if err := releasesig.Verify(sums, sig); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "the signature is good")
	return err
}
