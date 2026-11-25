package s3store

import (
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// #6908: the largest record that a store of the control plane declares (a kept request,
// 40 MiB) is read from a bucket as it was written to it.
func TestTheLargestRecordIsReadBack(t *testing.T) {
	t.Skip("#7195")
	s := fake(t)
	for _, size := range []int{16 << 20, 40 << 20} {
		if _, err := s.Put(ctx, "v1/large", make([]byte, size), store.Always); err != nil {
			t.Fatal(err)
		}
		if o, err := s.Get(ctx, "v1/large"); err != nil || len(o.Data) != size {
			t.Fatalf("%d bytes were written and not read: %v", size, err)
		}
	}
}
