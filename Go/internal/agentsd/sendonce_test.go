package agentsd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// uploadCalls uploads data from d as a send does, and is each call the control plane got, as
// the JSON it came in.
func uploadCalls(t *testing.T, d *Daemon, data []byte) [][]byte {
	t.Helper()
	var mu sync.Mutex
	var calls [][]byte
	d.SetControlAsk(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if call, ok := params.(wire.MachineWorkspaceCall); ok && call.Method == sendFolderMethod {
			b, err := json.Marshal(params)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			calls = append(calls, b)
			mu.Unlock()
		}
		return json.RawMessage(`{}`), nil
	})
	id := store.NewUUID()
	file, err := d.outgoingFile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.outgoingFolder(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.uploadThere(personSending, "devbox", id, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return calls
}

// uploadParts reads calls: the bytes of data in each call's own step and in its signed copy.
func uploadParts(t *testing.T, calls [][]byte, state *machinelock.State) (own, signed []byte, total int) {
	t.Helper()
	for _, b := range calls {
		total += len(b)
		var call wire.MachineWorkspaceCall
		mustUnmarshal(t, b, &call)
		raw, err := json.Marshal(call.Params)
		if err != nil {
			t.Fatal(err)
		}
		var step wire.MachineFolderSend
		mustUnmarshal(t, raw, &step)
		if step.Host != "devbox" {
			t.Fatalf("host %q", step.Host)
		}
		if step.Upload != nil {
			own = append(own, step.Upload.Data...)
		}
		if step.UploadSigned != nil && (step.Upload != nil || call.Deliver == nil) {
			t.Fatalf("a signed part with another step, or with no signature: %t", call.Deliver == nil)
		}
		if call.Deliver == nil {
			continue
		}
		// The signature is over the bytes the other machine gets.
		if call.Deliver.Method != wire.CallFoldersReceiveUpload.Name {
			t.Fatal(call.Deliver.Method)
		}
		if _, err := machinelock.CheckRequest(state, "h-2", call.Deliver.Method, []byte(call.Deliver.Params), call.Deliver.Lock, time.Now()); err != nil {
			t.Fatal(err)
		}
		var part wire.ReceiveUploadRequest
		mustUnmarshal(t, []byte(call.Deliver.Params), &part)
		signed = append(signed, part.Data...)
	}
	return own, signed, total
}

// #7118: with a machine lock, a part's data goes to the control plane once, in what is
// signed. An older control plane gets both, as before; with no lock nothing is signed.
func TestALockedUploadsDataGoesToTheControlPlaneOnce(t *testing.T) {
	data := make([]byte, sendThereParts+sendThereParts/2)
	_, _ = rand.Read(data)
	base64 := len(data) * 4 / 3
	knows := func() []string { return []string{wire.CapFoldersSignedUpload1} }

	t.Run("lock", func(t *testing.T) {
		d, _, _, state := lockedMachine(t)
		defer d.Shutdown()
		d.SetControlCapabilities(knows)
		calls := uploadCalls(t, d, data)
		own, signed, total := uploadParts(t, calls, state)
		if len(calls) != 2 || len(own) != 0 || !bytes.Equal(signed, data) {
			t.Fatalf("%d calls, %d bytes in their steps, %d signed", len(calls), len(own), len(signed))
		}
		if total > base64+base64/10 {
			t.Fatalf("%d bytes for %d of data (%d in base64): the data is there more than once", total, len(data), base64)
		}
	})
	t.Run("lock, an older control plane", func(t *testing.T) {
		d, _, _, state := lockedMachine(t)
		defer d.Shutdown()
		d.SetControlCapabilities(func() []string { return nil })
		own, signed, _ := uploadParts(t, uploadCalls(t, d, data), state)
		if !bytes.Equal(own, data) || !bytes.Equal(signed, data) {
			t.Fatalf("%d bytes in their steps, %d signed", len(own), len(signed))
		}
	})
	t.Run("no lock", func(t *testing.T) {
		d, _, _ := joinedMachine(t)
		defer d.Shutdown()
		d.SetControlCapabilities(knows)
		own, signed, total := uploadParts(t, uploadCalls(t, d, data), nil)
		if !bytes.Equal(own, data) || len(signed) != 0 || total > base64+base64/10 {
			t.Fatalf("%d bytes in their steps, %d signed, %d in all", len(own), len(signed), total)
		}
	})
}
