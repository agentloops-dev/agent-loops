package controllink

import (
	"os"
	"path/filepath"
	"testing"
)

// The host keeps the last public address a control plane saw it come from (#7493), and a
// private or loopback one, as from its own control plane, leaves it as it is.
func TestKeepSeenAddressKeepsOnlyAPublicOne(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got := SeenAddress(root); got.IsValid() {
		t.Fatalf("none kept: %v", got)
	}
	for _, c := range []struct{ said, want string }{
		{"198.51.100.9", "198.51.100.9"},
		{"127.0.0.1", "198.51.100.9"},
		{"10.128.0.5", "198.51.100.9"},
		{"not an address", "198.51.100.9"},
		{"::ffff:203.0.113.7", "203.0.113.7"},
		{"2001:db8::7", "2001:db8::7"},
	} {
		if err := KeepSeenAddress(root, c.said); err != nil {
			t.Fatal(err)
		}
		if got := SeenAddress(root); got.String() != c.want {
			t.Errorf("after %q: %v, want %s", c.said, got, c.want)
		}
	}
	// A file that says a private address is not taken.
	if err := os.WriteFile(filepath.Join(root, SeenAddressFile), []byte("10.0.0.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SeenAddress(root); got.IsValid() {
		t.Errorf("a private address was taken: %v", got)
	}
}
