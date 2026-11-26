package controlsetup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Receive is the machine's side of control/move (#6612): its copy is started again, empty,
// with the control key it is given, to receive the store of the copy that moves here. The
// copy's store before (the one its install made) is kept beside it, the newest only, named by
// the move, so a move that fails leaves nothing lost; Sweep deletes it when the move is done
// (#7372). url is where clients reach this machine, as the control plane found it; Receive
// says it again, and the pin of the copy's own certificate when it has one (none with a
// domain, whose certificate is publicly trusted).
func Receive(home string, key []byte, move, url string, d Deps) (string, string, error) {
	d.defaults()
	l := LayoutOf(home)
	if !Installed(home) {
		return "", "", errors.New("this machine has no control role: run agentsd install --control here first")
	}
	if len(key) != 32 {
		return "", "", errors.New("that is not a control key")
	}
	if !moveID.MatchString(move) {
		return "", "", fmt.Errorf("%q is not a move", move)
	}
	if !strings.HasPrefix(url, "https://") || strings.ContainsAny(url, " \n\r\"%") {
		return "", "", fmt.Errorf("%q is not an address the copy can answer at", url)
	}
	if out, err := d.Run("systemctl", "--user", "stop", UnitName); err != nil {
		return "", "", fmt.Errorf("systemctl stop: %v: %s", err, strings.TrimSpace(out))
	}
	if err := setStoreAside(l, move, time.Now()); err != nil {
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

// storeAsidePrefix names a store set aside by a move: storeAsidePrefix<unix seconds>-<move>.
const storeAsidePrefix = "store.before-move-"

// storeAsideAge is the longest a store set aside is kept when its move is not known to be done.
const storeAsideAge = 90 * 24 * time.Hour

// moveID is a move's ID as the control plane makes it: the milliseconds it started, and hex.
var moveID = regexp.MustCompile(`^[0-9]{1,20}-[0-9a-f]{1,32}$`)

// setStoreAside renames the copy's store, and removes any set aside by a move before.
// store-bounds: kind=blob; count=1; age=90d; applied=at the next move, and the hourly sweep (Sweep) when its move is done or at 90 days
func setStoreAside(l Layout, move string, now time.Time) error {
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
	return os.Rename(current, filepath.Join(l.Control, storeAsidePrefix+strconv.FormatInt(now.Unix(), 10)+"-"+move))
}

// Sweep deletes the store a move set aside (#7372) once the copy's store has that move's record
// as done or forwarding (the copy took over: its new store holds the control plane), or when
// it was set aside storeAsideAge ago. A move that failed keeps it until its retry, which sets
// the store aside again, or until that age. Run by agentsd's hourly sweep.
func Sweep(home string, now time.Time) {
	l := LayoutOf(home)
	dirs, _ := filepath.Glob(filepath.Join(l.Control, storeAsidePrefix+"*")) // the pattern is a good one
	for _, dir := range dirs {
		at, move := asideOf(filepath.Base(dir))
		if at.IsZero() {
			info, err := os.Lstat(dir)
			if err != nil {
				continue
			}
			at = info.ModTime()
		}
		why := ""
		switch {
		case move != "" && moveDone(l, move):
			why = "its move is done"
		case now.Sub(at) >= storeAsideAge:
			why = "it was set aside 90 days ago"
		default:
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("retention: %s: %v", filepath.Base(dir), err)
			continue
		}
		log.Printf("retention: the control store set aside by a move is deleted: %s", why)
	}
}

// asideOf is when a store was set aside and by which move, from its name; a zero time when the
// name does not say (none from before #7372 names its move).
func asideOf(name string) (time.Time, string) {
	rest := strings.TrimPrefix(name, storeAsidePrefix)
	unix, move, _ := strings.Cut(rest, "-")
	seconds, err := strconv.ParseInt(unix, 10, 64)
	if err != nil {
		return time.Time{}, ""
	}
	if !moveID.MatchString(move) {
		move = ""
	}
	return time.Unix(seconds, 0), move
}

// moveDone says whether the copy's store has the move's record, as the moving copy left it
// there before the take, ended done or forwarding.
func moveDone(l Layout, move string) bool {
	data, err := os.ReadFile(filepath.Join(l.Control, "store", "v1", "moves", move+".json")) //nolint:gosec // G304: the copy's own store, a checked move ID
	if err != nil {
		return false
	}
	var record struct {
		Phase wire.ControlMovePhase `json:"phase"`
	}
	if json.Unmarshal(data, &record) != nil {
		return false
	}
	return record.Phase == wire.ControlMovePhaseDone || record.Phase == wire.ControlMovePhaseForwarding
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
