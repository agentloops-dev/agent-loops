package s3store

import (
	"errors"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/flaky"
)

// #6908: the largest record that a store of the control plane declares (a kept request,
// 40 MiB) is read from a bucket as it was written to it.
func TestTheLargestRecordIsReadBack(t *testing.T) {
	flaky.UnderLoad(t, "#7435")
	s := fake(t)
	for _, size := range []int{16 << 20, providers.KeptRecordBytes, MaxRecord} {
		if _, err := s.Put(ctx, "v1/large", make([]byte, size), store.Always); err != nil {
			t.Fatal(err)
		}
		if o, err := s.Get(ctx, "v1/large"); err != nil || len(o.Data) != size {
			t.Fatalf("%d bytes were written and not read: %v", size, err)
		}
	}
}

// A write over the largest is refused, with the earlier object as it was.
func TestAWriteOverTheLargestIsRefused(t *testing.T) {
	s := fake(t)
	if _, err := s.Put(ctx, "v1/large", []byte("kept"), store.Always); err != nil {
		t.Fatal(err)
	}
	var limit *store.LimitError
	if _, err := s.Put(ctx, "v1/large", make([]byte, MaxRecord+1), store.Always); !errors.As(err, &limit) {
		t.Fatalf("not refused: %v", err)
	}
	if o, err := s.Get(ctx, "v1/large"); err != nil || string(o.Data) != "kept" {
		t.Fatalf("the earlier object changed: %v", err)
	}
}
