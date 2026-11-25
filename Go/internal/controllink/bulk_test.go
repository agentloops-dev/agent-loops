package controllink

import "testing"

// The link's other lines go before a send's parts (#7120).
func TestTheOtherLinesGoBeforeBulk(t *testing.T) {
	s := &sender{out: make(chan string, 4), bulk: make(chan string, 4), done: make(chan struct{})}
	for _, line := range []string{"part 1", "part 2"} {
		if err := s.queue(s.bulk, line); err != nil {
			t.Fatal(err)
		}
	}
	for _, line := range []string{"a", "b"} {
		if err := s.send(line); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b", "part 1", "part 2"} {
		if got, ok := s.next(); !ok || got != want {
			t.Fatal(got, want)
		}
	}
	close(s.done)
	if _, ok := s.next(); ok {
		t.Fatal("a line after the link ended")
	}
}
