package records

import (
	"context"
	"fmt"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// The bounds of a control plane's configuration (#6771 to #6781, epic #6669). A person made
// it, so it has no age and nothing is evicted: a write over the largest record is refused. A
// record that is over it already stays as it is, and can be changed to one no larger.

// TooLargeError is a write refused because its record is too large, in the words a person
// reads.
type TooLargeError struct{ Words string }

func (e *TooLargeError) Error() string { return e.Words }

// Fits refuses data, the record to be kept at key, when it is over most bytes and larger
// than the one there. What says what the record is, such as "The saved views".
func Fits(ctx context.Context, s store.Store, key, what string, data []byte, most int) error {
	if len(data) <= most {
		return nil
	}
	object, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	if object != nil && len(data) <= len(object.Data) {
		return nil
	}
	return TooLarge(what, len(data), most)
}

// TooMany is the refusal of one more of what is at its count, most.
func TooMany(what string, most int) error {
	return &TooLargeError{fmt.Sprintf("%s are at their most, %d. Remove one first.", what, most)}
}

// TooLarge is the refusal of a record of size bytes, over most.
func TooLarge(what string, size, most int) error {
	return &TooLargeError{fmt.Sprintf("%s would be too large: %d KiB, and the most is %d KiB. Remove something first.", what, (size+1023)>>10, most>>10)}
}

// SettingsMostBytes is the largest SettingsKey record (#6771). What the control plane writes
// there itself (its address, its key, the machine at home) is never refused: it would not
// start, or its key would not rotate.
const SettingsMostBytes = 64 << 10

// settingsFit refuses a change that grows what a person sets, the folders to look in and the
// places the control plane answers, past SettingsMostBytes.
func settingsFit(before, after Settings) error {
	size, err := settingsSize(after)
	if err != nil || size <= SettingsMostBytes {
		return err
	}
	was, err := personSet(before)
	if err != nil {
		return err
	}
	is, err := personSet(after)
	if err != nil || is <= was {
		return err
	}
	return TooLarge("The control plane's settings", size, SettingsMostBytes)
}

func settingsSize(s Settings) (int, error) {
	data, err := Encode(s)
	return len(data), err
}

func personSet(s Settings) (int, error) {
	set := Settings{FolderDetection: s.FolderDetection}
	// One place is the control plane's own.
	if len(s.Endpoints) > 1 {
		set.Endpoints = s.Endpoints
	}
	return settingsSize(set)
}
