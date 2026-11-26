package machinelock

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func record(t *testing.T, k *ecdsa.PrivateKey, s *State, kind, subject, scope string, seq int64, content string) wire.LockRecord {
	t.Helper()
	r, err := SignRecord(k, s, kind, subject, scope, seq, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// approval is the content of a setup.script record for a script of that text.
func approval(script string) string {
	return fmt.Sprintf(`{"hash":%q,"size":%d}`, ContentHash([]byte(script)), len(script))
}

// resigned is a changed record with a signature over what it now says.
func resigned(t *testing.T, k *ecdsa.PrivateKey, r wire.LockRecord, change func(*wire.LockRecord)) wire.LockRecord {
	t.Helper()
	change(&r)
	r.Key = PublicKey(k)
	sig, err := SignData(k, RecordData(r))
	if err != nil {
		t.Fatal(err)
	}
	r.Sig = sig
	return r
}

func TestARecordChecksOnlyAsItWasSigned(t *testing.T) {
	mac, phone, machine := key(t), key(t), key(t)
	s := lockWith(t, mac, phone, machine)
	r := record(t, phone, s, KindSetupScript, "setup", ScopeMade, 3, approval("echo a"))
	m, err := CheckRecord(s, r)
	if err != nil || !bytes.Equal(m.Key, PublicKey(phone)) {
		t.Fatalf("signer %x, %v", m.Key, err)
	}
	for name, change := range map[string]func(*wire.LockRecord){
		"subject": func(r *wire.LockRecord) { r.Subject = "root" },
		"scope":   func(r *wire.LockRecord) { r.Scope = "m:dev-1" },
		"seq":     func(r *wire.LockRecord) { r.Seq = 4 },
		"content": func(r *wire.LockRecord) { r.Content = []byte(approval("echo b")) },
		"removal": func(r *wire.LockRecord) { r.Content = nil },
		"key":     func(r *wire.LockRecord) { r.Key = PublicKey(mac) },
		"sig":     func(r *wire.LockRecord) { r.Sig = append([]byte{r.Sig[0] ^ 1}, r.Sig[1:]...) },
		"short":   func(r *wire.LockRecord) { r.Sig = r.Sig[:63] },
	} {
		changed := r
		change(&changed)
		if _, err := CheckRecord(s, changed); !errors.Is(err, ErrForged) {
			t.Errorf("another %s: %v", name, err)
		}
	}
	// The kind is signed as text too: one this build does not know is refused before the signature.
	other := r
	other.Kind = "setup.other"
	if _, err := CheckRecord(s, other); !errors.Is(err, ErrInvalid) {
		t.Errorf("another kind: %v", err)
	}
}

func TestOnlyAClientOfTheLockSignsARecord(t *testing.T) {
	mac, phone, machine, recovery, other := key(t), key(t), key(t), key(t), key(t)
	c := newChain(t, mac)
	c.add(mac, wire.LockBody{Kind: wire.LockEntryKindAddClient, Key: PublicKey(phone)})
	c.add(mac, wire.LockBody{Kind: wire.LockEntryKindAddMachine, Key: PublicKey(machine), SealingKey: PublicKey(machine), ID: ptr("dev-1")})
	c.add(mac, wire.LockBody{Kind: wire.LockEntryKindRecovery, Key: PublicKey(recovery)})
	s, err := Check(c.entries)
	if err != nil {
		t.Fatal(err)
	}
	for name, k := range map[string]*ecdsa.PrivateKey{"a machine": machine, "the recovery key": recovery, "a key of no lock": other} {
		if _, err := CheckRecord(s, record(t, k, s, KindSetupScript, "setup", "m:dev-1", 1, "x")); !errors.Is(err, ErrNotMember) {
			t.Errorf("%s signed: %v", name, err)
		}
	}
	r := record(t, phone, s, KindSetupScript, "setup", "m:dev-1", 1, "x")
	if _, err := CheckRecord(s, r); err != nil {
		t.Fatal(err)
	}
	c.add(mac, wire.LockBody{Kind: wire.LockEntryKindRemove, Key: PublicKey(phone)})
	if s, err = Check(c.entries); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckRecord(s, r); !errors.Is(err, ErrNotMember) {
		t.Fatalf("a client that was removed: %v", err)
	}
}

func TestARecordIsOfOneLockAndOnePerson(t *testing.T) {
	mac, machine := key(t), key(t)
	s := lockWith(t, mac, key(t), machine)
	r := record(t, mac, s, KindSetupScript, "setup", "m:dev-1", 1, "x")
	again := lockWith(t, mac, key(t), machine)
	if _, err := CheckRecord(again, r); !errors.Is(err, ErrOtherLock) {
		t.Fatalf("a lock that was replaced: %v", err)
	}
	if _, err := CheckRecord(s, resigned(t, mac, r, func(r *wire.LockRecord) { r.Person = "sam" })); !errors.Is(err, ErrOtherLock) {
		t.Fatalf("another person: %v", err)
	}
}

func TestARecordsParts(t *testing.T) {
	mac := key(t)
	s := lockWith(t, mac, key(t), key(t))
	r := record(t, mac, s, KindSetupScript, "setup", "m:dev-1", 1, "x")
	for name, change := range map[string]func(*wire.LockRecord){
		"version":                 func(r *wire.LockRecord) { r.V = 2 },
		"kind":                    func(r *wire.LockRecord) { r.Kind = "other" },
		"a newline in a kind":     func(r *wire.LockRecord) { r.Kind = "setup.script\nsetup" },
		"no subject":              func(r *wire.LockRecord) { r.Subject = "" },
		"a newline in a subject":  func(r *wire.LockRecord) { r.Subject = "setup\nm:dev-1" },
		"a newline after one":     func(r *wire.LockRecord) { r.Subject = "setup\n" },
		"a path":                  func(r *wire.LockRecord) { r.Subject = "../setup" },
		"a subject with an @":     func(r *wire.LockRecord) { r.Subject = "setup@m:dev-1" },
		"a long subject":          func(r *wire.LockRecord) { r.Subject = strings.Repeat("a", 129) },
		"no scope":                func(r *wire.LockRecord) { r.Scope = "" },
		"no machine":              func(r *wire.LockRecord) { r.Scope = "m:" },
		"a newline in a scope":    func(r *wire.LockRecord) { r.Scope = "m:dev-1\n" },
		"a scope no kind has":     func(r *wire.LockRecord) { r.Scope = "all" },
		"a scope in another case": func(r *wire.LockRecord) { r.Scope = "Made" },
		"number 0":                func(r *wire.LockRecord) { r.Seq = 0 },
		"a negative number":       func(r *wire.LockRecord) { r.Seq = -1 },
		"a number past the most":  func(r *wire.LockRecord) { r.Seq = MostRecordSeq + 1 },
		"a newline in the person": func(r *wire.LockRecord) { r.Person = "al\nex" },
		"too much content":        func(r *wire.LockRecord) { r.Content = make([]byte, MostRecordContent+1) },
	} {
		if _, err := CheckRecord(s, resigned(t, mac, r, change)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	most := resigned(t, mac, r, func(r *wire.LockRecord) { r.Content, r.Seq = make([]byte, MostRecordContent), MostRecordSeq })
	if _, err := CheckRecord(s, most); err != nil {
		t.Fatalf("the most content and the highest number: %v", err)
	}
	if removed := record(t, mac, s, KindSetupScript, "setup", "m:dev-1", 2, ""); !Removed(removed) || Removed(most) {
		t.Fatal("empty content is a removed value")
	}
}

func TestWhereARecordIsTaken(t *testing.T) {
	for _, v := range []struct {
		scope, host string
		made, want  bool
	}{
		{"m:dev-1", "dev-1", false, true}, {"m:dev-1", "dev-1", true, true}, {"m:dev-1", "dev-2", false, false},
		{"m:dev-1", "", false, false}, {"m:", "", false, false}, {ScopeMade, "dev-1", true, true}, {ScopeMade, "dev-1", false, false},
	} {
		err := TakesRecord(wire.LockRecord{Scope: v.scope}, v.host, v.made)
		if (err == nil) != v.want || err != nil && !errors.Is(err, ErrOtherPlace) {
			t.Errorf("%+v: %v", v, err)
		}
	}
}

func TestTheNumberOnlyGoesUp(t *testing.T) {
	mac := key(t)
	s := lockWith(t, mac, key(t), key(t))
	c := Counters{}
	take := func(r wire.LockRecord, want error) {
		t.Helper()
		before := maps.Clone(c)
		if err := c.Check(r); !errors.Is(err, want) || !maps.Equal(before, c) {
			t.Fatalf("check %s %d: %v", r.Scope, r.Seq, err)
		}
		changed, err := c.Take(r)
		if !errors.Is(err, want) || changed == maps.Equal(before, c) {
			t.Fatalf("take %s %d: %v", r.Scope, r.Seq, err)
		}
	}
	made := func(seq int64, content string) wire.LockRecord {
		return record(t, mac, s, KindSetupScript, "setup", ScopeMade, seq, content)
	}
	take(made(5, "a"), nil)
	take(made(5, "a"), nil)
	take(made(5, "b"), ErrTwo)
	take(made(4, "a"), ErrOlder)
	// Every scope of one subject shares the counter.
	take(record(t, mac, s, KindSetupScript, "setup", "m:dev-1", 4, "a"), ErrOlder)
	take(record(t, mac, s, KindSetupScript, "setup", "m:dev-1", 5, "c"), ErrTwo)
	take(record(t, mac, s, KindSetupScript, "setup", "m:dev-1", 7, "c"), nil)
	take(made(6, "d"), ErrOlder)
	// A removed value holds its number.
	take(made(8, ""), nil)
	take(made(7, "c"), ErrOlder)
	// Another subject or lock has its own.
	take(record(t, mac, s, KindSetupScript, "root", ScopeMade, 2, "a"), nil)
	take(record(t, mac, s, KindSetupScript, "root", "m:dev-1", 1, "old"), ErrOlder)
	take(record(t, mac, lockWith(t, mac, key(t), key(t)), KindSetupScript, "setup", ScopeMade, 1, "a"), nil)
	if !c.Valid() {
		t.Fatal("the counters do not read as ones that Take noted")
	}
}

func TestCountersAreBounded(t *testing.T) {
	mac := key(t)
	s := lockWith(t, mac, key(t), key(t))
	c := Counters{}
	r := record(t, mac, s, KindSetupScript, "A", "m:dev-1", 1, "x")
	for i := range MostCounters {
		r.Subject = fmt.Sprintf("A%d", i)
		if _, err := c.Take(r); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := json.Marshal(c); len(b) > 1<<20 {
		t.Fatalf("%d bytes", len(b))
	}
	r.Subject = "more"
	if _, err := c.Take(r); !errors.Is(err, ErrFull) || len(c) != MostCounters || !c.Valid() {
		t.Fatalf("%d counters, %v", len(c), err)
	}
	r.Subject, r.Seq = "A0", 2
	if _, err := c.Take(r); err != nil {
		t.Fatalf("one that is kept goes on: %v", err)
	}
	root := strings.Repeat("ab", 32)
	for name, bad := range map[string]Counters{
		"number": {root + "/a/b": {Seq: 0, Content: ContentHash(nil)}}, "hash": {root + "/a/b": {Seq: 1, Content: "zz"}},
		"name": {"": {Seq: 1, Content: ContentHash(nil)}}, "root": {"ab/a/b": {Seq: 1, Content: ContentHash(nil)}},
		"part": {root + "/a": {Seq: 1, Content: ContentHash(nil)}}, "empty part": {root + "//b": {Seq: 1, Content: ContentHash(nil)}},
	} {
		if bad.Valid() {
			t.Errorf("a counter with another %s reads", name)
		}
	}
}

// A number goes up in steps that are bounded, so that a higher one can always be signed.
func TestTheNumberGoesUpInBoundedSteps(t *testing.T) {
	mac := key(t)
	s := lockWith(t, mac, key(t), key(t))
	c := Counters{}
	at := func(seq int64, want error) {
		t.Helper()
		if _, err := c.Take(record(t, mac, s, KindSetupScript, "setup", ScopeMade, seq, "a")); !errors.Is(err, want) {
			t.Fatalf("%d: %v", seq, err)
		}
	}
	at(MostRecordSeq, ErrFar)
	at(MostFirstSeq+1, ErrFar)
	at(3, nil)
	at(MostRecordSeq, ErrFar)
	at(MostFirstSeq, ErrFar)
	at(3+MostSeqStep+1, ErrFar)
	at(3+MostSeqStep, nil)
	at(4+MostSeqStep, nil)
	c = Counters{}
	at(MostFirstSeq, nil)
	at(MostFirstSeq+1, nil)
}

// A sender keeps the records of each scope apart, and knows the highest number of all of them.
func TestASendersCountersAreOfOneScope(t *testing.T) {
	mac := key(t)
	s := lockWith(t, mac, key(t), key(t))
	c := SenderCounters{}
	at := func(scope string, seq int64, content string, want error) {
		t.Helper()
		r := record(t, mac, s, KindSetupScript, "root", scope, seq, content)
		check := c.Check(r)
		if changed, err := c.Take(r); !errors.Is(err, want) || !errors.Is(check, want) || changed && err != nil {
			t.Fatalf("%s %d: %v, %v", scope, seq, check, err)
		}
	}
	at("m:a", 5, "x", nil)
	at(ScopeMade, 2, "x", nil)
	at("m:b", 3, "y", nil)
	at("m:b", 2, "y", ErrOlder)
	at("m:a", 5, "y", ErrTwo)
	at("m:a", 5+MostSeqStep+1, "y", ErrFar)
	if got := c.Highest(s.Root[:], KindSetupScript, "root"); got != 5 || c.Highest(s.Root[:], KindSetupScript, "setup") != 0 {
		t.Fatal(got)
	}
	if !c.Valid() || Counters(c).Check(record(t, mac, s, KindSetupScript, "root", "m:a", 1, "x")) != nil {
		t.Fatalf("%+v", c)
	}
}

// The vectors that AgentsKit's LockRecordTests and the web page's lockRecords.test.mjs read.
// go test ./internal/machinelock -run TestTheRecordVectors -args -write-record-vectors
const recordVectors = "../../../Packages/AgentsKit/Tests/AgentsKitTests/Fixtures/lock/records.json"

var writeRecordVectors = flag.Bool("write-record-vectors", false, "make the record vectors afresh")

type recordVector struct {
	Name   string          `json:"name"`
	Record wire.LockRecord `json:"record"`
	// Signed is the bytes a signature covers.
	Signed []byte `json:"signed"`
	Host   string `json:"host"`
	Made   bool   `json:"made"`
	// Why is the failure, or nothing for a record that the machine takes.
	Why string `json:"why"`
}

// foreignVector is bytes of another domain, and a client's signature over them.
type foreignVector struct {
	Domain string `json:"domain"`
	Data   []byte `json:"data"`
	Sig    []byte `json:"sig"`
}

// seqVector is a number, the one had last, and why it does not follow.
type seqVector struct {
	Had int64  `json:"had"`
	Seq int64  `json:"seq"`
	Why string `json:"why"`
}

type recordVectorFile struct {
	Lock    wire.Lock           `json:"lock"`
	Kinds   map[string][]string `json:"kinds"`
	Records []recordVector      `json:"records"`
	Foreign []foreignVector     `json:"foreign"`
	// Steps is what CheckSeq says of a number after another; 0 is none.
	Steps []seqVector `json:"steps"`
	// Request and Entry are a record's own signature put where one of another domain goes.
	Request wire.LockSignature `json:"request"`
	Entry   wire.LockEntry     `json:"entry"`
}

func whyOf(err error) string {
	for why, e := range map[string]error{"invalid": ErrInvalid, "otherLock": ErrOtherLock, "notMember": ErrNotMember, "forged": ErrForged, "otherPlace": ErrOtherPlace,
		"older": ErrOlder, "far": ErrFar} {
		if errors.Is(err, e) {
			return why
		}
	}
	return ""
}

func checkedFor(s *State, v recordVector) string {
	_, err := CheckRecord(s, v.Record)
	if err == nil {
		err = TakesRecord(v.Record, v.Host, v.Made)
	}
	return whyOf(err)
}

func makeRecordVectors(t *testing.T) recordVectorFile {
	mac, phone, removed, machine, recovery, other := key(t), key(t), key(t), key(t), key(t), key(t)
	c := newChain(t, mac)
	c.now = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for _, k := range []*ecdsa.PrivateKey{phone, removed} {
		c.add(mac, wire.LockBody{Kind: wire.LockEntryKindAddClient, Key: PublicKey(k)})
	}
	c.add(mac, wire.LockBody{Kind: wire.LockEntryKindAddMachine, Key: PublicKey(machine), SealingKey: PublicKey(machine), ID: ptr("dev-1")})
	c.add(mac, wire.LockBody{Kind: wire.LockEntryKindRecovery, Key: PublicKey(recovery)})
	before, err := Check(c.entries)
	if err != nil {
		t.Fatal(err)
	}
	gone := record(t, removed, before, KindSetupScript, "setup", "m:dev-1", 1, approval("echo a"))
	c.add(mac, wire.LockBody{Kind: wire.LockEntryKindRemove, Key: PublicKey(removed)})
	s, err := Check(c.entries)
	if err != nil {
		t.Fatal(err)
	}
	f := recordVectorFile{Lock: wire.Lock{Person: s.Person, Entries: c.entries}, Kinds: recordKinds}
	add := func(name string, r wire.LockRecord, host string, made bool, why string) {
		f.Records = append(f.Records, recordVector{Name: name, Record: r, Signed: RecordData(r), Host: host, Made: made, Why: why})
	}
	script := approval("echo é <b>\n")
	base := record(t, phone, s, KindSetupScript, "setup", ScopeMade, 7, script)
	add("for a made machine", base, "dev-9", true, "")
	add("for one machine", record(t, mac, s, KindSetupScript, "root", "m:dev-1", 1, approval("true")), "dev-1", false, "")
	add("any bytes", record(t, mac, s, KindSetupScript, "s-1.a_B", "m:dev-1", 1, "\x00\xff\n"), "dev-1", false, "")
	add("a removed value", record(t, mac, s, KindSetupScript, "setup", "m:dev-1", MostRecordSeq, ""), "dev-1", false, "")
	add("not a made machine", base, "dev-9", false, "otherPlace")
	add("a made machine for one machine", record(t, mac, s, KindSetupScript, "setup", "m:dev-1", 1, script), "dev-9", true, "otherPlace")
	add("for another machine", record(t, mac, s, KindSetupScript, "setup", "m:dev-1", 1, script), "dev-2", false, "otherPlace")
	add("a removed client", gone, "dev-1", false, "notMember")
	add("a machine", record(t, machine, s, KindSetupScript, "setup", "m:dev-1", 1, script), "dev-1", false, "notMember")
	add("the recovery key", record(t, recovery, s, KindSetupScript, "setup", "m:dev-1", 1, script), "dev-1", false, "notMember")
	add("no member", record(t, other, s, KindSetupScript, "setup", "m:dev-1", 1, script), "dev-1", false, "notMember")
	add("another lock", record(t, phone, lockWith(t, mac, phone, machine), KindSetupScript, "setup", "m:dev-1", 1, script), "dev-1", false, "otherLock")
	inviteKey := key(t)
	c.add(mac, wire.LockBody{Kind: wire.LockEntryKindInvite, Key: PublicKey(inviteKey), Expires: ptr(wire.DateOf(c.now.Add(5 * time.Minute)))})
	f.Lock.Entries = c.entries
	add("an invite", record(t, inviteKey, s, KindSetupScript, "setup", "m:dev-1", 1, script), "dev-1", false, "notMember")
	for _, n := range [][2]int64{{0, 1}, {0, MostFirstSeq}, {0, MostFirstSeq + 1}, {0, MostRecordSeq}, {3, 2}, {3, 3}, {3, 4},
		{3, 3 + MostSeqStep}, {3, 4 + MostSeqStep}, {MostFirstSeq, MostFirstSeq + 1}, {MostRecordSeq - 1, MostRecordSeq}, {MostRecordSeq, 1}} {
		f.Steps = append(f.Steps, seqVector{n[0], n[1], whyOf(CheckSeq(n[0], n[1]))})
	}
	change := func(name, why string, how func(*wire.LockRecord)) {
		r := base
		how(&r)
		add(name, r, "dev-9", true, why)
	}
	sign := func(name, why string, how func(*wire.LockRecord)) {
		add(name+", signed", resigned(t, phone, base, how), "dev-9", true, why)
	}
	change("a kind it does not know", "invalid", func(r *wire.LockRecord) { r.Kind = "setup.other" })
	change("another subject", "forged", func(r *wire.LockRecord) { r.Subject = "root" })
	change("another scope", "forged", func(r *wire.LockRecord) { r.Scope = "m:dev-9" })
	change("another number", "forged", func(r *wire.LockRecord) { r.Seq = 8 })
	change("another content", "forged", func(r *wire.LockRecord) { r.Content = []byte(approval("echo b")) })
	change("another key", "forged", func(r *wire.LockRecord) { r.Key = PublicKey(mac) })
	change("a short signature", "forged", func(r *wire.LockRecord) { r.Sig = r.Sig[:63] })
	change("a long signature", "forged", func(r *wire.LockRecord) { r.Sig = append(bytes.Clone(r.Sig), 0) })
	change("a signature of zeros", "forged", func(r *wire.LockRecord) { r.Sig = make([]byte, 64) })
	change("a short root", "otherLock", func(r *wire.LockRecord) { r.Root = r.Root[:31] })
	change("another person", "otherLock", func(r *wire.LockRecord) { r.Person = "sam" })
	sign("another person", "otherLock", func(r *wire.LockRecord) { r.Person = "sam" })
	sign("another version", "invalid", func(r *wire.LockRecord) { r.V = 2 })
	sign("no such kind", "invalid", func(r *wire.LockRecord) { r.Kind = "setup.other" })
	sign("a newline in the subject", "invalid", func(r *wire.LockRecord) { r.Subject = "setup\nmade" })
	sign("a newline after the subject", "invalid", func(r *wire.LockRecord) { r.Subject = "setup\n" })
	sign("a letter past ASCII", "invalid", func(r *wire.LockRecord) { r.Subject = "K" })
	sign("a scope no kind has", "invalid", func(r *wire.LockRecord) { r.Scope = "all" })
	sign("a scope of no machine", "invalid", func(r *wire.LockRecord) { r.Scope = "m:" })
	sign("a scope in another case", "invalid", func(r *wire.LockRecord) { r.Scope = "Made" })
	sign("no person", "otherLock", func(r *wire.LockRecord) { r.Person = "" })
	sign("a number past the most", "invalid", func(r *wire.LockRecord) { r.Seq = MostRecordSeq + 1 })
	sign("no number", "invalid", func(r *wire.LockRecord) { r.Seq = 0 })
	sign("a control character in the person", "invalid", func(r *wire.LockRecord) { r.Person = "al\x7fex" })

	_, client, _ := strings.Cut(string(RecordData(base)), "\n")
	foreign := func(domain string, data []byte) {
		sig, err := SignData(phone, data)
		if err != nil {
			t.Fatal(err)
		}
		f.Foreign = append(f.Foreign, foreignVector{Domain: domain, Data: data, Sig: sig})
	}
	foreign("agents-lock/1", append([]byte(domain), client...))
	foreign("agents-lock-channel/1", clientHelloData([]byte(client)))
	foreign("agents-lock-request/1", RequestData("dev-1", 1795521600, "agents/start", []byte(client)))
	foreign("agents-approval/1", []byte("agents-approval/1\n"+client))
	foreign("agents-lock-join/1", []byte("agents-lock-join/1\n"+client))
	f.Request = wire.LockSignature{To: ptr("dev-1"), At: 1795521600, Key: base.Key, Sig: base.Sig}
	f.Entry = wire.LockEntry{Body: RecordData(base), Sig: base.Sig}
	return f
}

func TestTheRecordVectors(t *testing.T) {
	if *writeRecordVectors {
		b, _ := json.MarshalIndent(makeRecordVectors(t), "", "  ")
		if err := os.WriteFile(recordVectors, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(recordVectors)
	if err != nil {
		t.Fatal(err)
	}
	var f recordVectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	s, err := Check(f.Lock.Entries)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(f.Kinds)
	if kinds, _ := json.Marshal(recordKinds); !bytes.Equal(kinds, want) {
		t.Errorf("kinds %s, the vectors have %s", kinds, want)
	}
	for _, v := range f.Records {
		if got := RecordData(v.Record); !bytes.Equal(got, v.Signed) {
			t.Errorf("%s: signs %q", v.Name, got)
		}
		if got := checkedFor(s, v); got != v.Why {
			t.Errorf("%s: %q, want %q", v.Name, got, v.Why)
		}
	}
	for _, v := range f.Steps {
		if got := whyOf(CheckSeq(v.Had, v.Seq)); got != v.Why {
			t.Errorf("%d after %d: %q, want %q", v.Seq, v.Had, got, v.Why)
		}
	}
	// A signature of another domain is not a record's, and a record's is not one of another.
	base := f.Records[0].Record
	for _, v := range f.Foreign {
		r := base
		r.Sig = v.Sig
		if !VerifyData(base.Key, v.Data, v.Sig) || !bytes.HasPrefix(v.Data, []byte(v.Domain+"\n")) {
			t.Errorf("%s: the vector does not check", v.Domain)
		}
		if _, err := CheckRecord(s, r); !errors.Is(err, ErrForged) {
			t.Errorf("%s: taken as a record: %v", v.Domain, err)
		}
		if VerifyData(base.Key, v.Data, base.Sig) {
			t.Errorf("%s: a record's signature checks", v.Domain)
		}
	}
	at := time.Unix(f.Request.At, 0)
	if _, err := CheckRequest(s, "dev-1", "agents/start", []byte("{}"), f.Request, at); !errors.Is(err, ErrForged) {
		t.Errorf("a record as a request: %v", err)
	}
	if _, err := Open(f.Entry); err == nil {
		t.Error("a record as a lock entry opened")
	}
}
