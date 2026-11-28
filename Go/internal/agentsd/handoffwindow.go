package agentsd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The hand-off in the real window (#5208, spike #5127). When the agents' browser is Google
// Chrome (#5207), its window is real, just below the screen. hand_to_person then gives the
// person that window, not the screencast: the "screen" lease is taken, the window with the
// agent's tab comes onto the screen and Chrome to the front, and the card says so. The
// person does the step there: a press-and-hold check, a Touch ID or iCloud Keychain passkey
// prompt, a CAPTCHA, none of which the screencast carries. When the card comes down (Done,
// Can't do it, or the hand-off ends any other way), the window goes back below the screen,
// the app that was in front before comes back, and the lease is let go.
//
// Chrome for Testing on a Mac has the same window (#7259). Take over (Open on a live tile)
// on the Mac that runs the browser gives the person that window the same way, by
// browser/window, until the take-over window closes or the client's connection ends.
//
// When it cannot (a headless Chrome for Testing, another host than a Mac, no agents-drive,
// the screen held by another agent), the card is as before: Open the page, on the
// screencast; and a take over shows the screencast.

// handOffWindowState is the Daemon's hand-offs in the real window, by the card's request id,
// and the take overs in it, by client and session.
type handOffWindowState struct {
	windowMu  sync.Mutex
	windows   map[wire.UUID]windowHandOff
	takeOvers map[int64]map[wire.UUID]wire.UUID
}

// windowHandOff is one window on the screen for the person, and what puts things back.
type windowHandOff struct {
	agent wire.UUID
	tab   string
	// chrome is Google Chrome's pid; previous is the app that was in front before it.
	chrome, previous int
	// leased is whether the hand-off took the "screen" lease, so it lets it go.
	leased bool
}

// windowWaitsLongest is the most moving the window and bringing an app to the front may take.
const windowWaitsLongest = 5 * time.Second

// showBrowserWindow and hideBrowserWindow move the agents' browser window onto the screen
// and back below it; a test gives its own.
var (
	showBrowserWindow = func(d *Daemon, ctx context.Context, tab string) (int, error) { return d.browser.ShowWindow(ctx, tab) }
	hideBrowserWindow = func(d *Daemon, ctx context.Context, tab string) error { return d.browser.HideWindow(ctx, tab) }
)

// frontApp brings the app with this pid to the front, with agents-drive's "front", and says
// the pid that was in front before. A test gives its own.
var frontApp = func(pid int) (int, error) {
	cmd := exec.Command(driveHelper())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	defer func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }() // the answer was read already; how it exits says nothing more
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill() // it may have ended already
		}
	}()
	frame, _ := json.Marshal(map[string]any{"action": "front", "pid": pid})
	if _, err := stdin.Write(append(frame, 0)); err != nil {
		return 0, err
	}
	type answer struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Previous int    `json:"previous"`
	}
	got := make(chan answer, 1)
	go func() {
		var a answer
		if b, err := bufio.NewReader(stdout).ReadBytes(0); err == nil {
			if err := json.Unmarshal(b[:len(b)-1], &a); err != nil {
				a.Error = "agents-drive's answer could not be read: " + err.Error()
			}
		} else {
			a.Error = "agents-drive said nothing"
		}
		got <- a
	}()
	select {
	case a := <-got:
		if !a.OK {
			return a.Previous, errors.New(a.Error)
		}
		return a.Previous, nil
	case <-time.After(windowWaitsLongest):
		_ = cmd.Process.Kill() // it may have ended already
		return 0, fmt.Errorf("agents-drive did not answer in %s", windowWaitsLongest)
	}
}

// screenNowArgs is the "screen" lease, now or not at all: a hand-off does not wait in line.
var screenNowArgs = map[string]json.RawMessage{"name": json.RawMessage(`"screen"`), "wait": json.RawMessage(`false`)}

// showHandOffWindow gives the person the real window for the hand-off request, and says the
// window as the card names it; false when the card stays on the screencast.
func (d *Daemon) showHandOffWindow(agent, request wire.UUID) (string, bool) {
	window, _, ok := d.showRealWindow(agent, request, true)
	return window, ok
}

// showRealWindow puts the window with agent's tab on the screen for request, and says the
// window as the card names it and the browser's pid. mustFront: the browser must come to the
// front by agents-drive, or the window goes back. A take over does not need it: macOS lets
// only the app in front give the front away, so the Mac app, which the person just used,
// brings the browser to the front itself.
func (d *Daemon) showRealWindow(agent, request wire.UUID, mustFront bool) (string, int, bool) {
	if d.browser == nil || !d.desktopControlAvailable() {
		return "", 0, false
	}
	held := d.holdsScreen(agent)
	if !held {
		if _, err := d.leaseResource(agent, screenNowArgs); err != nil || !d.holdsScreen(agent) {
			// Another agent has the screen: the person gets the screencast.
			return "", 0, false
		}
	}
	letGo := func() {
		if !held && d.holdsScreen(agent) {
			if _, err := d.releaseResource(agent, screenLeaseName); err != nil {
				log.Printf("browser: could not release the screen: %v", err)
			}
		}
	}
	tab := string(d.sessionTab(agent))
	ctx, cancel := context.WithTimeout(context.Background(), windowWaitsLongest)
	defer cancel()
	chrome, err := showBrowserWindow(d, ctx, tab)
	if err != nil {
		letGo()
		return "", 0, false
	}
	previous, err := frontApp(chrome)
	if err != nil && !mustFront {
		log.Printf("browser: the taken-over window is on the screen; agents-drive did not bring it to the front, so the client does: %v", err)
		previous = 0
	} else if err != nil {
		log.Printf("browser: the hand-off's window is on the screen, but Chrome did not come to the front: %v", err)
		if err := hideBrowserWindow(d, ctx, tab); err != nil {
			log.Printf("browser: couldn't put the window back below the screen: %v", err)
		}
		letGo()
		return "", 0, false
	}
	d.windowMu.Lock()
	if d.windows == nil {
		d.windows = map[wire.UUID]windowHandOff{}
	}
	d.windows[request] = windowHandOff{agent: agent, tab: tab, chrome: chrome, previous: previous, leased: !held}
	d.windowMu.Unlock()
	log.Printf("browser: the hand-off's window is in front (Chrome pid %d, before it pid %d)", chrome, previous)
	return realWindowWords(d.sessionTabURL(agent)), chrome, true
}

// realWindowWords is the window as the card names it: "the agents' Chrome window, in front
// (accounts.google.com)".
func realWindowWords(page string) string {
	said := "the agents' Chrome window, in front"
	if u, err := url.Parse(page); err == nil && u.Hostname() != "" {
		said += " (" + u.Hostname() + ")"
	}
	return said
}

// hideHandOffWindow is the card for request come down: its window goes back below the
// screen, the app that was in front before comes back, and the "screen" lease is let go.
// It does nothing for a card that had no window.
func (d *Daemon) hideHandOffWindow(request wire.UUID) {
	d.windowMu.Lock()
	w, ok := d.windows[request]
	delete(d.windows, request)
	d.windowMu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), windowWaitsLongest)
	defer cancel()
	if err := hideBrowserWindow(d, ctx, w.tab); err != nil {
		log.Printf("browser: couldn't put the hand-off's window back below the screen: %v", err)
	}
	if w.previous > 0 && w.previous != w.chrome {
		if _, err := frontApp(w.previous); err != nil {
			log.Printf("browser: the app that was in front before the hand-off (pid %d) did not come back: %v", w.previous, err)
		}
	}
	if w.leased && d.holdsScreen(w.agent) {
		if _, err := d.releaseResource(w.agent, screenLeaseName); err != nil {
			log.Printf("browser: could not release the screen: %v", err)
		}
	}
}

// windowHandedOff is whether the person has one of agent's windows: its "screen" lease
// stays past its turn's end, as browser:agents does, until the card comes down.
func (d *Daemon) windowHandedOff(agent wire.UUID) bool {
	d.windowMu.Lock()
	defer d.windowMu.Unlock()
	for _, w := range d.windows {
		if w.agent == agent && w.leased {
			return true
		}
	}
	return false
}

// takeOverWindow is browser/window (#7259): show gives the person at this Mac the window with
// agent's tab, as a hand-off does; a second show for the same client and session brings it to
// the front again. show false puts it back.
func (d *Daemon) takeOverWindow(c *Client, agent wire.UUID, show bool) (wire.BrowserWindowResult, error) {
	if c == nil || (c.Role != RoleControl && c.Role != RoleDevice) {
		return wire.BrowserWindowResult{}, &Error{Code: CodeNotPermitted, Message: "Only your own clients can take over the browser's window."}
	}
	d.windowMu.Lock()
	request, had := d.takeOvers[c.ID][agent]
	if !show && had {
		delete(d.takeOvers[c.ID], agent)
	}
	d.windowMu.Unlock()
	if !show {
		if had {
			d.hideHandOffWindow(request)
		}
		return wire.BrowserWindowResult{}, nil
	}
	if had {
		d.hideHandOffWindow(request)
	}
	request = store.NewUUID()
	window, pid, ok := d.showRealWindow(agent, request, false)
	if !ok {
		d.windowMu.Lock()
		delete(d.takeOvers[c.ID], agent)
		d.windowMu.Unlock()
		return wire.BrowserWindowResult{}, nil
	}
	d.windowMu.Lock()
	if d.takeOvers == nil {
		d.takeOvers = map[int64]map[wire.UUID]wire.UUID{}
	}
	if d.takeOvers[c.ID] == nil {
		d.takeOvers[c.ID] = map[wire.UUID]wire.UUID{}
	}
	d.takeOvers[c.ID][agent] = request
	d.windowMu.Unlock()
	browserPid := int64(pid)
	return wire.BrowserWindowResult{Shown: true, Window: &window, Pid: &browserPid}, nil
}

// endTakeOvers puts back every window c took over: its connection ended.
func (d *Daemon) endTakeOvers(c *Client) {
	d.windowMu.Lock()
	requests := d.takeOvers[c.ID]
	delete(d.takeOvers, c.ID)
	d.windowMu.Unlock()
	if len(requests) == 0 {
		return
	}
	// Putting a window back waits on the browser and agents-drive, so off the caller's path.
	d.goJobOrNow("put back a taken-over window", func() {
		for _, request := range requests {
			d.hideHandOffWindow(request)
		}
	})
}

func (d *Daemon) registerBrowserWindow() {
	On(d, wire.CallBrowserWindow, func(_ context.Context, c *Client, r wire.BrowserWindowRequest) (wire.BrowserWindowResult, error) {
		return d.takeOverWindow(c, sessionKey(r.Agent), r.Show)
	})
}
