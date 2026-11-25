package agentsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"code.dny.dev/ssrf"
	"golang.org/x/net/dns/dnsmessage"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// chosenImages is a daemon with one fake web for the forges' hosts and another for the hosts that
// the person chose, so a test sees which one each request went to, and all its headers.
type chosenImages struct {
	d              *Daemon
	person         *Client
	forges, chosen *headerWeb
}

// headerWeb is a fakeImageWeb that keeps each request's headers too.
type headerWeb struct {
	fakeImageWeb
	headers []http.Header
}

func (w *headerWeb) RoundTrip(req *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.headers = append(w.headers, req.Header.Clone())
	w.mu.Unlock()
	res, err := w.fakeImageWeb.RoundTrip(req)
	if res != nil {
		if res.Header == nil {
			res.Header = http.Header{}
		}
		res.Header.Set("Set-Cookie", "who=me; Path=/")
	}
	return res, err
}

type imageAnswers = map[string]func() *http.Response

func newChosenImages(t *testing.T, answers imageAnswers) *chosenImages {
	t.Helper()
	d, _, _, _ := forgeDaemon(t)
	d.SettingsDir = t.TempDir()
	x := &chosenImages{d: d, person: &Client{Role: RoleControl, d: d},
		forges: &headerWeb{fakeImageWeb: fakeImageWeb{answers: answers}}, chosen: &headerWeb{fakeImageWeb: fakeImageWeb{answers: answers}}}
	d.forgeImageTransport, d.publicImageTransport = x.forges, x.chosen
	return x
}

func (x *chosenImages) read(u string, show ...string) (wire.ForgeImage, error) {
	var image wire.ForgeImage
	err := callAs(x.d, x.person, "forge/image", wire.ForgeImageRequest{URL: u, Show: show}, &image)
	return image, err
}

func (x *chosenImages) mustRead(t *testing.T, u string, show ...string) wire.ForgeImage {
	t.Helper()
	image, err := x.read(u, show...)
	if err != nil || !bytes.Equal(image.Data, pngBytes) {
		t.Fatalf("%s: %d bytes, %v", u, len(image.Data), err)
	}
	return image
}

func (x *chosenImages) change(t *testing.T, method, host string) wire.ImageHosts {
	t.Helper()
	var list wire.ImageHosts
	must(t, callAs(x.d, x.person, method, wire.ImageHostRequest{Host: host}, &list))
	return list
}

func (x *chosenImages) asked(t *testing.T, forges, chosen string) {
	t.Helper()
	if f, c := strings.Join(x.forges.requests(), " "), strings.Join(x.chosen.requests(), " "); f != forges || c != chosen {
		t.Fatalf("asked the forges' hosts %q and the chosen %q, wanted %q and %q", f, c, forges, chosen)
	}
}

// mustBlock is a read that is blocked, at host, which from redirected to.
func (x *chosenImages) mustBlock(t *testing.T, host, from, u string, show ...string) {
	t.Helper()
	_, err := x.read(u, show...)
	e := asError(err)
	if e == nil || e.Code != wire.FailureImageBlocked {
		t.Fatalf("%s: %v, wanted imageBlocked", u, err)
	}
	b, ok := e.Data.(wire.ImageBlocked)
	got := ""
	if b.From != nil {
		got = *b.From
	}
	if !ok || b.Host != host || got != from {
		t.Fatalf("%s: %+v from %q, wanted %s from %q", u, e.Data, got, host, from)
	}
}

// mustFail is a read that fails, and is not blocked.
func (x *chosenImages) mustFail(t *testing.T, u string, show ...string) string {
	t.Helper()
	_, err := x.read(u, show...)
	if err == nil {
		t.Fatalf("%s: no error", u)
	}
	if e := asError(err); e != nil && e.Code == wire.FailureImageBlocked {
		t.Fatalf("%s: blocked: %v", u, err)
	}
	return err.Error()
}

const (
	pics = "https://pics.example.com/a.png"
	cdn  = "https://cdn.example.net/a.png"
)

func TestABlockedHostIsNamedWithNoRequest(t *testing.T) {
	t.Parallel()

	x := newChosenImages(t, imageAnswers{pics: imageAnswer("image/png", pngBytes)})
	x.mustBlock(t, "pics.example.com", "", pics)
	// Look-alike hosts are named whole, in ASCII.
	x.mustBlock(t, "raw.githubusercontent.com.evil.example", "", "https://raw.githubusercontent.com.evil.example/a.png")
	x.mustBlock(t, "github.com.evil.example", "", "https://GitHub.com.Evil.example/user-attachments/assets/abc")
	x.mustBlock(t, "xn--gthub-n4a.com", "", "https://gıthub.com/user-attachments/assets/abc")
	x.mustBlock(t, "pics.example.com", "", "https://pics。example.com:443/a.png")
	x.asked(t, "", "")
}

// What a person cannot allow is another failure: no Show and no Allow.
func TestOtherFailuresAreNotBlocked(t *testing.T) {
	t.Parallel()

	const redirects = "https://pics.example.com/to-http"
	x := newChosenImages(t, imageAnswers{
		redirects:                          redirectTo("http://cdn.example.net/a.png"),
		"https://pics.example.com/to-ip":   redirectTo("https://169.254.169.254/latest"),
		"https://pics.example.com/to-port": redirectTo("https://cdn.example.net:8443/a.png"),
		"https://pics.example.com/svg":     imageAnswer("image/png", []byte("<svg/>")),
		"https://pics.example.com/big":     imageAnswer("image/png", append(bytes.Clone(pngBytes), make([]byte, forgeImageMost)...)),
	})
	x.change(t, "imageHosts/allow", "pics.example.com")
	for _, u := range []string{"http://other.example.com/a.png", "https://other.example.com:8443/a.png", "https://u:p@other.example.com/a.png",
		"https://127.0.0.1/a.png", "https://[::1]/a.png", "https://127.1/a.png", "https://2130706433/a.png", "https://0x7f.0x1/a.png",
		"https://localhost/a.png", "https://a.localhost/a.png", "https://mac.local/a.png", "https://intranet/a.png", "https://a.example.com./a.png",
		"file:///etc/passwd", "ftp://other.example.com/a.png"} {
		x.mustFail(t, u)
	}
	x.asked(t, "", "")
	for _, u := range []string{redirects, "https://pics.example.com/to-ip", "https://pics.example.com/to-port", "https://pics.example.com/svg",
		"https://pics.example.com/big", "https://pics.example.com/404"} {
		x.mustFail(t, u)
	}
	if n := len(x.chosen.requests()); n != 6 {
		t.Fatalf("%d requests: a redirect off https was followed", n)
	}
}

// One Show is one exact host, for one read, and is remembered nowhere.
func TestShowReadsOneImageOnce(t *testing.T) {
	t.Parallel()

	x := newChosenImages(t, imageAnswers{pics: imageAnswer("image/png", pngBytes)})
	if image := x.mustRead(t, pics, "Pics.Example.com"); image.Keep == nil || *image.Keep {
		t.Fatal("a shown image is kept")
	}
	x.mustBlock(t, "pics.example.com", "", pics)
	x.mustBlock(t, "pics.example.com", "", pics, "example.com")
	x.mustBlock(t, "pics.example.com", "", pics, "x.pics.example.com")
	x.mustRead(t, pics, "pics.example.com")
	x.asked(t, "", pics+" "+pics) // shown again is read again: it was in no cache
	if fileExists(x.d.imageHostsPath()) {
		t.Fatal("a show changed the list")
	}
	seven := strings.Split("a.b c.d e.f g.h i.j k.l m.n", " ")
	if e := asError(func() error { _, err := x.read(pics, seven...); return err }()); e == nil || e.Code != CodeInvalidParams {
		t.Fatalf("%v", e)
	}
	for _, h := range []string{"*.example.com", "127.0.0.1", "localhost", "", "pics.example.com:443", "https://pics.example.com"} {
		x.mustFail(t, pics, h)
	}
}

// A redirect to a host on no list stops before it connects, and names both hosts.
func TestARedirectToAnotherHostIsBlockedAgain(t *testing.T) {
	t.Parallel()

	const attachment = "https://github.com/alexec/agents/assets/1/x.png"
	x := newChosenImages(t, imageAnswers{pics: redirectTo(cdn), cdn: imageAnswer("image/png", pngBytes), attachment: redirectTo(cdn)})
	x.mustBlock(t, "cdn.example.net", "pics.example.com", pics, "pics.example.com")
	x.asked(t, "", pics)
	x.mustRead(t, pics, "pics.example.com", "cdn.example.net")
	// Always allow there adds only that host; the first one was only shown.
	x.change(t, "imageHosts/allow", "cdn.example.net")
	x.mustBlock(t, "pics.example.com", "", pics)
	x.mustRead(t, pics, "pics.example.com")
	// Allowed first, not the next.
	x.change(t, "imageHosts/remove", "cdn.example.net")
	x.change(t, "imageHosts/allow", "pics.example.com")
	x.mustBlock(t, "cdn.example.net", "pics.example.com", pics)
	// From a forge's own host.
	x.mustBlock(t, "cdn.example.net", "github.com", attachment)
}

func TestARedirectGoesAtMostFiveTimes(t *testing.T) {
	t.Parallel()

	answers := imageAnswers{}
	hop := func(i int) string { return fmt.Sprintf("https://pics.example.com/%d", i) }
	for i := range 6 {
		answers[hop(i)] = redirectTo(hop(i + 1))
	}
	answers[hop(6)] = imageAnswer("image/png", pngBytes)
	x := newChosenImages(t, answers)
	if msg := x.mustFail(t, hop(0), "pics.example.com"); !strings.Contains(msg, "too many redirects") {
		t.Fatal(msg)
	}
	x.mustRead(t, hop(1), "pics.example.com")
}

// The token, a cookie and the referrer go to no host that the person chose, nor to any host after
// one, also a forge's own.
func TestNoTokenGoesToAChosenHostOrAfterOne(t *testing.T) {
	t.Parallel()

	const attachment = "https://github.com/alexec/agents/assets/1/x.png"
	const raw = "https://raw.githubusercontent.com/a/b/main/x.png"
	const page = "https://github.com/alexec/agents/pages/x.png"
	x := newChosenImages(t, imageAnswers{attachment: redirectTo(page), page: redirectTo(pics), pics: redirectTo(raw), raw: imageAnswer("image/png", pngBytes)})
	// github.com is on the list as a plain host: its other paths go with nothing.
	x.change(t, "imageHosts/allow", "github.com")
	image := x.mustRead(t, attachment, "pics.example.com")
	x.asked(t, attachment+" auth=Bearer ghu_secret "+raw, page+" "+pics)
	for _, h := range append(x.chosen.headers, x.forges.headers[1]) {
		for _, name := range []string{"Authorization", "Cookie", "Referer"} {
			if h.Get(name) != "" {
				t.Fatalf("%s: %q", name, h.Get(name))
			}
		}
	}
	if image.Keep == nil || *image.Keep {
		t.Fatal("kept by the client")
	}
	// A built-in image is as it was: the token, and the client keeps it.
	if image := x.mustRead(t, raw); image.Keep != nil {
		t.Fatal("not kept")
	}
	if last := x.forges.requests()[2]; last != raw+" auth=Bearer ghu_secret" {
		t.Fatal(last)
	}
}

// Remove counts at once, also for an image in the cache and at a redirect.
func TestRemoveTakesEffectAtOnce(t *testing.T) {
	t.Parallel()

	const other = "https://other.example.org/b.png"
	x := newChosenImages(t, imageAnswers{pics: redirectTo(cdn), cdn: imageAnswer("image/png", pngBytes), other: imageAnswer("image/png", pngBytes)})
	for _, h := range []string{"pics.example.com", "cdn.example.net", "other.example.org"} {
		x.change(t, "imageHosts/allow", h)
	}
	for range 2 {
		if image := x.mustRead(t, pics); image.Keep == nil || *image.Keep {
			t.Fatal("kept by the client")
		}
		x.mustRead(t, other)
	}
	x.asked(t, "", pics+" "+cdn+" "+other) // the second time from the cache
	list := x.change(t, "imageHosts/remove", "CDN.example.net")
	if len(list.Hosts) != 2 || list.Hosts[0].Host != "pics.example.com" || list.Hosts[1].Host != "other.example.org" {
		t.Fatalf("%+v", list)
	}
	if n := x.d.forgeImages.order.Len(); n != 1 {
		t.Fatalf("%d images kept", n)
	}
	x.mustBlock(t, "cdn.example.net", "pics.example.com", pics)
	x.mustRead(t, other)

	// A removal by hand counts at once too.
	x.change(t, "imageHosts/allow", "cdn.example.net")
	x.mustRead(t, pics)
	must(t, os.Remove(x.d.imageHostsPath()))
	x.mustBlock(t, "pics.example.com", "", pics)
	x.mustBlock(t, "cdn.example.net", "pics.example.com", pics, "pics.example.com")
}

func TestOnlyThePersonShowsAnImageOrChangesTheHosts(t *testing.T) {
	t.Parallel()

	x := newChosenImages(t, imageAnswers{pics: imageAnswer("image/png", pngBytes)})
	var out json.RawMessage
	for _, role := range []Role{RoleAgent, RoleHostApp, RoleWindow, RoleStranger} {
		c := &Client{Role: role, d: x.d}
		for method, params := range map[string]any{
			"forge/image":       wire.ForgeImageRequest{URL: pics, Show: []string{"pics.example.com"}},
			"imageHosts/list":   wire.Empty{},
			"imageHosts/allow":  wire.ImageHostRequest{Host: "pics.example.com"},
			"imageHosts/remove": wire.ImageHostRequest{Host: "pics.example.com"},
		} {
			if e := asError(callAs(x.d, c, method, params, &out)); e == nil || e.Code != CodeNotPermitted {
				t.Fatalf("%s by %s: %v, wanted notPermitted", method, role, e)
			}
			if role.Allows(method) {
				t.Fatalf("the %s role may call %s", role, method)
			}
		}
	}
	for _, tc := range appToolCalls() {
		if name := strings.ToLower(tc.name); strings.Contains(name, "image_host") || strings.Contains(name, "imagehost") {
			t.Fatalf("an app tool reaches the image hosts: %s", tc.name)
		}
	}
	x.asked(t, "", "")
	if fileExists(x.d.imageHostsPath()) {
		t.Fatal("changed by a refused caller")
	}
	x.person = &Client{Role: RoleDevice, d: x.d}
	x.mustRead(t, pics, "pics.example.com")
}

// The list is empty at first, keeps the order and is idempotent; a host that breaks the rule is
// refused, and an entry past the most is.
func TestTheImageHostsListIsBounded(t *testing.T) {
	t.Parallel()

	x := newChosenImages(t, nil)
	list := func() []wire.ImageHost {
		var l wire.ImageHosts
		must(t, callAs(x.d, x.person, "imageHosts/list", wire.Empty{}, &l))
		return l.Hosts
	}
	if l := list(); l == nil || len(l) != 0 {
		t.Fatalf("%+v", l)
	}
	before := x.change(t, "imageHosts/allow", "bücher.example")
	if again := x.change(t, "imageHosts/allow", "XN--bcher-kva.example"); len(again.Hosts) != 1 || again.Hosts[0] != before.Hosts[0] || again.Hosts[0].Host != "xn--bcher-kva.example" {
		t.Fatalf("%+v", again)
	}
	x.change(t, "imageHosts/remove", "never.example.com")
	var out json.RawMessage
	for _, h := range []string{"", "localhost", "a.local", "10.0.0.1", "::1", "*.example.com", "example.com:443", "u@example.com", "https://example.com",
		"example.com/a", "a..example.com", ".example.com", "example.com.", "com", strings.Repeat("a", 64) + ".com", strings.Repeat("abc.", 80) + "com"} {
		if e := asError(callAs(x.d, x.person, "imageHosts/allow", wire.ImageHostRequest{Host: h}, &out)); e == nil || e.Code != CodeInvalidParams {
			t.Fatalf("%q: %v", h, e)
		}
	}
	// The longest hosts fit, up to the most.
	label := strings.Repeat("a", 61) + "."
	for i := 1; i < imageHostsMost; i++ {
		x.change(t, "imageHosts/allow", fmt.Sprintf("h%03d.%s%s%s%s", i, label, label, label, strings.Repeat("b", 248-3*62)))
	}
	if e := asError(callAs(x.d, x.person, "imageHosts/allow", wire.ImageHostRequest{Host: "one.more.example"}, &out)); e == nil || !strings.Contains(e.Message, "full (100 hosts)") {
		t.Fatalf("%v", e)
	}
	if n := len(list()); n != imageHostsMost {
		t.Fatalf("%d hosts", n)
	}
	b, err := os.ReadFile(x.d.imageHostsPath())
	if err != nil || len(b) > 2*imageHostsMostBytes {
		t.Fatalf("%d bytes, %v", len(b), err)
	}
}

// Each machine has its own entry, a hand edit counts at once, a bad host in it is ignored, and a
// file that cannot be read allows none and is left as it is.
func TestTheImageHostsFile(t *testing.T) {
	t.Parallel()

	x := newChosenImages(t, imageAnswers{pics: imageAnswer("image/png", pngBytes)})
	path := filepath.Join(x.d.SettingsDir, "image-hosts.json")
	writeFile(t, path, `{"OTHER":{"hosts":[{"host":"pics.example.com","addedAt":"2026-11-24T10:00:00.000Z"}],"later":true}}`)
	x.mustBlock(t, "pics.example.com", "", pics)
	x.change(t, "imageHosts/allow", "one.example.com")
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"later": true`) || !strings.Contains(string(b), x.d.Store.RootID) {
		t.Fatalf("%s", b)
	}
	writeFile(t, path, `{"`+x.d.Store.RootID+`":{"hosts":[{"host":"*.example.com","addedAt":"2026-11-24T10:00:00.000Z"},{"host":"127.0.0.1"},{"host":"localhost"},
		{"host":"UPPER.example.com","addedAt":"2026-11-24T10:00:00.000Z"},{"host":"pics.example.com","addedAt":"2026-11-24T10:00:00.000Z"},
		{"host":"pics.example.com","addedAt":"2026-11-25T10:00:00.000Z"}]}}`)
	x.mustRead(t, pics)
	var l wire.ImageHosts
	must(t, callAs(x.d, x.person, "imageHosts/list", wire.Empty{}, &l))
	if len(l.Hosts) != 1 || !l.Hosts[0].AddedAt.Time().Equal(time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", l)
	}
	writeFile(t, path, "half")
	x.mustBlock(t, "pics.example.com", "", pics)
	var out json.RawMessage
	if e := asError(callAs(x.d, x.person, "imageHosts/allow", wire.ImageHostRequest{Host: "two.example.com"}, &out)); e == nil || e.Code != wire.FailureCouldNotSave {
		t.Fatalf("%v", e)
	}
	if b, _ := os.ReadFile(path); string(b) != "half" {
		t.Fatalf("%s", b)
	}

	// One machine more than the file keeps is refused.
	most := map[string]any{}
	for i := range imageHostsMachines {
		most[fmt.Sprint(i)] = map[string]any{}
	}
	b, _ = json.Marshal(most)
	writeFile(t, path, string(b))
	if e := asError(callAs(x.d, x.person, "imageHosts/allow", wire.ImageHostRequest{Host: "two.example.com"}, &out)); e == nil || !strings.Contains(e.Message, "100 machines") {
		t.Fatalf("%v", e)
	}
}

// fakeDNS answers each A question with the next of answers, the last one for ever.
func fakeDNS(t *testing.T, answers ...[4]byte) *net.Resolver {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0") //nolint:noctx // a test's own server
	must(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) != 1 {
				continue
			}
			q := m.Questions[0]
			m.Response, m.Additionals = true, nil
			if q.Type == dnsmessage.TypeA {
				m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: q.Class},
					Body: &dnsmessage.AResource{A: answers[0]}}}
				if len(answers) > 1 {
					answers = answers[1:]
				}
			}
			if b, err := m.Pack(); err == nil {
				_, _ = pc.WriteTo(b, from) // the resolver asks again
			}
		}
	}()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
}

// An allowed host whose name is a private address is refused, at the first read and at a redirect.
func TestAChosenHostMustBePublic(t *testing.T) {
	t.Parallel()

	const intranet = "https://intranet.example.com/a.png"
	for _, addr := range [][4]byte{{127, 0, 0, 1}, {10, 0, 0, 1}, {192, 168, 1, 1}, {169, 254, 169, 254}, {100, 100, 100, 200}} {
		x := newChosenImages(t, imageAnswers{pics: redirectTo(intranet)})
		private := newPublicImageTransport(&net.Dialer{Resolver: fakeDNS(t, addr), Control: imageGuard.Safe})
		fake := x.chosen
		x.d.publicImageTransport = roundTrip(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "pics.example.com" {
				return fake.RoundTrip(req)
			}
			return private.RoundTrip(req)
		})
		for _, u := range []string{intranet, pics} {
			_, err := x.read(u, "pics.example.com", "intranet.example.com")
			if e := asError(err); e == nil || e.Code != wire.FailureImageNotPublic || !strings.Contains(e.Message, "intranet.example.com is on a private network") {
				t.Fatalf("%v, %s: %v", addr, u, err)
			}
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// A name that resolves again is checked again: each connection is, after the name resolved.
func TestANameThatResolvesAgainIsCheckedAgain(t *testing.T) {
	t.Parallel()

	stop := errors.New("not dialed by a test")
	var mu sync.Mutex
	var verdicts []string
	transport := newPublicImageTransport(&net.Dialer{Resolver: fakeDNS(t, [4]byte{1, 1, 1, 1}, [4]byte{169, 254, 169, 254}),
		Control: func(network, address string, c syscall.RawConn) error {
			err := imageGuard.Safe(network, address, c)
			mu.Lock()
			verdicts = append(verdicts, fmt.Sprintf("%s %v", address, errors.Is(err, ssrf.ErrProhibitedIP)))
			mu.Unlock()
			if err == nil {
				err = stop // it was public; a test connects to nothing
			}
			return err
		}})
	x := newChosenImages(t, nil)
	x.d.publicImageTransport = transport
	x.change(t, "imageHosts/allow", "pics.example.com")
	x.mustFail(t, pics)
	if msg := x.mustFail(t, pics); !strings.Contains(msg, "private network") {
		t.Fatal(msg)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(verdicts, ", "); got != "1.1.1.1:443 false, 169.254.169.254:443 true" {
		t.Fatal(got)
	}
}

// The guard, by address: every form that is not public, and Azure's metadata address, which is.
func TestTheImageGuard(t *testing.T) {
	t.Parallel()

	for _, a := range []string{"127.0.0.1:443", "0.0.0.0:443", "10.1.2.3:443", "172.16.0.1:443", "192.168.0.1:443", "169.254.169.254:443",
		"100.64.0.1:443", "224.0.0.1:443", "255.255.255.255:443", "168.63.129.16:443", "8.8.8.8:80",
		"[::]:443", "[::1]:443", "[fe80::1%en0]:443", "[fc00::1]:443", "[fd00:ec2::254]:443", "[ff02::1]:443",
		"[::ffff:127.0.0.1]:443", "[::ffff:8.8.8.8]:443", "[64:ff9b::7f00:1]:443", "[2002:7f00:1::]:443"} {
		network := "tcp4"
		if strings.HasPrefix(a, "[") {
			network = "tcp6"
		}
		if imageGuard.Safe(network, a, nil) == nil {
			t.Errorf("%s is dialed", a)
		}
	}
	for network, a := range map[string]string{"tcp4": "8.8.8.8:443", "tcp6": "[2606:4700:4700::1111]:443"} {
		if err := imageGuard.Safe(network, a, nil); err != nil {
			t.Error(err)
		}
	}
	// No proxy resolves the name in the guard's place, and the reads of one host are bounded.
	tr := publicImageTransport.(*http.Transport)
	if tr.Proxy != nil || tr.MaxConnsPerHost != imageHostConnections || tr.ForceAttemptHTTP2 {
		t.Fatalf("%+v", tr)
	}
}
