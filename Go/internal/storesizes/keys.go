package storesizes

import (
	"context"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// mostKeys is how many keys one store's measurement lists.
const mostKeys = 2_000_000

// part is the part of the store that key counts to: the object, its folder, or its two.
func (p *Place) part(key string) string {
	first, rest, _ := strings.Cut(strings.TrimPrefix(key, p.Prefix), "/")
	switch p.Part {
	case "folder":
		return first
	case "folders":
		second, _, _ := strings.Cut(rest, "/")
		return first + "/" + second
	}
	return key
}

// Keys measures a family of keys from the store's listing alone: no object is read. A family
// with Walk is listed a folder at a time, so memory holds one folder's keys.
func (r Row) Keys(ctx context.Context, s store.Store, now time.Time) Measure {
	prefixes := []string{r.At.Prefix}
	if r.At.Walk == "folder" {
		folders, err := store.Folders(ctx, s, r.At.Prefix)
		if err != nil {
			return Unmeasured(now, "The store could not be listed.")
		}
		prefixes = prefixes[:0]
		for _, f := range folders {
			prefixes = append(prefixes, r.At.Prefix+f+"/"+r.At.Sub)
		}
	}
	t, listed := newTally(), 0
	for _, prefix := range prefixes {
		keys, err := s.List(ctx, prefix)
		if err != nil {
			return Unmeasured(now, "The store could not be listed.")
		}
		if listed += len(keys); listed > mostKeys {
			return Unmeasured(now, "It has too many records to measure.")
		}
		for _, k := range keys {
			// A key on its own is not the family of those that begin with it.
			if !strings.HasSuffix(r.At.Prefix, "/") && k.Key != r.At.Prefix {
				continue
			}
			part := r.At.part(k.Key)
			n := int64(1)
			if r.At.Skip != "" && strings.HasSuffix(k.Key, r.At.Skip) || !strings.Contains(k.Key, r.At.Only) {
				n = 0
			}
			if k.Size < 0 {
				t.bytesUnknown = true
			}
			t.add(part, max(k.Size, 0), n)
		}
	}
	return t.measure(now, r.At.Count != "", r.LimitPer != "" && r.At.Part != "")
}
