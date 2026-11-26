package machinelock

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Signed records (#7167): a value that a client of the lock signed for one or more of the
// person's machines, such as the approval of a set-up script. A machine takes the content only
// from a record that checks against the lock it holds, at a number not lower than the last one
// it took (Counters). Swift's LockRecords and the web page's wire/lockRecords.ts make the same
// checks, against the same vectors (Fixtures/lock/records.json).
//
// The signed bytes name the kind and the scope as text, so a kind or a scope can be added to
// recordKinds with no change to them.

const recordDomain = "agents-lock-record/1"

const (
	// RecordVersion is LockRecord.v.
	RecordVersion = 1
	// MostRecordContent bounds one record's content.
	MostRecordContent = 256 << 10
	// MostRecordSeq is the highest seq, the largest whole number every client reads exactly.
	MostRecordSeq = 1<<53 - 1
	// MostFirstSeq is the highest number of the first record that a machine takes of a kind and
	// subject, and MostSeqStep is how far above the last one the next may be. So there is always
	// a higher number to sign.
	MostFirstSeq = 1 << 40
	MostSeqStep  = 1 << 20
)

// ScopeMade names no machine: a record of it is for a machine as it is made. scopeMachine
// starts a scope that names one machine.
const (
	ScopeMade    = "made"
	scopeMachine = "m:"
)

// ScopeOf is the scope of one machine.
func ScopeOf(machine string) string { return scopeMachine + machine }

// The ways a record fails its check, beside ErrInvalid, ErrForged and ErrNotMember.
var (
	// ErrOtherLock: the record is of another person, or of a lock that was replaced.
	ErrOtherLock = errors.New("the record is not of your lock")
	// ErrOtherPlace: the record is for another machine.
	ErrOtherPlace = errors.New("the record is for another machine")
	// ErrOlder: a newer record of its kind and subject was taken.
	ErrOlder = errors.New("the record is older than the one last taken")
	// ErrTwo: another record with the same number was taken.
	ErrTwo = errors.New("another record has the same number")
	// ErrFar: the number is too far above the one last taken.
	ErrFar = errors.New("the record's number is too far above the one last taken")
	// ErrFull: there is no room for one more counter.
	ErrFull = errors.New("there are too many records")
)

// KindSetupScript is the approval of a set-up script: its subject is the script (root or
// setup), and its content a SetupScriptApproval.
const KindSetupScript = "setup.script"

// recordKinds are the kinds a build knows, with the scopes each may have.
var recordKinds = map[string][]string{
	KindSetupScript: {ScopeMade, scopeMachine},
}

// recordName is a subject and a machine's id.
var recordName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// CheckRecordName says whether a kind, subject and scope may name a record.
func CheckRecordName(kind, subject, scope string) error {
	scopes, ok := recordKinds[kind]
	if !ok {
		return fmt.Errorf("%w: the kind %q is not one this build knows", ErrInvalid, kind)
	}
	if !recordName.MatchString(subject) {
		return fmt.Errorf("%w: %q is not a subject", ErrInvalid, subject)
	}
	class := scope
	if machine, ok := strings.CutPrefix(scope, scopeMachine); ok && recordName.MatchString(machine) {
		class = scopeMachine
	} else if ok {
		return fmt.Errorf("%w: %q names no machine", ErrInvalid, scope)
	}
	if !slices.Contains(scopes, class) {
		return fmt.Errorf("%w: a %s record cannot have the scope %q", ErrInvalid, kind, scope)
	}
	return nil
}

// ContentHash is the hash of a record's content that its signature covers.
func ContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// RecordData is what a record's signature covers: the domain and each part, ended by a newline.
func RecordData(r wire.LockRecord) []byte {
	return []byte(strings.Join([]string{recordDomain, r.Person, hex.EncodeToString(r.Root), r.Kind, r.Subject, r.Scope,
		strconv.FormatInt(r.Seq, 10), ContentHash(r.Content), ""}, "\n"))
}

// SignRecord signs content as a record of the lock state, with a client's key.
func SignRecord(key *ecdsa.PrivateKey, state *State, kind, subject, scope string, seq int64, content []byte) (wire.LockRecord, error) {
	r := wire.LockRecord{V: RecordVersion, Person: state.Person, Root: state.Root[:], Kind: kind, Subject: subject, Scope: scope,
		Seq: seq, Content: append([]byte{}, content...), Key: PublicKey(key)}
	if err := checkRecordShape(r); err != nil {
		return r, err
	}
	sig, err := SignData(key, RecordData(r))
	r.Sig = sig
	return r, err
}

// checkRecordShape refuses a record whose parts are not ones that a signature may cover.
func checkRecordShape(r wire.LockRecord) error {
	if r.V != RecordVersion {
		return fmt.Errorf("%w: version %d, not %d", ErrInvalid, r.V, RecordVersion)
	}
	if err := CheckRecordName(r.Kind, r.Subject, r.Scope); err != nil {
		return err
	}
	if strings.ContainsFunc(r.Person, func(c rune) bool { return c < ' ' || c == 0x7f }) {
		return fmt.Errorf("%w: its person has a control character", ErrInvalid)
	}
	if r.Seq < 1 || r.Seq > MostRecordSeq {
		return fmt.Errorf("%w: its number is %d", ErrInvalid, r.Seq)
	}
	if len(r.Content) > MostRecordContent {
		return fmt.Errorf("%w: its content is over %d KiB", ErrInvalid, MostRecordContent>>10)
	}
	return nil
}

// Removed says that a record says its value is removed.
func Removed(r wire.LockRecord) bool { return len(r.Content) == 0 }

// CheckRecord checks a record against the lock: that a client it names now signed it, for
// this person and this lock. It gives back the signer. It does not say which machine may
// take the record (TakesRecord), nor whether a newer one was taken (Counters).
func CheckRecord(state *State, r wire.LockRecord) (Member, error) {
	if err := checkRecordShape(r); err != nil {
		return Member{}, err
	}
	if r.Person != state.Person || !bytes.Equal(r.Root, state.Root[:]) {
		return Member{}, ErrOtherLock
	}
	m, ok := state.Member(r.Key)
	if !ok || !m.Client() {
		return Member{}, fmt.Errorf("%w: only a device of yours signs a record", ErrNotMember)
	}
	if !VerifyData(r.Key, RecordData(r), r.Sig) {
		return Member{}, fmt.Errorf("%w: the record's signature does not check", ErrForged)
	}
	return m, nil
}

// TakesRecord refuses a record that is not for the machine host. A record of the scope made
// is for a machine that its caller says was just made.
func TakesRecord(r wire.LockRecord, host string, made bool) error {
	switch {
	case r.Scope == ScopeMade && made, host != "" && r.Scope == ScopeOf(host):
		return nil
	}
	return ErrOtherPlace
}

// MostCounters bounds the counters a machine keeps. One more is refused, and none is dropped.
const MostCounters = 2000

// Counter is the record last taken of one kind and subject: its number and its content's hash.
type Counter struct {
	Seq     int64  `json:"seq"`
	Content string `json:"content"`
}

// CheckSeq says whether a record's number may follow the number had last: 0 for none.
func CheckSeq(had, seq int64) error {
	switch {
	case had == 0 && seq > MostFirstSeq:
		return fmt.Errorf("%w: %d, and the first is at most %d", ErrFar, seq, int64(MostFirstSeq))
	case had != 0 && seq-had > MostSeqStep:
		return fmt.Errorf("%w: %d, and %d was taken", ErrFar, seq, had)
	case seq < had:
		return fmt.Errorf("%w: %d, and %d was taken", ErrOlder, seq, had)
	}
	return nil
}

func checkCounter(c map[string]Counter, key string, r wire.LockRecord) error {
	had, ok := c[key]
	if !ok && len(c) >= MostCounters {
		return ErrFull
	}
	if err := CheckSeq(had.Seq, r.Seq); err != nil {
		return err
	}
	if ok && r.Seq == had.Seq && ContentHash(r.Content) != had.Content {
		return fmt.Errorf("%w: %d", ErrTwo, r.Seq)
	}
	return nil
}

func takeCounter(c map[string]Counter, key string, r wire.LockRecord) (changed bool, err error) {
	if err := checkCounter(c, key, r); err != nil {
		return false, err
	}
	now := Counter{Seq: r.Seq, Content: ContentHash(r.Content)}
	changed = c[key] != now
	c[key] = now
	return changed, nil
}

// validCounters says that each counter is one that a take noted, under a key of so many parts.
func validCounters(c map[string]Counter, parts int) bool {
	for key, n := range c {
		hash, err := hex.DecodeString(n.Content)
		if n.Seq < 1 || n.Seq > MostRecordSeq || err != nil || len(hash) != sha256.Size {
			return false
		}
		name := strings.SplitN(key, "/", parts)
		root, err := hex.DecodeString(name[0])
		if len(name) != parts || err != nil || len(root) != sha256.Size || slices.Contains(name, "") {
			return false
		}
	}
	return len(c) <= MostCounters
}

// Counters are a machine's counters, one for each lock, kind and subject, whatever the scope
// of the record: the scopes that one machine takes share one.
type Counters map[string]Counter

func counterKey(r wire.LockRecord) string {
	return hex.EncodeToString(r.Root) + "/" + r.Kind + "/" + r.Subject
}

// Check says whether a record may be taken after those taken before: ErrOlder for a lower
// number, ErrTwo for another content with the same number, ErrFar for one too far above, ErrFull
// for no room.
func (c Counters) Check(r wire.LockRecord) error { return checkCounter(c, counterKey(r), r) }

// Take is Check, and notes the record as taken. changed says that the counters must be kept
// before the content is used.
func (c Counters) Take(r wire.LockRecord) (changed bool, err error) {
	return takeCounter(c, counterKey(r), r)
}

// Valid says that every counter reads as one that Take noted.
func (c Counters) Valid() bool { return validCounters(c, 3) }

// SenderCounters are the counters of a machine that keeps records for other machines, as the
// sender of a set-up script does: one for each lock, kind, subject and scope, as the records of
// two scopes are for two machines. The sender checks a record with CheckRecord, and TakesRecord
// is the receiver's. They are kept apart from the machine's own Counters, so that what it keeps
// for another does not move its own.
type SenderCounters map[string]Counter

func senderKey(r wire.LockRecord) string { return counterKey(r) + "/" + r.Scope }

// Check is Counters.Check, among the records of the same scope.
func (c SenderCounters) Check(r wire.LockRecord) error { return checkCounter(c, senderKey(r), r) }

// Take is Check, and notes the record.
func (c SenderCounters) Take(r wire.LockRecord) (changed bool, err error) {
	return takeCounter(c, senderKey(r), r)
}

// Highest is the highest number of a lock's kind and subject in any scope. A client signs one
// higher: a machine that takes two of the scopes has one counter for both.
func (c SenderCounters) Highest(root []byte, kind, subject string) int64 {
	under := counterKey(wire.LockRecord{Root: root, Kind: kind, Subject: subject}) + "/"
	var highest int64
	for key, n := range c {
		if strings.HasPrefix(key, under) {
			highest = max(highest, n.Seq)
		}
	}
	return highest
}

// Valid is Counters.Valid.
func (c SenderCounters) Valid() bool { return validCounters(c, 4) }

// setupHash is SetupScriptApproval's hash.
var setupHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SetupScriptOf is the content of a setup.script record, read strictly (DecodeStrict).
func SetupScriptOf(content []byte) (wire.SetupScriptApproval, error) {
	var a wire.SetupScriptApproval
	if err := DecodeStrict(content, &a); err != nil {
		return a, err
	}
	if !setupHash.MatchString(a.Hash) || a.Size < 1 {
		return a, fmt.Errorf("%w: it has no hash and size of a script", ErrInvalid)
	}
	return a, nil
}
