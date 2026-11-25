package storesizes

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// mostFiles is how many files and folders one store's measurement looks at.
	mostFiles = 50_000
	// mostCounted is the largest JSON file that is read to count its entries.
	mostCounted = 64 << 20
)

var errMany = errors.New("too many files")

// Files measures a store of files in bases, the folders its Base is here. It reads a JSON file
// to count its entries only when the files changed since before, and never follows a link.
func (r Row) Files(bases []string, before Measure, now time.Time) Measure {
	if len(bases) == 0 {
		if r.At.Base == "folders" {
			return newTally().measure(now, r.At.Count != "", r.LimitPer != "")
		}
		return Unmeasured(now, "It has no place on this machine.")
	}
	l := look{row: r, tally: newTally(), stamp: fnv.New64a()}
	for _, base := range bases {
		for _, glob := range r.At.Files {
			switch err := l.match(base, glob); {
			case errors.Is(err, errMany):
				return Unmeasured(now, "It has too many files to measure.")
			case err != nil:
				return Unmeasured(now, "Some of its files could not be read.")
			}
		}
	}
	t := l.tally
	if r.At.Count == "" || r.At.Count == "files" {
		return t.measure(now, r.At.Count != "", r.LimitPer != "")
	}
	sum := strconv.FormatUint(l.stamp.Sum64(), 16)
	if before.Stamp == sum {
		m := t.measure(now, false, r.LimitPer != "")
		m.Records, m.LargestRecords, m.Stamp = before.Records, before.LargestRecords, sum
		return m
	}
	t.countUnknown = !r.count(t, l.paths)
	m := t.measure(now, true, r.LimitPer != "")
	m.Stamp = sum
	return m
}

// look is one store's files as they are found.
type look struct {
	row   Row
	tally *tally
	stamp hash.Hash64
	paths []string
	seen  int
}

// match looks at what glob matches in base.
func (l *look) match(base, glob string) error {
	paths, err := filepath.Glob(filepath.Join(escape(base), glob))
	if err != nil {
		return err
	}
	for _, path := range paths {
		part := path
		switch l.row.At.Part {
		case "base":
			part = base
		case "pattern":
			part = glob
		case "folder":
			part = filepath.Dir(path)
		}
		if err := l.walk(part, path); err != nil {
			return err
		}
	}
	return nil
}

// walk adds the files at path to part. One that goes meanwhile is not there.
func (l *look) walk(part, path string) error {
	return filepath.WalkDir(path, func(p string, e fs.DirEntry, err error) error {
		if l.seen++; l.seen > mostFiles {
			return errMany
		}
		if err == nil && !e.Type().IsRegular() {
			return nil
		}
		var info fs.FileInfo
		if err == nil {
			info, err = e.Info()
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		n := int64(0)
		if l.row.At.Count == "files" {
			n = 1
		}
		l.tally.add(part, info.Size(), n)
		l.paths = append(l.paths, p)
		fmt.Fprintln(l.stamp, p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
}

// count adds the entries of the JSON files, and says whether it could.
func (r Row) count(t *tally, paths []string) bool {
	how, names, _ := strings.Cut(r.At.Count, ":")
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || info.Size() > mostCounted {
			return false
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: a declared store of this machine; only its entries are counted
		if err != nil {
			return false
		}
		if names == "" {
			n, ok := entries(data)
			if !ok {
				return false
			}
			t.add(path, 0, n)
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return false
		}
		for _, name := range strings.Split(names, ",") {
			n, ok := entries(fields[name])
			if !ok {
				return false
			}
			part := path
			if how == "fields" {
				part = name
			}
			t.add(part, 0, n)
		}
	}
	return true
}

// entries is how many entries a JSON object or array has; one that is missing or null has none.
func entries(data []byte) (int64, bool) {
	if len(data) == 0 || string(data) == "null" {
		return 0, true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) == nil {
		return int64(len(object)), true
	}
	var list []json.RawMessage
	if json.Unmarshal(data, &list) == nil {
		return int64(len(list)), true
	}
	return 0, false
}

// escape is a folder's path in a glob: its own characters match themselves.
func escape(path string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(path)
}
