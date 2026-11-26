package machine

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// KeptID is the id in the file at path, made there the first time (#7399): 32 hex digits, as
// /etc/machine-id has. A container has no machine-id of its own and a new host name each
// time it is made, so a copy of the control plane in one keeps its id in its data volume,
// and each deploy is the same machine.
func KeptID(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	// Written whole, then renamed: a copy that stops half way leaves no empty id.
	// store-bounds: kind=key-value; count=1 (one id of 33 bytes); age=until-deleted; applied=made once, then only read
	tmp, err := os.CreateTemp(filepath.Dir(path), ".machine-id-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return id, nil
}
