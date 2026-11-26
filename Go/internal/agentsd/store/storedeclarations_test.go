package store

import (
	"strings"
	"testing"
)

// #7350: root-id.json and the root mark hold a root's path, which the system holds to 4 KiB:
// at that path, each is within its declared size.
func TestTheRootFilesAreWithinTheirMostAtTheLongestPath(t *testing.T) {
	t.Parallel()
	path := "/" + strings.Repeat("/", 4095) // a slash is escaped, so it is the longest text
	text := rootIdentityText(rootIdentity{ID: newUUID(), MadeAt: "2026-11-26T00:00:00Z", Path: path})
	if len(text) > 16<<10 {
		t.Fatalf("root-id.json is %d bytes, over 16 KiB", len(text))
	}
	if len(path) > 4<<10 {
		t.Fatalf("the root mark is %d bytes, over 4 KiB", len(path))
	}
}
