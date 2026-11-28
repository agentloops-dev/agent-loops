package controllink

import (
	"bytes"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// SeenAddressFile, in a host's root, is the last public IP address that a control plane saw
// this host's uplink come from (#7493), as its answer to machine/hello said it. A cloud VM
// behind NAT (GCP, AWS) has only a private address on its interfaces: `agentsd install
// --control` takes this one when it finds no public address of its own.
const SeenAddressFile = "uplink-address"

// KeepSeenAddress keeps address in root when it is a public IP address and not the one kept
// already. Any other value changes nothing, so a host that later joins its own control plane
// on loopback keeps the public address that an uplink before said.
func KeepSeenAddress(root, address string) error {
	addr, err := netip.ParseAddr(address)
	if err != nil || !PublicAddress(addr) {
		return nil
	}
	text := []byte(addr.Unmap().String() + "\n")
	path := filepath.Join(root, SeenAddressFile)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, text) {
		return nil
	}
	tmp := filepath.Join(root, "."+SeenAddressFile+".tmp")
	// store-bounds: not a store: one IP address, at most 40 bytes, rewritten whole when it changes
	if err := os.WriteFile(tmp, text, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SeenAddress is the public address kept in root, or an invalid one when there is none.
func SeenAddress(root string) netip.Addr {
	text, err := os.ReadFile(filepath.Join(root, SeenAddressFile))
	if err != nil {
		return netip.Addr{}
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(string(text)))
	if err != nil || !PublicAddress(addr) {
		return netip.Addr{}
	}
	return addr
}

// PublicAddress says clients elsewhere can dial addr: a global unicast address that is not
// private. 100.64/10 stays in: a machine is seen to come from it only inside a tailnet.
func PublicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsGlobalUnicast() && !addr.IsPrivate()
}
