package main

// The link to the control plane dials again when it drops (#6478, follow-up 1 of #6473). A
// blip used to end a long loops -p with exit 1 while the turn went on at the host, and the
// live chat with it. Now the chat and print mode dial again with backoff for up to
// --timeout (or relinkFor), then read the transcript from the last index they read, so no
// entry is lost or shown twice. A link that stays lost ends the command with exit 6.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// relinkFor is how long a dropped link is dialled again with no --timeout.
var relinkFor = time.Minute

// The backoff between dials: the first, doubled each time, to at most the last.
var (
	relinkFirst = 250 * time.Millisecond
	relinkMost  = 5 * time.Second
)

// relinker is a link that can dial again. The fakes of the tests may not be one: then a
// lost link ends the command at once, with exit 6.
type relinker interface {
	// relink dials again until it is back, for up to wait; say is told once, before the
	// first dial.
	relink(ctx context.Context, wait time.Duration, say func()) error
	// notes is the notes of the socket now: after a relink, a new channel.
	notes() <-chan controllink.Note
}

// link is the paired control plane's socket, dialled again when it drops.
type link struct {
	mu sync.Mutex
	s  *controllink.Session
	o  options
	// redial is dial, which a test replaces.
	redial func(ctx context.Context, o *options) (*controllink.Session, error)
}

func newLink(s *controllink.Session, o options) *link {
	return &link{s: s, o: o, redial: dialLink}
}

func (l *link) session() *controllink.Session {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s
}

func (l *link) Call(ctx context.Context, host, method string, params any) (json.RawMessage, error) {
	return l.session().Call(ctx, host, method, params)
}

func (l *link) notes() <-chan controllink.Note { return l.session().Notes }

func (l *link) Close() {
	if s := l.session(); s != nil {
		s.Close()
	}
}

func (l *link) relink(ctx context.Context, wait time.Duration, say func()) error {
	l.Close()
	say()
	dctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	pause := relinkFirst
	var last error
	for {
		// The session's read lives as long as ctx, not the dial's deadline.
		s, err := l.redial(ctx, &l.o)
		if err == nil {
			l.mu.Lock()
			l.s = s
			l.mu.Unlock()
			return nil
		}
		last = err
		if dctx.Err() != nil {
			return last
		}
		select {
		case <-time.After(pause):
		case <-dctx.Done():
			return last
		}
		pause = min(pause*2, relinkMost)
	}
}

// lost says err is the link that dropped, not a host that refused.
func lost(err error) bool { return errors.Is(err, controllink.ErrLinkLost) }

// hostOffline says err is the control plane's "That machine is offline": the host's own
// link is down, as it is for a while after the control plane starts again, before the
// host has joined again (#6478).
func hostOffline(err error) bool {
	if err == nil {
		return false
	}
	var e struct {
		Code int `json:"code"`
	}
	return json.Unmarshal([]byte(err.Error()), &e) == nil && e.Code == wire.FailureHostOffline
}

// waitHost reports what this terminal shows until the session's host answers, with the
// backoff, while ctx lasts: its entries come here again. It says the last error when ctx
// ends first.
func waitHost(ctx context.Context, c caller, host string, report wire.PresenceReport) error {
	pause := relinkFirst
	for {
		_, err := c.Call(ctx, host, "presence/report", report)
		if !hostOffline(err) {
			return err
		}
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return err
		}
		pause = min(pause*2, relinkMost)
	}
}

// lostForGood is exit 6: the link stayed lost for wait.
func lostForGood(control string, wait time.Duration, id string, err error) error {
	if control == "" {
		control = "the control plane"
	}
	why := ""
	if err != nil {
		why = fmt.Sprintf(" (%v)", err)
	}
	msg := fmt.Sprintf("Lost the link to %s and could not get it back in %s%s.", control, wait, why)
	if id != "" {
		msg += "\nThe session goes on: loops -r " + id
	}
	return &exit{code: exitLinkLost, msg: msg}
}
