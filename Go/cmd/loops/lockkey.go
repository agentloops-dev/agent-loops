package main

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
)

// A terminal's key in its person's machine lock (#6563): one for each pairing, by the client
// id the control plane knows it by, so a new pairing is a new device. On a Mac it is in the
// Keychain (lockkey_darwin.go), where only loops reads it; elsewhere, and with
// LOOPS_KEY_FILES=1 (a pairing copied to CI), a file of mode 0600 under
// ~/.config/loops/lock-keys. The key is P-256, kept as its raw 32-byte scalar.

// lockKeyStore keeps the keys; a test replaces lockKeys with files under its own folder.
type lockKeyStore interface {
	load(client string) ([]byte, bool, error)
	save(client string, key []byte) error
	remove(client string) error
}

var lockKeys = pickLockKeys()

func pickLockKeys() lockKeyStore {
	if os.Getenv("LOOPS_KEY_FILES") == "1" {
		return fileLockKeys{}
	}
	return platformLockKeys()
}

// lockKey is the pairing's key, made and kept the first time.
func lockKey(m *controllink.ClientMembership) (*ecdsa.PrivateKey, error) {
	raw, ok, err := lockKeys.load(m.Client)
	if err != nil {
		return nil, &exit{code: exitError, msg: fmt.Sprintf("Could not read this terminal's machine lock key (%v).\n"+
			"Pair again to make a new one: loops auth login '<code>'", err)}
	}
	if ok {
		key, err := machinelock.MachineSigner(raw)
		if err != nil {
			return nil, &exit{code: exitError, msg: "This terminal's machine lock key is not a key.\nPair again to make a new one: loops auth login '<code>'"}
		}
		return key, nil
	}
	key, err := machinelock.NewKey()
	if err != nil {
		return nil, err
	}
	raw, err = key.Bytes()
	if err != nil {
		return nil, err
	}
	if err := lockKeys.save(m.Client, raw); err != nil {
		return nil, &exit{code: exitError, msg: fmt.Sprintf("Could not keep this terminal's machine lock key (%v).", err)}
	}
	return key, nil
}

// fileLockKeys keeps each key in a file of its own, mode 0600, in lock-keys, a folder of
// mode 0700 beside hosts.json.
type fileLockKeys struct{}

func (fileLockKeys) folder() string { return filepath.Join(configDir(), "lock-keys") }

func (f fileLockKeys) path(client string) (string, error) {
	if client == "" || filepath.Base(client) != client || client == "." || client == ".." {
		return "", fmt.Errorf("%q is not a client id", client)
	}
	return filepath.Join(f.folder(), client+".key"), nil
}

func (f fileLockKeys) load(client string) ([]byte, bool, error) {
	p, err := f.path(client)
	if err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return b, err == nil, err
}

func (f fileLockKeys) save(client string, key []byte) error {
	p, err := f.path(client)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.folder(), 0o700); err != nil {
		return err
	}
	return store.WriteAtomically(p, key, 0o600)
}

func (f fileLockKeys) remove(client string) error {
	p, err := f.path(client)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
