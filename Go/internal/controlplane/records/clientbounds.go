package records

import (
	"context"
	"fmt"
	"strings"
)

// The clients' bounds (#6757, epic #6669). They are a person's data, so a write over a
// limit is refused, and nothing is evicted to make room. A write that keeps a paired
// client in use (its key renewed, its last sight, its epoch) is never refused: it would
// end its pairing.
const (
	// ClientMostBytes is the largest client record.
	ClientMostBytes = 16 << 10
	// ClientsMostBytes is the most an organisation's clients come to (#6669). Tombstones,
	// gone in a week, may come to as much again: a forget makes room at once.
	ClientsMostBytes = 1 << 20
)

// ClientLimitError is a write refused at a limit, in the words a person reads.
type ClientLimitError struct{ Words string }

func (e *ClientLimitError) Error() string { return e.Words }

// ClientRoom refuses a new client when the clients are at their limit. A client paired
// already has its room.
func (r *Records) ClientRoom(ctx context.Context, id string) error {
	if r.Client(strings.ToUpper(id)) != nil {
		return nil
	}
	return r.clientsWithin(ctx, 0)
}

// clientsWithin refuses when the clients and grow more bytes are over ClientsMostBytes.
func (r *Records) clientsWithin(ctx context.Context, grow int) error {
	listed, err := r.Store.List(ctx, ClientsPrefix)
	if err != nil {
		return err
	}
	live, all := int64(grow), int64(grow)
	r.mu.Lock()
	for _, k := range listed {
		size := k.Size
		if size < 0 {
			size = ClientMostBytes
		}
		all += size
		if r.tombstones[k.Key] != k.ETag {
			live += size
		}
	}
	r.mu.Unlock()
	if live >= ClientsMostBytes {
		return &ClientLimitError{"This control plane keeps as many clients as it can. Forget one that is not used, then try again."}
	}
	if all >= 2*ClientsMostBytes {
		return &ClientLimitError{"Too many clients were forgotten in the last week. Try again in a few days."}
	}
	return nil
}

// newClientWithin refuses a new client's record over either limit.
func (r *Records) newClientWithin(ctx context.Context, c ClientRecord) error {
	size, err := encodedSize(c)
	if err != nil {
		return err
	}
	if size > ClientMostBytes {
		return tooLarge(size)
	}
	return r.clientsWithin(ctx, size)
}

// changeWithin refuses a change that grows what a client says of itself, its name and its
// push tokens, past ClientMostBytes or while the clients are at their limit.
func (r *Records) changeWithin(ctx context.Context, before, after ClientRecord) error {
	was, err := encodedSize(before)
	if err != nil {
		return err
	}
	size, err := encodedSize(after)
	if err != nil {
		return err
	}
	if size <= was {
		return nil
	}
	grew, err := saidGrew(before, after)
	if err != nil {
		return err
	}
	if !grew {
		return nil
	}
	if size > ClientMostBytes {
		return tooLarge(size)
	}
	return r.clientsWithin(ctx, size-was)
}

// saidGrew says whether a client's name and push tokens came to more.
func saidGrew(before, after ClientRecord) (bool, error) {
	said := func(c ClientRecord) (int, error) {
		return encodedSize(ClientRecord{Name: c.Name, PushToken: c.PushToken, ActivityTokens: c.ActivityTokens, WebPush: c.WebPush})
	}
	was, err := said(before)
	if err != nil {
		return false, err
	}
	is, err := said(after)
	return is > was, err
}

func encodedSize(c ClientRecord) (int, error) {
	data, err := Encode(c)
	return len(data), err
}

func tooLarge(size int) error {
	return &ClientLimitError{fmt.Sprintf("What this client says of itself is too large: %d bytes, and the most is %d.", size, ClientMostBytes)}
}

// ForgetClientIf forgets a client that still passes still, read again if another copy
// wrote it first: one renewed there meanwhile keeps its pairing. It says whether it went.
func (r *Records) ForgetClientIf(ctx context.Context, id string, still func(ClientRecord) bool) (bool, error) {
	return r.forgetClient(ctx, id, still)
}
