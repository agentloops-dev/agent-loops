package controlsetup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Receive is the machine's side of control/move (#6612): its copy is started again, empty,
// with the control key it is given, to receive the store of the copy that moves here. The
// copy's store before (the one its install made) is kept beside it, the newest only, so a move
// that fails leaves nothing lost. url is where clients reach this machine, as the control
// plane found it; Receive says it again, and the pin of the copy's own certificate when it has
// one (none with a domain, whose certificate is publicly trusted).
func Receive(home string, key []byte, url string, d Deps) (string, string, error) {
	d.defaults()
	l := LayoutOf(home)
	if !Installed(home) {
		return "", "", errors.New("this machine has no control role: run agentsd install --control here first")
	}
	if len(key) != 32 {
		return "", "", errors.New("that is not a control key")
	}
	if !strings.HasPrefix(url, "https://") || strings.ContainsAny(url, " \n\r\"%") {
		return "", "", fmt.Errorf("%q is not an address the copy can answer at", url)
	}
	if out, err := d.Run("systemctl", "--user", "stop", UnitName); err != nil {
		return "", "", fmt.Errorf("systemctl stop: %v: %s", err, strings.TrimSpace(out))
	}
	if err := setStoreAside(l); err != nil {
		return "", "", err
	}
	if err := writeKey(filepath.Join(l.Control, "control-key"), key); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(l.ReceiveUnit()), 0o700); err != nil {
		return "", "", err
	}
	// store-bounds: not a store: the unit's drop-in, one small file rewritten whole by each move
	if err := os.WriteFile(l.ReceiveUnit(), []byte(receiveDropIn(url)), 0o600); err != nil {
		return "", "", err
	}
	for _, step := range [][]string{{"daemon-reload"}, {"start", UnitName}} {
		if out, err := d.Run("systemctl", append([]string{"--user"}, step...)...); err != nil {
			return "", "", fmt.Errorf("systemctl %s: %v: %s", step[0], err, strings.TrimSpace(out))
		}
	}
	unit, err := os.ReadFile(l.Unit)
	if err != nil {
		return "", "", err
	}
	if strings.Contains(string(unit), "--acme-domain") {
		return url, "", nil
	}
	pin, err := waitForPin(filepath.Join(l.Control, "tls", "cert.pem"), d)
	return url, pin, err
}

// ReceiveUnit is the unit's drop-in that a move writes: the copy receives, at the address
// that the control plane reached it at. It stays after the copy took over: a copy started
// again to receive with its records serves.
func (l Layout) ReceiveUnit() string {
	return filepath.Join(filepath.Dir(l.Unit), UnitName+".d", "20-receive.conf")
}

func receiveDropIn(url string) string {
	return "[Service]\nEnvironment=AGENTS_CONTROL_RECEIVE=1\nEnvironment=AGENTS_CONTROL_URL=" + url + "\n"
}

// storeAsidePrefix names a store set aside by a move.
const storeAsidePrefix = "store.before-move-"

// setStoreAside renames the copy's store, and removes any set aside by a move before.
func setStoreAside(l Layout) error {
	current := filepath.Join(l.Control, "store")
	if _, err := os.Stat(current); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	old, err := filepath.Glob(filepath.Join(l.Control, storeAsidePrefix+"*"))
	if err != nil {
		return err
	}
	for _, dir := range old {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return os.Rename(current, filepath.Join(l.Control, storeAsidePrefix+strconv.FormatInt(time.Now().Unix(), 10)))
}

// writeKey writes the key whole, 0600, then renames it into place, as the key file's keeper
// does.
func writeKey(path string, key []byte) error {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".control-key.%x.tmp", b))
	// store-bounds: not a store: the control key, one 32-byte file
	if err := os.WriteFile(tmp, key, 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	return os.Rename(tmp, path)
}

// waitForPin is the pin of the copy's own certificate, once it is there: base64url of the
// SHA-256 of its SubjectPublicKeyInfo, as every code holds it.
func waitForPin(certFile string, d Deps) (string, error) {
	deadline := time.Now().Add(d.Wait)
	for {
		data, err := os.ReadFile(certFile) //nolint:gosec // G304: the copy's own certificate
		if err == nil {
			block, _ := pem.Decode(data)
			if block == nil {
				return "", errors.New("the copy's certificate does not read")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return "", err
			}
			sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
			return controllink.B64URL(sum[:]), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the copy made no certificate in %s", d.Wait)
		}
		time.Sleep(d.Poll)
	}
}
