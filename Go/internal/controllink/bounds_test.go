package controllink

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A record over its limit is refused with words, and the one kept before stays: a limit
// never deletes a key, unpairs a machine or moves a pin (#7351). No test prints a key.

func TestAMembershipOverItsLimitIsRefusedAndTheOneBeforeStays(t *testing.T) {
	root := t.TempDir()
	kept := &Membership{Host: "h-1", ControlKey: bytes.Repeat([]byte{1}, 32), Name: "home", URL: "wss://a"}
	if err := kept.Save(root); err != nil {
		t.Fatal(err)
	}
	big := *kept
	for len(big.Endpoints)*64 < MembershipMostBytes {
		big.Endpoints = append(big.Endpoints, wire.ControlEndpoint{URL: "wss://" + strings.Repeat("a", 60)})
	}
	var tooLarge *TooLarge
	if err := big.Save(root); !errors.As(err, &tooLarge) || !strings.Contains(err.Error(), "not kept") {
		t.Fatalf("Save of %d endpoints: %v", len(big.Endpoints), err)
	}
	back, err := LoadMembership(root)
	if err != nil || back.Host != kept.Host || len(back.Endpoints) != 0 || !bytes.Equal(back.ControlKey, kept.ControlKey) {
		t.Fatalf("the membership before is not the one kept (err %v)", err)
	}
}

func TestAMembershipFileTooLargeToReadIsLeftAsItIs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control-host.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), 4*MembershipMostBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	var tooLarge *TooLarge
	if _, err := LoadMembership(root); !errors.As(err, &tooLarge) || !strings.Contains(err.Error(), "stays as it is") {
		t.Fatalf("LoadMembership: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 4*MembershipMostBytes+1 {
		t.Fatalf("the file changed: %v", err)
	}
}

func TestALockInviteOverItsLimitIsRefusedAndTheOneBeforeStays(t *testing.T) {
	root := t.TempDir()
	kept := LockInvite{Key: bytes.Repeat([]byte{2}, 32), Person: "p-1", Count: 1, Head: bytes.Repeat([]byte{3}, 32)}
	if err := SaveLockInvite(root, kept); err != nil {
		t.Fatal(err)
	}
	big := kept
	big.Person = strings.Repeat("p", LockInviteMostBytes)
	if err := SaveLockInvite(root, big); err == nil {
		t.Fatal("a lock invite over its limit was kept")
	}
	back := LoadLockInvite(root)
	if back == nil || back.Person != kept.Person || !bytes.Equal(back.Key, kept.Key) {
		t.Fatal("the invite before is not the one kept")
	}
}

func TestAPinOverItsLimitIsRefusedAndThePinBeforeStays(t *testing.T) {
	root := t.TempDir()
	key := bytes.Repeat([]byte{4}, 65)
	if _, err := PinPersonKey(root, key, PinFromJoinCode, time.Now()); err != nil {
		t.Fatal(err)
	}
	big := &PinnedPersonKey{Key: key, KeyID: strings.Repeat("k", PinnedPersonKeyMostBytes), From: PinFromWindow, At: time.Now()}
	if err := big.save(root); err == nil {
		t.Fatal("a pin over its limit was kept")
	}
	back, err := LoadPinnedPersonKey(root)
	if err != nil || back == nil || back.From != PinFromJoinCode || !bytes.Equal(back.Key, key) {
		t.Fatalf("the pin before is not the one kept (err %v)", err)
	}
}

func TestAKeyOfTheWrongSizeIsNotKept(t *testing.T) {
	root := t.TempDir()
	key := NewPrivateKey()
	for _, s := range []interface {
		Load() ([]byte, bool, error)
		Save([]byte) error
	}{HostKeyFile{Root: root}, PersonKeyFile{Root: root}} {
		if err := s.Save(key); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(make([]byte, 33)); err == nil {
			t.Fatal("a key of 33 bytes was kept")
		}
		if back, ok, err := s.Load(); err != nil || !ok || !bytes.Equal(back, key) {
			t.Fatalf("the key before is not the one kept (err %v)", err)
		}
	}
}
