package controlplane

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Conn is WebSocketLineTransport: a WebSocket carrying one line per text frame. Writes
// queue and never block the router: a peer that falls this far behind is closed, and
// reconnects to read everything afresh, as the Swift transport's bound does.
type Conn struct {
	ws  *websocket.Conn
	out chan string
	// bulk lines go when out is empty (#7120).
	bulk   chan string
	done   chan struct{}
	once   sync.Once
	Remote string

	// The bytes of the lines in out and bulk.
	waiting atomic.Int64
}

// queued is how many lines may wait for a slow peer before it is closed, and queuedBytes
// how many bytes (#3650): a line can be tens of MiB, so a count alone does not bound it.
// A single line larger than queuedBytes still goes when nothing waits before it.
const (
	queued      = 8192
	queuedBytes = 64 << 20
)

func newConn(ws *websocket.Conn, remote string) *Conn {
	// Until it proves a key; then by what proved one (#2150).
	ws.SetReadLimit(unprovenMessageMost)
	c := &Conn{ws: ws, out: make(chan string, queued), bulk: make(chan string, queued), done: make(chan struct{}), Remote: remote}
	go c.write()
	go c.ping()
	return c
}

func (c *Conn) write() {
	for {
		line, ok := c.next()
		if !ok {
			return
		}
		c.waiting.Add(-int64(len(line)))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := c.ws.Write(ctx, websocket.MessageText, []byte(line))
		cancel()
		if err != nil {
			c.Close()
			return
		}
	}
}

// next is the line to write now, a bulk one when no other waits; false when the socket closed.
func (c *Conn) next() (string, bool) {
	select {
	case line := <-c.out:
		return line, true
	default:
	}
	select {
	case line := <-c.out:
		return line, true
	case line := <-c.bulk:
		return line, true
	case <-c.done:
		return "", false
	}
}

// ping sends a ping every 20 s; with no pong within 20 s more, the socket goes.
func (c *Conn) ping() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.Close()
				return
			}
		case <-c.done:
			return
		}
	}
}

// Send queues a line; false when the socket is closed, or too far behind and so closed.
func (c *Conn) Send(line string) bool { return c.queue(c.out, line) }

// SendBulk is Send for a line that the others go before: a part of a machine's send (#7120).
// Bulk lines keep their order among themselves.
func (c *Conn) SendBulk(line string) bool { return c.queue(c.bulk, line) }

func (c *Conn) queue(out chan string, line string) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	size := int64(len(line))
	if held := c.waiting.Add(size); held > queuedBytes && held != size {
		c.waiting.Add(-size)
		c.Close()
		return false
	}
	select {
	case out <- line:
		return true
	default:
		c.waiting.Add(-size)
		c.Close()
		return false
	}
}

// SendNow writes a line and waits for it to go: the exchange, and a last reply.
func (c *Conn) SendNow(line string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, []byte(line))
}

// Reply is SendNow for a line whose failure needs no handling: a reply, a refusal or a
// notice to a peer that, when the write fails, has gone, and whose next Read says so.
func (c *Conn) Reply(line string) {
	_ = c.SendNow(line) // The peer has gone; the connection's Read ends it.
}

// Read is the next line.
func (c *Conn) Read(ctx context.Context) (string, error) {
	_, b, err := c.ws.Read(ctx)
	if err != nil {
		c.Close()
	}
	return string(b), err
}

// ReadWithin is the next line, if it comes within d.
func (c *Conn) ReadWithin(d time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return c.Read(ctx)
}

func (c *Conn) Close() { c.CloseWith(websocket.StatusNormalClosure, "") }

// CloseWith closes the socket with a code and a reason: 4403 for a forgotten client.
func (c *Conn) CloseWith(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		go c.ws.Close(code, reason)
	})
}

// SetReadLimit is the largest message the socket reads from now on.
func (c *Conn) SetReadLimit(n int64) { c.ws.SetReadLimit(n) }

// Done is closed when the socket is.
func (c *Conn) Done() <-chan struct{} { return c.done }
