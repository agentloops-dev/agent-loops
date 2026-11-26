package controlsetup

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
)

// installedHome is a home with the control role installed, a store and a certificate.
func installedHome(t *testing.T, unit string) (string, Layout, string) {
	t.Helper()
	home := t.TempDir()
	l := LayoutOf(home)
	for _, dir := range []string{filepath.Dir(l.Unit), filepath.Join(l.Control, "store", "v1"), filepath.Join(l.Control, "tls")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(l.Unit, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Control, "store", "v1", "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "box"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "box"}}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Control, "tls", "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return home, l, controllink.B64URL(sum[:])
}

// A move to this machine (#6612): the copy stops, its store is set aside, the key is kept for
// it, and it starts again to receive at the address given, with its certificate's pin.
func TestReceiveStartsTheCopyEmptyWithTheKey(t *testing.T) {
	t.Parallel()
	home, l, wantPin := installedHome(t, Unit(Options{}))
	f := &fake{}
	key := bytes.Repeat([]byte{7}, 32)
	url, pin, err := Receive(home, key, "https://203.0.113.7:8791", Deps{Run: f.run, Poll: time.Millisecond, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://203.0.113.7:8791" || pin != wantPin {
		t.Fatal(url, pin)
	}
	if got := strings.Join(f.calls, "; "); got != "systemctl --user stop agents-control.service; systemctl --user daemon-reload; systemctl --user start agents-control.service" {
		t.Fatal(got)
	}
	if kept, err := os.ReadFile(filepath.Join(l.Control, "control-key")); err != nil || !bytes.Equal(kept, key) {
		t.Fatal(kept, err)
	}
	if _, err := os.Stat(filepath.Join(l.Control, "store")); !os.IsNotExist(err) {
		t.Fatal("the store was not set aside", err)
	}
	aside, _ := filepath.Glob(filepath.Join(l.Control, storeAsidePrefix+"*", "v1", "settings.json"))
	if len(aside) != 1 {
		t.Fatal(aside)
	}
	dropIn, err := os.ReadFile(l.ReceiveUnit())
	if err != nil || !strings.Contains(string(dropIn), "AGENTS_CONTROL_RECEIVE=1") || !strings.Contains(string(dropIn), "AGENTS_CONTROL_URL=https://203.0.113.7:8791") {
		t.Fatal(string(dropIn), err)
	}
	// With a domain the certificate is publicly trusted: no pin.
	home, _, _ = installedHome(t, Unit(Options{Domain: "ams3-1.example.dev"}))
	if _, pin, err := Receive(home, key, "https://ams3-1.example.dev", Deps{Run: (&fake{}).run}); err != nil || pin != "" {
		t.Fatal(pin, err)
	}
}

func TestReceiveRefusesWithoutTheControlRole(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{7}, 32)
	if _, _, err := Receive(t.TempDir(), key, "https://203.0.113.7", Deps{Run: (&fake{}).run}); err == nil || !strings.Contains(err.Error(), "no control role") {
		t.Fatal(err)
	}
	home, _, _ := installedHome(t, Unit(Options{}))
	for _, url := range []string{"http://203.0.113.7", "https://a b", "https://x\nExecStart=/bin/sh"} {
		if _, _, err := Receive(home, key, url, Deps{Run: (&fake{}).run}); err == nil {
			t.Errorf("%q was taken", url)
		}
	}
}
