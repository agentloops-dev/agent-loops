package agentsd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"code.dny.dev/ssrf"
	"golang.org/x/net/idna"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The hosts that the person allowed forge/image to read images from (#7138, epic #7136):
// <settings>/image-hosts.json, one entry for each machine by its root id, so machines that share
// ~/.agents keep their own. The list is empty until the person allows a host that they saw
// blocked; the forges' own hosts are code (forgeImageHosts) and never in it. Only the person's
// own clients read or change it. A hand edit counts at the next image.
const (
	imageHostsFile = "image-hosts.json"
	// imageHostsMost is the hosts of one machine, imageHostsMostBytes its entry, and
	// imageHostsMachines the machines of one file. A write over one is refused: the list is the
	// person's own, so nothing is evicted.
	imageHostsMost      = 100
	imageHostsMostBytes = 32 << 10
	imageHostsMachines  = 100
	// imageHostMost is the longest host, as DNS has it.
	imageHostMost = 253
	// imageShowMost is the hosts of one read that the person can show: the first and each redirect's.
	imageShowMost = forgeImageRedirects + 1
	// imageHostConnections is how many reads of one host that the person chose run at once.
	imageHostConnections = 4
)

// imageHostsState is the daemon's copy of this machine's list, while the file is as it was read.
type imageHostsState struct {
	mu    sync.Mutex
	stamp fs.FileInfo // nil for no file
	read  bool
	hosts []wire.ImageHost
	set   map[string]bool
}

// imageHostName is a host a person can allow or show, in lower case ASCII; ok is false for an IP
// address, a name with no dot, localhost, .local, a wildcard, and one that is no host at all.
func imageHostName(raw string) (string, bool) {
	// A name longer than the limit in UTF-8 can be shorter in ASCII, but not by this much.
	if len(raw) > 4*imageHostMost {
		return "", false
	}
	host, err := idna.Lookup.ToASCII(raw)
	if err != nil {
		// Lookup refuses names that do resolve, as one with an underscore; they are ASCII already.
		host = strings.ToLower(raw)
	}
	if host == "" || len(host) > imageHostMost || !strings.Contains(host, ".") {
		return "", false
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || strings.Trim(l, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			return "", false
		}
	}
	// A last label that is a number makes the whole an IP address, also in its odd forms (127.1, 0x7f.1).
	last := labels[len(labels)-1]
	if last[0] >= '0' && last[0] <= '9' || last == "localhost" || last == "local" {
		return "", false
	}
	return host, true
}

// imageHostOf is the host of a URL that a person can allow or show: https, on the default port,
// with no user.
func imageHostOf(u *url.URL) (string, bool) {
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return "", false
	}
	return imageHostName(u.Hostname())
}

func (d *Daemon) imageHostsPath() string { return filepath.Join(d.SettingsDir, imageHostsFile) }

type imageHostsEntry struct {
	Hosts []wire.ImageHost `json:"hosts"`
}

// imageHostEntries is every machine's entry, as it is in the file. imageHosts.mu is held.
func (d *Daemon) imageHostEntries() (map[string]json.RawMessage, error) {
	entries := map[string]json.RawMessage{}
	b, err := os.ReadFile(d.imageHostsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return entries, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &entries)
	}
	if err != nil {
		return nil, fmt.Errorf("%s could not be read: %w", d.imageHostsPath(), err)
	}
	if entries == nil {
		entries = map[string]json.RawMessage{}
	}
	return entries, nil
}

// loadImageHosts reads this machine's list again when the file changed. A host that breaks the
// rule is ignored, and a file that cannot be read allows none. imageHosts.mu is held.
func (d *Daemon) loadImageHosts() error {
	s := &d.imageHosts
	stamp, err := os.Stat(d.imageHostsPath())
	if err != nil {
		stamp = nil
	}
	if s.read && (stamp == nil && s.stamp == nil || stamp != nil && s.stamp != nil && os.SameFile(stamp, s.stamp) &&
		stamp.Size() == s.stamp.Size() && stamp.ModTime().Equal(s.stamp.ModTime())) {
		return nil
	}
	s.read, s.stamp, s.hosts, s.set = false, stamp, []wire.ImageHost{}, map[string]bool{}
	entries, err := d.imageHostEntries()
	if err != nil {
		return err
	}
	var entry imageHostsEntry
	if raw := entries[d.Store.RootID]; raw != nil {
		if err := diskjson.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("%s could not be read: %w", d.imageHostsPath(), err)
		}
	}
	for _, h := range entry.Hosts {
		if name, ok := imageHostName(h.Host); ok && name == h.Host && !s.set[name] && len(s.hosts) < imageHostsMost {
			s.hosts, s.set[name] = append(s.hosts, h), true
		}
	}
	s.read = true
	return nil
}

// allowedImageHost says that the person allowed host. A list that cannot be read allows none.
func (d *Daemon) allowedImageHost(host string) bool {
	d.imageHosts.mu.Lock()
	defer d.imageHosts.mu.Unlock()
	return d.loadImageHosts() == nil && d.imageHosts.set[host]
}

// changeImageHosts writes this machine's list as change leaves it, among the other machines'.
func (d *Daemon) changeImageHosts(change func([]wire.ImageHost) ([]wire.ImageHost, error)) (wire.ImageHosts, error) {
	s := &d.imageHosts
	s.mu.Lock()
	defer s.mu.Unlock()
	refused := func(err error) (wire.ImageHosts, error) {
		return wire.ImageHosts{}, &Error{Code: wire.FailureCouldNotSave, Message: "The image hosts were not saved: " + err.Error()}
	}
	if err := d.loadImageHosts(); err != nil {
		return refused(err)
	}
	hosts, err := change(slices.Clone(s.hosts))
	if err != nil {
		return wire.ImageHosts{}, err
	}
	if slices.Equal(hosts, s.hosts) {
		return wire.ImageHosts{Hosts: hosts}, nil
	}
	entries, err := d.imageHostEntries()
	if err != nil {
		return refused(err)
	}
	entry, err := diskjson.Marshal(imageHostsEntry{Hosts: hosts})
	if err != nil {
		return refused(err)
	}
	if err := tooLarge(imageHostsFile, entry, imageHostsMostBytes); err != nil {
		return refused(err)
	}
	if _, ok := entries[d.Store.RootID]; !ok && len(entries) >= imageHostsMachines {
		return refused(fmt.Errorf("%s has %d machines, the most it keeps; remove one that is gone: %w", d.imageHostsPath(), len(entries), store.ErrLimit))
	}
	entries[d.Store.RootID] = entry
	b, err := json.MarshalIndent(entries, "", "  ")
	if err == nil {
		err = os.MkdirAll(d.SettingsDir, 0o700)
	}
	if err == nil {
		// store-bounds: kind=key-value; count=100 hosts a machine, 100 machines; size=32KiB a machine; age=until-deleted; applied=on write (refused)
		err = store.WriteAtomically(d.imageHostsPath(), b, 0o600)
	}
	if err != nil {
		return refused(err)
	}
	// The answer is the list as it is stored.
	s.read = false
	if err := d.loadImageHosts(); err != nil {
		return refused(err)
	}
	return wire.ImageHosts{Hosts: slices.Clone(s.hosts)}, nil
}

func (d *Daemon) allowImageHost(raw string) (wire.ImageHosts, error) {
	host, ok := imageHostName(raw)
	if !ok {
		return wire.ImageHosts{}, Invalid("%q is not a host that images can be allowed from.", raw)
	}
	return d.changeImageHosts(func(hosts []wire.ImageHost) ([]wire.ImageHost, error) {
		if slices.ContainsFunc(hosts, func(h wire.ImageHost) bool { return h.Host == host }) {
			return hosts, nil
		}
		if len(hosts) >= imageHostsMost {
			return nil, Invalid("The list is full (%d hosts). Remove one in Settings ▸ Image Hosts.", imageHostsMost)
		}
		return append(hosts, wire.ImageHost{Host: host, AddedAt: wire.DateOf(nowFunc())}), nil
	})
}

func (d *Daemon) removeImageHost(raw string) (wire.ImageHosts, error) {
	host, ok := imageHostName(raw)
	if !ok {
		return wire.ImageHosts{}, Invalid("%q is not a host.", raw)
	}
	list, err := d.changeImageHosts(func(hosts []wire.ImageHost) ([]wire.ImageHost, error) {
		return slices.DeleteFunc(hosts, func(h wire.ImageHost) bool { return h.Host == host }), nil
	})
	if err == nil {
		d.forgeImages.dropHost(host)
	}
	return list, err
}

func (d *Daemon) registerImageHosts() {
	refused := func(c *Client) error {
		if personsClient(c) {
			return nil
		}
		return &Error{Code: CodeNotPermitted, Message: "Only your own clients can see or change the image hosts."}
	}
	On(d, wire.CallImageHostsList, func(_ context.Context, c *Client, _ wire.Empty) (wire.ImageHosts, error) {
		if err := refused(c); err != nil {
			return wire.ImageHosts{}, err
		}
		d.imageHosts.mu.Lock()
		defer d.imageHosts.mu.Unlock()
		if err := d.loadImageHosts(); err != nil {
			return wire.ImageHosts{}, err
		}
		return wire.ImageHosts{Hosts: slices.Clone(d.imageHosts.hosts)}, nil
	})
	On(d, wire.CallImageHostsAllow, func(_ context.Context, c *Client, r wire.ImageHostRequest) (wire.ImageHosts, error) {
		if err := refused(c); err != nil {
			return wire.ImageHosts{}, err
		}
		return d.allowImageHost(r.Host)
	})
	On(d, wire.CallImageHostsRemove, func(_ context.Context, c *Client, r wire.ImageHostRequest) (wire.ImageHosts, error) {
		if err := refused(c); err != nil {
			return wire.ImageHosts{}, err
		}
		return d.removeImageHost(r.Host)
	})
}

// imageGuard refuses every address that is not public, and every port but https's, at each
// connection, after the name resolved: so a name that resolves again is checked again. The
// registries' lists leave out one metadata address that is in public space: Azure's.
var imageGuard = ssrf.New(ssrf.WithPorts(443), ssrf.WithDeniedV4Prefixes(netip.MustParsePrefix("168.63.129.16/32")))

// publicImageTransport reads from the hosts that the person chose. It has no proxy, as a proxy
// would resolve the name in its place. A Transport with its own dialer speaks HTTP/1, so
// MaxConnsPerHost bounds the reads of one host.
var publicImageTransport http.RoundTripper = newPublicImageTransport(&net.Dialer{Timeout: forgeImageWait, Control: imageGuard.Safe})

func newPublicImageTransport(dialer *net.Dialer) *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    forgeImageWait,
		MaxConnsPerHost:        imageHostConnections,
		MaxIdleConns:           16,
		IdleConnTimeout:        10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
}
