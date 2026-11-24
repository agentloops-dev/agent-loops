package controlplane

import (
	"context"
	"errors"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The channel links' bounds (#6763, epic #6669). A person approved each link, so a new one
// over a limit is refused and none is evicted. One in use never goes; one not used for
// linkUnusedKept goes in the hourly sweep (Alex's decision), and its identity's next
// message asks the person again.
const (
	// linkMostBytes is the largest link.
	linkMostBytes = 4 << 10
	// linksMostBytes is the most an organisation's links come to (#6669).
	linksMostBytes = 1 << 20
	// linkUnusedKept is how long a link stays after its identity last acted.
	linkUnusedKept = 90 * 24 * time.Hour
	// linkUsedWithin is how near UsedAt is kept to the last use: one write a day, not one a
	// message.
	linkUsedWithin = 24 * time.Hour
)

// room refuses a link of size bytes at key when it is over either limit. The link it
// replaces, if any, makes room.
func (l *links) room(ctx context.Context, key string, size int) error {
	if size > linkMostBytes {
		return fail(notPermitted, "What is kept of this link is too large: %d bytes, and the most is %d.", size, linkMostBytes)
	}
	listed, err := l.s.cfg.Store.List(ctx, linksPrefix)
	if err != nil {
		return fail(storeUnavailable, "The channel links could not be read: %v", err)
	}
	total := int64(size)
	for _, k := range listed {
		switch {
		case k.Key == key:
		case k.Size < 0:
			total += linkMostBytes
		default:
			total += k.Size
		}
	}
	if total > linksMostBytes {
		return fail(notPermitted, "This control plane keeps as many channel links as it can. Unlink one that is not used, then try again.")
	}
	return nil
}

// linked is the link of a plain sender to a person who is not removed, or nil. The link is
// now in use.
func (l *links) linked(ctx context.Context, sender wire.ChannelSender) (*linkRecord, error) {
	link, etag, err := l.read(ctx, sender.Channel, sender.Identity)
	if err != nil {
		return nil, fail(storeUnavailable, "The channel links could not be read: %v", err)
	}
	if link == nil || !l.s.isPerson(link.User) {
		return nil, nil
	}
	if link.UsedAt == nil || l.now().Sub(link.UsedAt.Time) >= linkUsedWithin {
		// The message goes on without it: the next message notes it, and so did whoever
		// wrote first.
		if err := l.noteUsed(ctx, *link, etag, l.now()); err != nil && !store.IsConflict(err) {
			l.s.log("a " + link.Channel + " link's use was not noted: " + err.Error())
		}
	}
	return link, nil
}

// noteUsed dates a link's last use, against the version read. It is never refused for room.
func (l *links) noteUsed(ctx context.Context, link linkRecord, etag string, at time.Time) error {
	now := records.At(at)
	link.UsedAt = &now
	return l.write(ctx, link, etag)
}

func (l *links) write(ctx context.Context, link linkRecord, etag string) error {
	data, err := records.Encode(link)
	if err != nil {
		return err
	}
	_, err = l.s.cfg.Store.Put(ctx, linkKey(link.Channel, link.Identity), data, store.Matching(etag))
	return err
}

// expire removes, at most most, the links not used for linkUnusedKept, and dates those
// with no last use from now.
func (l *links) expire(ctx context.Context, now time.Time, most int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	every, err := l.every(ctx)
	if err != nil {
		return err
	}
	var failed []error
	gone := 0
	for _, r := range every {
		if r.UsedAt != nil && now.Sub(r.UsedAt.Time) <= linkUnusedKept {
			continue
		}
		if gone >= most {
			break
		}
		went, err := l.expireOne(ctx, r, now)
		// A link another copy wrote first is in use, or gone.
		if err != nil && !store.IsConflict(err) {
			failed = append(failed, err)
		}
		if went {
			gone++
		}
	}
	return errors.Join(failed...)
}

// expireOne dates or removes one link, read again, and says whether it went. There is no
// conditional delete, so it is first written as nobody's against the version read: a use
// noted meanwhile keeps it.
func (l *links) expireOne(ctx context.Context, listed linkRecord, now time.Time) (bool, error) {
	link, etag, err := l.read(ctx, listed.Channel, listed.Identity)
	if err != nil || link == nil {
		return false, err
	}
	if link.UsedAt == nil {
		return false, l.noteUsed(ctx, *link, etag, now)
	}
	if now.Sub(link.UsedAt.Time) <= linkUnusedKept {
		return false, nil
	}
	user := link.User
	link.User = ""
	if err := l.write(ctx, *link, etag); err != nil {
		return false, err
	}
	// One left here is nobody's, and the next sweep deletes it.
	if err := l.s.cfg.Store.Delete(ctx, linkKey(link.Channel, link.Identity)); err != nil {
		return false, err
	}
	l.changed(user)
	l.s.log("a " + link.Channel + " identity was unlinked from " + user + ": not used for 90 days")
	return true, nil
}
