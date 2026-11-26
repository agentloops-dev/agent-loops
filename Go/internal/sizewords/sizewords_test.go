package sizewords

import "testing"

func TestOf(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 bytes", 1: "1 byte", 1023: "1023 bytes", 1 << 10: "1 KB", 1536: "1.5 KB", 256 << 10: "256 KB",
		4 << 20: "4 MB", 4<<20 + 1: "4.1 MB", 4<<20 + 300<<10: "4.3 MB", 1<<20 - 1: "1 MB",
		1<<30 + 1<<29: "1.5 GB", 3 << 40: "3 TB",
	} {
		if got := Of(n); got != want {
			t.Errorf("Of(%d) = %q, want %q", n, got, want)
		}
	}
}
