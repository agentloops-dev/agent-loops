package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

func TestTheDevicesAreNamed(t *testing.T) {
	if d, ok := DeviceNamed(" Desktop "); !ok || d != nil {
		t.Fatalf("desktop: %v %v", d, ok)
	}
	if d, ok := DeviceNamed("phone"); !ok || d.Width != 412 || !d.Mobile || !d.Touch {
		t.Fatalf("phone: %+v %v", d, ok)
	}
	if d, ok := DeviceNamed("tablet"); !ok || d.Width != 640 {
		t.Fatalf("tablet: %+v %v", d, ok)
	}
	if _, ok := DeviceNamed("watch"); ok {
		t.Fatal("a watch is not a device")
	}
}

// The user agent is the browser's own Chrome on the device, and the client hints keep the
// browser's brands, with the device's platform: they agree, as #4060's user agent does.
func TestADevicesUserAgentKeepsTheBrowsersChrome(t *testing.T) {
	brands := []*emulation.UserAgentBrandVersion{{Brand: "Chromium", Version: "141"}, {Brand: "Not?A_Brand", Version: "8"}}
	own := uaHints{Agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
		Brands: brands, FullVersionList: brands}
	phone, _ := DeviceNamed("phone")
	p, err := phone.userAgent(own)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserAgent != "Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36" {
		t.Fatal(p.UserAgent)
	}
	m := p.UserAgentMetadata
	if m == nil || m.Platform != "Android" || !m.Mobile || m.Model != "Pixel 7" || len(m.Brands) != 2 || m.Brands[0].Version != "141" {
		t.Fatalf("%+v", m)
	}
	tablet, _ := DeviceNamed("tablet")
	if p, _ := tablet.userAgent(own); p.UserAgentMetadata.Mobile || strings.Contains(p.UserAgent, "Mobile") {
		t.Fatalf("a tablet is not mobile: %+v", p)
	}
	// With no brands read (a page that is not secure), no hints rather than wrong ones.
	if p, _ := phone.userAgent(uaHints{Agent: own.Agent}); p.UserAgentMetadata != nil {
		t.Fatal("hints with no brands")
	}
	if _, err := phone.userAgent(uaHints{Agent: "Mozilla/5.0"}); err == nil {
		t.Fatal("a user agent with no Chrome")
	}
}

// TestADeviceOnARealChromium runs only with AGENTS_TEST_CHROMIUM naming a Chromium. A tab
// takes the phone's profile, and another session on it (Playwright's, the cast's) sees it;
// a cast that ends leaves it; desktop takes it away.
func TestADeviceOnARealChromium(t *testing.T) {
	exe := os.Getenv("AGENTS_TEST_CHROMIUM")
	if exe == "" {
		t.Skip("AGENTS_TEST_CHROMIUM names no Chromium")
	}
	p, err := launchChromium(exe, t.TempDir(), append(os.Environ(), "HOME="+t.TempDir()), mode{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	tabs, err := FollowTabs(p.endpoint, func(target.ID, target.ID) {}, func(target.ID) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tabs.Close()
	site, hints := servePhonePage(t)
	tab, err := tabs.Open(site.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Another client of the browser, as @playwright/mcp is.
	alloc, cancelAlloc := remote.NewAllocator(context.Background(), p.endpoint, remote.NoModifyURL)
	defer cancelAlloc()
	root, cancelRoot := chromedp.NewContext(alloc)
	defer cancelRoot()
	if _, err := chromedp.CallBrowser(root, target.GetTargets, target.GetTargetsParams{}); err != nil {
		t.Fatal(err)
	}
	other, cancelOther := attachToTab(t, root, tab)
	defer cancelOther()
	look := func() seenOnPage {
		t.Helper()
		return lookAtThePage(t, other)
	}
	before := look()
	if strings.Contains(before.Agent, "Android") || before.Width == 412 {
		t.Fatalf("emulated before: %+v", before)
	}

	phone, _ := DeviceNamed("phone")
	if err := tabs.Emulate(tab, phone); err != nil {
		t.Fatal(err)
	}
	checkItIsThePhone(t, look(), before)

	castAsThePhone(t, p.endpoint, tab, phone, site.URL, hints, look)

	// Playwright's session ends with the agent's turn: the phone stays.
	cancelOther()
	other, cancelOther = attachToTab(t, root, tab)
	defer cancelOther()
	if s := look(); s.Width != 412 {
		t.Fatalf("Playwright's end took the phone away: %+v", s)
	}

	castGivesUpItsSize(t, p.endpoint, tabs, tab, phone, look)
}

// seenOnPage is what a page says of the device it is laid out on.
type seenOnPage struct {
	Width, Touch int64
	Ratio        float64
	Agent        string
	Mobile       bool
	Platform     string
}

// servePhonePage serves a page made for phones (with no viewport meta, a phone lays a page
// out 980 wide), on 127.0.0.1, a secure origin, so the page reads client hints and is sent
// them. Each request's headers go to the channel.
func servePhonePage(t *testing.T) (*httptest.Server, chan http.Header) {
	t.Helper()
	hints := make(chan http.Header, 8)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hints <- r.Header.Clone():
		default:
		}
		w.Header().Set("Content-Type", "text/html")
		reply(t, w, `<meta name=viewport content="width=device-width"><p>a page`)
	}))
	t.Cleanup(site.Close)
	return site, hints
}

// attachToTab opens a session of its own on the tab, which detaches when it is cancelled.
func attachToTab(t *testing.T, root context.Context, tab target.ID) (context.Context, context.CancelFunc) {
	t.Helper()
	other, cancel := chromedp.NewContext(root, chromedp.WithTargetID(tab), chromedp.WithDetachOnCancel())
	if err := chromedp.Do(other); err != nil {
		cancel()
		t.Fatal(err)
	}
	return other, cancel
}

// lookAtThePage reads what the page in the session says of its device.
func lookAtThePage(t *testing.T, other context.Context) seenOnPage {
	t.Helper()
	ctx, cancel := context.WithTimeout(other, 10*time.Second)
	defer cancel()
	s, err := chromedp.Run(ctx, chromedp.Evaluate[seenOnPage](`({width: innerWidth, touch: navigator.maxTouchPoints, ratio: devicePixelRatio,
			agent: navigator.userAgent, mobile: !!(navigator.userAgentData && navigator.userAgentData.mobile),
			platform: navigator.userAgentData ? navigator.userAgentData.platform : ""})`))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// checkItIsThePhone checks that the page is laid out as the phone, with the browser's own
// Chrome in its user agent and hints that agree.
func checkItIsThePhone(t *testing.T, s, before seenOnPage) {
	t.Helper()
	if s.Width != 412 || s.Ratio != 2.625 || s.Touch == 0 || !strings.Contains(s.Agent, "Android 14; Pixel 7") ||
		strings.Contains(s.Agent, "Headless") {
		t.Fatalf("not a phone: %+v", s)
	}
	if v := chromeVersion.FindString(before.Agent); v == "" || !strings.Contains(s.Agent, v) {
		t.Fatalf("not the browser's own Chrome: %q, %q", s.Agent, before.Agent)
	}
	if before.Platform == "" || !s.Mobile || s.Platform != "Android" {
		t.Fatalf("the hints do not agree: %+v, before %+v", s, before)
	}
}

// castAsThePhone checks that a cast of the session lays the page out as the phone, and
// leaves it so when it ends.
func castAsThePhone(t *testing.T, endpoint string, tab target.ID, phone *Device, url string, hints chan http.Header, look func() seenOnPage) {
	t.Helper()
	frames := make(chan Frame, 4)
	cast, err := Watch(endpoint, tab, 800, 600, phone, func(f Frame) {
		select {
		case frames <- f:
		default:
		}
	}, func(Page) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := look(); s.Width != 412 {
		t.Fatalf("the cast laid it out at %d", s.Width)
	}
	for len(hints) > 0 {
		<-hints
	}
	if err := cast.Navigate("go", url+"/again"); err != nil {
		t.Fatal(err)
	}
	checkThePhoneRequest(t, hints)
	select {
	case f := <-frames:
		if f.Width != 412 {
			t.Fatalf("a frame %v wide", f.Width)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no frame")
	}
	cast.Close()
	time.Sleep(500 * time.Millisecond)
	if s := look(); s.Width != 412 || !strings.Contains(s.Agent, "Pixel 7") {
		t.Fatalf("the cast's end took the phone away: %+v", s)
	}
}

// checkThePhoneRequest checks that the next request says it is from the phone.
func checkThePhoneRequest(t *testing.T, hints <-chan http.Header) {
	t.Helper()
	select {
	case h := <-hints:
		if !strings.Contains(h.Get("User-Agent"), "Pixel 7") || h.Get("Sec-CH-UA-Mobile") != "?1" ||
			h.Get("Sec-CH-UA-Platform") != `"Android"` || h.Get("Sec-CH-UA") == "" {
			t.Fatalf("the request does not agree: %v", h)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no request")
	}
}

// castGivesUpItsSize checks that a cast that watched at the pane's size gives it up for a
// device the session takes, and takes it back when the session is a desktop again.
func castGivesUpItsSize(t *testing.T, endpoint string, tabs *Tabs, tab target.ID, phone *Device, look func() seenOnPage) {
	t.Helper()
	if err := tabs.Emulate(tab, nil); err != nil {
		t.Fatal(err)
	}
	cast, err := Watch(endpoint, tab, 800, 600, nil, func(Frame) {}, func(Page) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cast.Close()
	if s := look(); s.Width != 800 || strings.Contains(s.Agent, "Android") {
		t.Fatalf("still a phone: %+v", s)
	}
	if err := cast.SetDevice(phone); err != nil {
		t.Fatal(err)
	}
	if err := tabs.Emulate(tab, phone); err != nil {
		t.Fatal(err)
	}
	if s := look(); s.Width != 412 {
		t.Fatalf("the cast kept its size: %+v", s)
	}
	if err := tabs.Emulate(tab, nil); err != nil {
		t.Fatal(err)
	}
	if err := cast.SetDevice(nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if s := look(); s.Width != 800 || strings.Contains(s.Agent, "Android") {
		t.Fatalf("not the pane's size again: %+v", s)
	}
}
