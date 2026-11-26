package controllink

import (
	"fmt"
	"io"
	"os"
)

// The largest records of this machine's identity files (#7351, epic #6669). A write over
// its limit is refused with words and leaves the file as it was; a read stops at four
// times the limit, so a file no writer here could make is not read whole. Neither ever
// deletes a file: a refused record keeps the machine's pairing, key and pin as they are.
const (
	// MembershipMostBytes is control-host.json's largest record: the control plane's key,
	// its name, and the endpoints it gave, which its settings bound at 64 KiB.
	MembershipMostBytes = 256 << 10
	// LockInviteMostBytes is lock-invite.json's: a 32-byte key, a person, a count and a head.
	LockInviteMostBytes = 4 << 10
	// PinnedPersonKeyMostBytes is person-key.pub.json's: one public key, its id and where from.
	PinnedPersonKeyMostBytes = 4 << 10
)

// TooLarge is a record refused at its write, or a file refused at its read, for its size.
type TooLarge struct {
	What        string
	Size, Most  int
	AtTheReader bool
}

func (e *TooLarge) Error() string {
	if e.AtTheReader {
		return fmt.Sprintf("%s is larger than %d bytes, so it is not read; the file stays as it is", e.What, e.Most)
	}
	return fmt.Sprintf("%s is %d bytes, more than the %d this machine keeps, so it is not kept; the one before stays", e.What, e.Size, e.Most)
}

// fits refuses a record of more than most bytes, before it is written.
func fits(what string, b []byte, most int) error {
	if len(b) > most {
		return &TooLarge{What: what, Size: len(b), Most: most}
	}
	return nil
}

// readAtMost is os.ReadFile, refused when the file is more than four times most.
func readAtMost(path, what string, most int) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: one of this machine's own identity files in its root
	if err != nil {
		return nil, err
	}
	defer f.Close()
	limit := 4 * most
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, &TooLarge{What: what, Most: limit, AtTheReader: true}
	}
	return b, nil
}
