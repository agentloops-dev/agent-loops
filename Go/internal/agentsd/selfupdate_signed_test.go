package agentsd

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/linuxrelease"
	"github.com/agentloops-dev/agent-loops/Go/internal/releasesig"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A Linux machine checks a release's signature before it opens or runs anything of it.

// releaseSig is sums signed with a test key, "current" or "spare".
func releaseSig(t *testing.T, key string, sums []byte) []byte {
	t.Helper()
	b, err := os.ReadFile("../releasesig/testdata/test-" + key + ".pem") //nolint:gosec // G304: a test's own data
	if err != nil {
		t.Fatal(err)
	}
	k, err := releasesig.ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	return releasesig.Sign(k, sums)
}

// otherKey is a key that no build has.
func otherKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	k, _, err := releasesig.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	line, err := releasesig.PublicText(k.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return k, "-----BEGIN PUBLIC KEY-----\n" + line + "\n-----END PUBLIC KEY-----\n"
}

// signedRig is release 0.12.0 of a host that writes the file ran when it runs, and what a
// source serves of it, by file name.
type signedRig struct {
	*linuxRig
	ran     string
	tarball string
	files   map[string][]byte
	mu      sync.Mutex
	asked   []string
}

func (r *signedRig) sums() []byte { return r.files["SHA256SUMS"] }

// took is whether a path was asked for.
func (r *signedRig) took(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.asked {
		if a == path {
			return true
		}
	}
	return false
}

func newSignedRig(t *testing.T) *signedRig {
	t.Helper()
	r := &signedRig{ran: filepath.Join(t.TempDir(), "ran"), tarball: linuxrelease.Tarball("x86_64", "0.12.0")}
	host := []byte("#!/bin/sh\necho ran > '" + r.ran + "'\necho '0.12.0 (go)'\n")
	body := linuxReleaseTarball(t, map[string][]byte{"agentsd-linux-x86_64": host})
	r.linuxRig, _, _ = newLinuxRig(t, body, body)
	r.files = map[string][]byte{"SHA256SUMS": []byte(linuxSum(body) + "  " + r.tarball + "\n"), r.tarball: body}
	r.files["SHA256SUMS.sig"] = releaseSig(t, "current", r.sums())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.asked = append(r.asked, req.URL.Path)
		r.mu.Unlock()
		name, ok := strings.CutPrefix(req.URL.Path, "/v1/releases/0.12.0/")
		if b, there := r.files[name]; ok && there {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, req)
	}))
	t.Cleanup(srv.Close)
	// The public releases have nothing, so a release from the control plane is all there is.
	r.u.public = srv.URL + "/public"
	r.u.control = func() []releaseSource { return []releaseSource{{base: srv.URL}} }
	return r
}

// refuses takes the release, and it is not installed, and no program of it ran. why is in
// the message.
func (r *signedRig) refuses(t *testing.T, why string) {
	t.Helper()
	r.u.Named("0.12.0")
	s := r.until(t, wire.SelfUpdateStateFailed)
	if s.Message == nil || !strings.Contains(*s.Message, why) {
		t.Fatalf("%+v", *s.Message)
	}
	if _, err := os.Stat(r.ran); err == nil {
		t.Fatal("the new host ran")
	}
	if m, _ := filepath.Glob(filepath.Join(r.bin, "agentsd-*")); len(m) != 1 || r.target(t) != r.oldName || len(r.leftovers()) != 0 {
		t.Fatalf("%v %s %v", m, r.target(t), r.leftovers())
	}
}

// installs takes the release, and its host ran (so refuses' file says something).
func (r *signedRig) installs(t *testing.T) {
	t.Helper()
	r.u.Named("0.12.0")
	r.until(t, wire.SelfUpdateStateRestarting)
	if _, err := os.Stat(r.ran); err != nil || r.target(t) == r.oldName {
		t.Fatalf("%v %s", err, r.target(t))
	}
}

func TestSignedReleaseIsInstalled(t *testing.T) {
	newSignedRig(t).installs(t)
}

func TestReleaseSignedWithTheSpareKeyIsInstalled(t *testing.T) {
	r := newSignedRig(t)
	r.files["SHA256SUMS.sig"] = releaseSig(t, "spare", r.sums())
	r.installs(t)
}

func TestReleaseWithNoGoodSignatureIsRefused(t *testing.T) {
	other, _ := otherKey(t)
	for name, change := range map[string]func(r *signedRig){
		"not a signature": func(r *signedRig) { r.files["SHA256SUMS.sig"] = []byte("not one") },
		"empty":           func(r *signedRig) { r.files["SHA256SUMS.sig"] = nil },
		"a bit changed":   func(r *signedRig) { r.files["SHA256SUMS.sig"][10] ^= 1 },
		"another key":     func(r *signedRig) { r.files["SHA256SUMS.sig"] = releasesig.Sign(other, r.sums()) },
		"other bytes":     func(r *signedRig) { r.files["SHA256SUMS.sig"] = releaseSig(t, "current", r.sums()[1:]) },
		"sums changed after": func(r *signedRig) {
			body := append([]byte{}, r.files[r.tarball]...)
			body = append(body, 0)
			r.files[r.tarball] = body
			r.files["SHA256SUMS"] = []byte(linuxSum(body) + "  " + r.tarball + "\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSignedRig(t)
			change(r)
			r.refuses(t, "does not match its signature")
			if r.took("/v1/releases/0.12.0/" + r.tarball) {
				t.Fatal("the tarball was downloaded before the signature was checked")
			}
			if r.took("/public/v0.12.0/SHA256SUMS") {
				t.Fatal("another source was tried after a refusal")
			}
		})
	}
}

// SHA256SUMS of another release, with its good signature, lists no file of this one.
func TestSignedSumsOfAnotherReleaseAreRefused(t *testing.T) {
	r := newSignedRig(t)
	sums := []byte(linuxSum(r.files[r.tarball]) + "  " + linuxrelease.Tarball("x86_64", "0.11.0") + "\n")
	r.files["SHA256SUMS"], r.files["SHA256SUMS.sig"] = sums, releaseSig(t, "current", sums)
	r.files[linuxrelease.Tarball("x86_64", "0.11.0")] = r.files[r.tarball]
	r.refuses(t, "")
	if r.took("/v1/releases/0.12.0/" + r.tarball) {
		t.Fatal("the tarball was downloaded")
	}
}

func TestSignedSumsWithATarballChangedAfterAreRefused(t *testing.T) {
	r := newSignedRig(t)
	r.files[r.tarball] = append(append([]byte{}, r.files[r.tarball]...), 0)
	r.refuses(t, "does not match its checksum")
}

// A control plane that is older than signed releases has SHA256SUMS and no signature. The
// public releases are asked next; with none there either, nothing is installed.
func TestControlPlaneWithNoSignature(t *testing.T) {
	r := newSignedRig(t)
	delete(r.files, "SHA256SUMS.sig")
	r.refuses(t, "could not download release 0.12.0's signature")
	if !r.took("/public/v0.12.0/SHA256SUMS") {
		t.Fatal("the public releases were not asked")
	}

	r = newSignedRig(t)
	sig := r.files["SHA256SUMS.sig"]
	delete(r.files, "SHA256SUMS.sig")
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/v0.12.0/")
		if name == "SHA256SUMS.sig" {
			_, _ = w.Write(sig)
		} else {
			_, _ = w.Write(r.files[name])
		}
	}))
	t.Cleanup(public.Close)
	r.u.public = public.URL
	r.installs(t)
}

// The public releases and a mirror (AGENTS_RELEASE_DOWNLOADS) need the signature too.
func TestEverySourceNeedsTheSignature(t *testing.T) {
	for _, from := range []string{"public", "mirror"} {
		t.Run(from, func(t *testing.T) {
			r := newSignedRig(t)
			serve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if b, ok := r.files[strings.TrimPrefix(req.URL.Path, "/v0.12.0/")]; ok {
					_, _ = w.Write(b)
					return
				}
				http.NotFound(w, req)
			}))
			t.Cleanup(serve.Close)
			r.u.control = nil
			if from == "public" {
				r.u.public = serve.URL
			} else {
				t.Setenv("AGENTS_RELEASE_DOWNLOADS", serve.URL)
			}
			delete(r.files, "SHA256SUMS.sig")
			r.refuses(t, "signature")
			r.files["SHA256SUMS.sig"] = releaseSig(t, "current", r.sums())
			r.installs(t)
		})
	}
}

// An own build is signed with a key that the account put in AGENTS_RELEASE_KEY.
func TestOwnBuildNeedsTheAccountsOwnKey(t *testing.T) {
	r := newSignedRig(t)
	other, public := otherKey(t)
	r.files["SHA256SUMS.sig"] = releasesig.Sign(other, r.sums())
	file := filepath.Join(t.TempDir(), "key.pem")
	must(t, os.WriteFile(file, []byte(public), 0o600))
	must(t, os.Chmod(file, 0o622)) //nolint:gosec // G302: the mode under test
	t.Setenv("AGENTS_RELEASE_KEY", file)
	r.refuses(t, "signature")
	must(t, os.Chmod(file, 0o600))
	r.installs(t)
}
