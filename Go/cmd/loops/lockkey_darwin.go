package main

import (
	"errors"

	"github.com/agentloops-dev/agent-loops/Go/internal/mac"
)

// keychainLockKeys keeps each key in a Keychain item that only loops reads: mac.KeychainAdd
// trusts the program that made the item, by its code signature (#6563).
type keychainLockKeys struct{ service string }

func platformLockKeys() lockKeyStore { return keychainLockKeys{service: "dev.agentloops.loops.lock"} }

func (k keychainLockKeys) load(client string) ([]byte, bool, error) {
	key, ok, err := mac.KeychainGet(k.service, client)
	if errors.Is(err, mac.ErrKeychainDenied) {
		return nil, false, errors.New("the Keychain did not let this build of loops read it; another build made it")
	}
	return key, ok, err
}

func (k keychainLockKeys) save(client string, key []byte) error {
	return mac.KeychainAdd(k.service, client, "loops machine lock key", key)
}

func (k keychainLockKeys) remove(client string) error { return mac.KeychainDelete(k.service, client) }
