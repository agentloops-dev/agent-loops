package hosted

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// Backup and restore of one account (#2226, spec 1003 FR-041). The disk's daily snapshot
// keeps the whole service; these put one account back without the rest going back too:
//
//	<dir>/<account>/account.json   its record in hosted/v1/accounts/
//	<dir>/<account>/records.jsonl  every record under accounts/<id>/, one {"key","data"} a line
//
// Its control key is sealed in its own prefix, so a restore needs the same KMS key.

// stopWithin is how long a running service takes, at most, to stop a suspended copy.
const stopWithin = 70 * time.Second

type backedUp struct {
	Key  string `json:"key"`
	Data []byte `json:"data"`
}

// The bounds of the operator's backups (#6787): they are applied after each backup, as the
// service does not know the operator's folder.
const (
	MaxBackups = 90
	backupAge  = 90 * 24 * time.Hour
	// BackupWarnBytes is the size of an account's backup that Backup's caller warns of. The
	// backup is made all the same (Alex, 2026-11-24): an account is never left without one.
	BackupWarnBytes = 10 << 20
)

// AccountBackup is one account's backup that Backup made.
type AccountBackup struct {
	ID    string
	Bytes int64
}

// Backup copies each account in ids, or every account when there are none, into dir, and
// gives those it copied. One that fails does not stop the others.
func (a Admin) Backup(ctx context.Context, dir string, ids []string) ([]AccountBackup, error) {
	if len(ids) == 0 {
		list, err := a.Accounts(ctx)
		if err != nil {
			return nil, err
		}
		for _, acc := range list {
			ids = append(ids, acc.ID)
		}
	}
	var done []AccountBackup
	var failed []error
	for _, id := range ids {
		n, err := a.backUp(ctx, dir, id)
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", id, err))
			continue
		}
		done = append(done, AccountBackup{ID: id, Bytes: n})
	}
	return done, errors.Join(failed...)
}

// backupCopy is one account's copy in a backup.
type backupCopy struct {
	backup, id string // the backup's folder's name, and the account
	at         time.Time
	whole      bool
}

// PruneBackups takes the backups beside dir, dir among them, under their bounds (#3123, #6787):
// the newest keep, and no copy of an account 90 days old. The newest whole copy of an account
// that is still here always stays, and so does dir. Backups are the folders in dir's parent that
// hold only what Backup writes; they are named by date, so their names sort oldest first. It
// gives what it deleted.
func (a Admin) PruneBackups(ctx context.Context, dir string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, errors.New("keep at least the backup just made")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	accounts, err := a.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, acc := range accounts {
		live[acc.ID] = true
	}
	parent, this := filepath.Dir(dir), filepath.Base(dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	var names []string // ReadDir sorts by name
	copies := map[string][]backupCopy{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if list := backupCopies(parent, e.Name()); list != nil {
			names = append(names, e.Name())
			copies[e.Name()] = list
		}
	}
	stays := newestCopies(names, copies, live)
	// dir is one of those kept, whatever its name, and so is a backup named after it: it is newer.
	spare := len(names) - keep
	var gone []string
	for _, name := range names {
		over := spare > 0 && name < this
		if over {
			spare--
		}
		left := len(copies[name])
		for _, c := range copies[name] {
			if stays[[2]string{name, c.id}] || !over && a.now().Sub(c.at) < backupAge {
				continue
			}
			p := filepath.Join(parent, name, c.id)
			if err := os.RemoveAll(p); err != nil {
				return gone, err
			}
			gone = append(gone, p)
			left--
		}
		if left == 0 {
			if err := os.Remove(filepath.Join(parent, name)); err != nil {
				return gone, err
			}
		}
	}
	return gone, nil
}

// newestCopies is the newest whole copy of each live account: by its backup's name, and by its
// own time, as a folder can have another name or be copied back from elsewhere.
func newestCopies(names []string, copies map[string][]backupCopy, live map[string]bool) map[[2]string]bool {
	byName, byTime := map[string]backupCopy{}, map[string]backupCopy{}
	for _, name := range names {
		for _, c := range copies[name] {
			if !c.whole || !live[c.id] {
				continue
			}
			byName[c.id] = c
			if was, ok := byTime[c.id]; !ok || !c.at.Before(was.at) {
				byTime[c.id] = c
			}
		}
	}
	stays := map[[2]string]bool{}
	for _, newest := range []map[string]backupCopy{byName, byTime} {
		for id, c := range newest {
			stays[[2]string{c.backup, id}] = true
		}
	}
	return stays
}

// backupCopies is the accounts' copies in parent's folder name, or none when it holds anything
// that Backup does not write. A copy that was cut short is not whole.
func backupCopies(parent, name string) []backupCopy {
	entries, err := os.ReadDir(filepath.Join(parent, name))
	if err != nil {
		return nil
	}
	var list []backupCopy
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			return nil
		}
		files, err := os.ReadDir(filepath.Join(parent, name, e.Name()))
		if err != nil || len(files) == 0 {
			return nil
		}
		c := backupCopy{backup: name, id: e.Name()}
		has := map[string]bool{}
		for _, f := range files {
			fi, err := f.Info()
			if err != nil || !fi.Mode().IsRegular() || f.Name() != "account.json" && f.Name() != "records.jsonl" && !strings.HasPrefix(f.Name(), ".records-") {
				return nil
			}
			has[f.Name()] = true
			if fi.ModTime().After(c.at) {
				c.at = fi.ModTime()
			}
		}
		c.whole = has["account.json"] && has["records.jsonl"]
		list = append(list, c)
	}
	return list
}

func (a Admin) backUp(ctx context.Context, dir, id string) (int64, error) {
	if !validID(id) {
		return 0, fmt.Errorf("%q is not an account's id", id)
	}
	o, err := a.Store.Get(ctx, accountKey(id))
	if err != nil {
		return 0, err
	}
	if o == nil {
		return 0, errors.New("there is no such account")
	}
	into := filepath.Join(dir, id)
	if err := os.MkdirAll(into, 0o700); err != nil {
		return 0, err
	}
	// Written beside, then moved, so a backup cut short leaves the one before whole.
	// store-bounds: kind=blob; count=90 backups at most, 7 by default; age=90d; applied=after each backup (PruneBackups)
	tmp, err := os.CreateTemp(into, ".records-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	st := accountStore(a.Store, id)
	keys, err := st.List(ctx, "")
	if err != nil {
		_ = tmp.Close()
		return 0, err
	}
	for _, k := range keys {
		r, err := st.Get(ctx, k.Key)
		if err != nil {
			_ = tmp.Close()
			return 0, err
		}
		if r == nil {
			continue
		}
		if err := enc.Encode(backedUp{Key: k.Key, Data: r.Data}); err != nil {
			_ = tmp.Close()
			return 0, err
		}
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(into, "records.jsonl")); err != nil {
		return 0, err
	}
	// store-bounds: kind=blob; count=90 backups at most, 7 by default; age=90d; applied=after each backup (PruneBackups)
	if err := os.WriteFile(filepath.Join(into, "account.json"), o.Data, 0o600); err != nil {
		return 0, err
	}
	size, err := os.Stat(filepath.Join(into, "records.jsonl"))
	if err != nil {
		return 0, err
	}
	return size.Size() + int64(len(o.Data)), nil
}

// Delete deletes an account and its control plane's records, as Delete Account does. It
// must be suspended a minute first, so no running copy writes after the delete.
func (a Admin) Delete(ctx context.Context, id string) error {
	acc, err := a.accounts().get(ctx, id)
	if err != nil {
		return err
	}
	if acc == nil {
		return fmt.Errorf("there is no account %q", id)
	}
	if acc.Suspended == nil {
		return fmt.Errorf("%s is running: suspend it first (agents-hosted admin suspend %s deleting), then delete it", id, id)
	}
	if a.now().Sub(acc.Suspended.At) < stopWithin {
		return fmt.Errorf("%s was suspended %s ago: its control plane may still be running; try again in a minute", id, a.now().Sub(acc.Suspended.At).Round(time.Second))
	}
	return a.accounts().remove(ctx, acc)
}

// Restore makes one account's records what dir holds for it: each record put back, and
// each record it did not hold deleted. Nothing outside accounts/<id>/ changes, except an
// account that was deleted: its record, and its sign-in's link, come back.
//
// The account must be suspended, or gone, so no running copy writes over it: suspend,
// restore, unsuspend.
func (a Admin) Restore(ctx context.Context, dir, id string) (int, error) {
	if !validID(id) {
		return 0, fmt.Errorf("%q is not an account's id", id)
	}
	from := filepath.Join(dir, id)
	data, backed, err := readBackedAccount(from, dir, id)
	if err != nil {
		return 0, err
	}
	now, err := a.accounts().get(ctx, id)
	if err != nil {
		return 0, err
	}
	if err := a.checkRestorable(ctx, id, now, backed); err != nil {
		return 0, err
	}
	n, err := a.putBackRecords(ctx, from, id)
	if err != nil {
		return n, err
	}
	// Its size is counted again at its next start.
	if err := a.Store.Delete(ctx, usageKey(id)); err != nil {
		return n, err
	}
	if now == nil {
		if err := a.relinkDeleted(ctx, id, backed, data); err != nil {
			return n, err
		}
	}
	return n, nil
}

// readBackedAccount reads the account record of a backup in from, and checks it is id's.
func readBackedAccount(from, dir, id string) ([]byte, Account, error) {
	var backed Account
	data, err := os.ReadFile(filepath.Join(from, "account.json"))
	if err != nil {
		return nil, backed, fmt.Errorf("no backup of %s in %s: %w", id, dir, err)
	}
	if err := json.Unmarshal(data, &backed); err != nil || backed.ID != id {
		return nil, backed, fmt.Errorf("%s/account.json is not %s's record", from, id)
	}
	return data, backed, nil
}

// checkRestorable refuses a restore over an account that runs, or may still run, and over
// a deleted one whose sign-in has another account now.
func (a Admin) checkRestorable(ctx context.Context, id string, now *Account, backed Account) error {
	if now != nil && now.Suspended == nil {
		return fmt.Errorf("%s is running: suspend it first (agents-hosted admin suspend %s restoring), restore, then unsuspend it", id, id)
	}
	// A running service stops a suspended copy within a minute (sweepAccounts).
	if now != nil && a.now().Sub(now.Suspended.At) < stopWithin {
		return fmt.Errorf("%s was suspended %s ago: its control plane may still be running; try again in a minute", id, a.now().Sub(now.Suspended.At).Round(time.Second))
	}
	if now != nil {
		return nil
	}
	// The person may have signed up again since: then this backup is not theirs to take
	// back, and the operator decides.
	o, err := a.Store.Get(ctx, signInKey(backed.SignIn))
	if err != nil {
		return err
	}
	if o == nil {
		return nil
	}
	var link struct {
		Account string `json:"account"`
	}
	if json.Unmarshal(o.Data, &link) == nil && link.Account != id {
		return fmt.Errorf("%s has another account now, %s: delete it first", backed.SignIn, link.Account)
	}
	return nil
}

// putBackRecords puts each record of from/records.jsonl back in the account's store, and
// deletes each record the backup did not hold. It gives how many it put back. The bytes go back
// as they are, with no largest record (#7251): the backup can be the only copy of one.
func (a Admin) putBackRecords(ctx context.Context, from, id string) (int, error) {
	f, err := os.Open(filepath.Join(from, "records.jsonl"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st := accountStore(a.Store, id)
	kept := map[string]bool{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scan.Scan() {
		var r backedUp
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil || r.Key == "" {
			return len(kept), fmt.Errorf("%s/records.jsonl line %d is not a record", from, len(kept)+1)
		}
		if _, err := st.Put(ctx, r.Key, r.Data, store.Always); err != nil {
			return len(kept), err
		}
		kept[r.Key] = true
	}
	if err := scan.Err(); err != nil {
		return len(kept), err
	}
	keys, err := st.List(ctx, "")
	if err != nil {
		return len(kept), err
	}
	for _, k := range keys {
		if kept[k.Key] {
			continue
		}
		if err := st.Delete(ctx, k.Key); err != nil {
			return len(kept), err
		}
	}
	return len(kept), nil
}

// relinkDeleted brings back a deleted account's record and its sign-in's link.
func (a Admin) relinkDeleted(ctx context.Context, id string, backed Account, data []byte) error {
	link, _ := json.Marshal(map[string]string{"account": id})
	if _, err := a.Store.Put(ctx, signInKey(backed.SignIn), link, store.Absent); err != nil && !store.IsConflict(err) {
		return err
	}
	if _, err := a.Store.Put(ctx, accountKey(id), data, store.Absent); err != nil && !store.IsConflict(err) {
		return err
	}
	return nil
}
