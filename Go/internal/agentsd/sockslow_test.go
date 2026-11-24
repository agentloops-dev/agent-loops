package agentsd

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// A client that reads all the time, only slowly, is not let go, however long a burst or
// one line takes it: the deadline is for a piece, not for all of it (#7012). The time is
// synctest's, so the machine's load has no part in it.
func TestASlowSocketClientThatReadsIsNotLetGo(t *testing.T) {
	t.Parallel()
	for name, lines := range map[string][]string{
		"a long burst": strings.Split(strings.Repeat(strings.Repeat("x", 16<<10)+" ", 40)+"end", " "),
		"a long line":  {strings.Repeat("x", 40*sockPiece)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				server, client := net.Pipe() // it has no buffer: a write ends when it is read
				s := newSockConn(server, RoleControl)
				defer s.Close()
				want := 0
				for _, line := range lines {
					must(t, s.Write(line))
					want += len(line) + 1
				}
				began := time.Now()
				buf := make([]byte, 16<<10)
				for got := 0; got < want; {
					time.Sleep(time.Second)
					n, err := client.Read(buf)
					got += n
					if err != nil {
						t.Fatalf("let go after %s, with %d of %d bytes read: %v", time.Since(began), got, want, err)
					}
				}
				if took := time.Since(began); took <= 2*sockWriteDeadline {
					t.Fatalf("the client read it all in %s, so the test shows nothing", took)
				}
			})
		})
	}
}

// A client that stops reading is still let go, sockWriteDeadline after it stopped.
func TestASocketClientThatStopsReadingIsLetGoAtTheDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		server, client := net.Pipe()
		s := newSockConn(server, RoleControl)
		defer s.Close()
		must(t, s.Write("first"))
		if line, err := bufio.NewReader(client).ReadString('\n'); err != nil || line != "first\n" {
			t.Fatalf("read %q, %v", line, err)
		}
		must(t, s.Write(strings.Repeat("x", 2*sockPiece)))
		began := time.Now()
		<-s.done
		if took := time.Since(began); took != sockWriteDeadline {
			t.Fatalf("let go after %s, not %s", took, sockWriteDeadline)
		}
		if _, err := client.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("the read after it: %v, not EOF", err)
		}
	})
}
