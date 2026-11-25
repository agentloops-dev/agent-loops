package agentsd

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Inputs of a stream take their turns in the order typed, whatever order they run in (#7147).
func TestShellInputsTakeTheirTurnsInOrder(t *testing.T) {
	t.Parallel()
	var o shellOrder
	var mu sync.Mutex
	var got []int64
	var wg sync.WaitGroup
	const n = 100
	for i := n - 1; i >= 0; i-- {
		wg.Go(func() {
			done, err := o.turn(context.Background(), "A", int64(i))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			got = append(got, int64(i))
			mu.Unlock()
			done()
		})
	}
	wg.Wait()
	if len(got) != n || !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] < got[j] }) {
		t.Errorf("the inputs went in the order %v", got)
	}
	// Each place is typed once, and none far past the next.
	for _, seq := range []int64{0, n - 1, n + shellAhead + 1} {
		if _, err := o.turn(context.Background(), "A", seq); err == nil {
			t.Errorf("place %d was typed again or too soon", seq)
		}
	}
	// Another screen's stream has an order of its own.
	if done, err := o.turn(context.Background(), "B", 0); err != nil {
		t.Error(err)
	} else {
		done()
	}
}

// What was typed after an input that was lost is not typed.
func TestAShellInputAfterALostOneIsNotTyped(t *testing.T) {
	t.Parallel()
	var o shellOrder
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.turn(ctx, "A", 1); err == nil {
		t.Error("an input was typed with the one before it lost")
	}
}

// A shell keeps a bounded number of streams, the newest used.
func TestAShellsStreamsAreBounded(t *testing.T) {
	t.Parallel()
	var o shellOrder
	for i := range shellStreams * 3 {
		done, err := o.turn(context.Background(), wire.UUID(strconv.Itoa(i)), 0)
		must(t, err)
		done()
	}
	if len(o.streams) != shellStreams {
		t.Errorf("%d streams are kept, not %d", len(o.streams), shellStreams)
	}
	if o.streams[wire.UUID(strconv.Itoa(shellStreams*3-1))] == nil {
		t.Error("the newest stream was let go")
	}
}
