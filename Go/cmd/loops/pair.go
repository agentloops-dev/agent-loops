package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
	"github.com/spf13/cobra"
)

func init() {
	register(func(a *app, root *cobra.Command) {
		root.AddCommand(&cobra.Command{
			Use:   "pair '<code>'",
			Short: "pair this terminal with a control plane",
			Args: func(cmd *cobra.Command, args []string) error {
				if len(args) != 1 {
					return &exit{code: exitError, msg: "loops pair '<code>'"}
				}
				return nil
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				return pair(a.ctx, a.out, args[0])
			},
		})
		root.AddCommand(&cobra.Command{
			Use:   "unpair",
			Short: "forget the pairing, here and on the control plane",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return unpair(a.ctx, a.out, a.o)
			},
		})
	})
}

func pair(ctx context.Context, w io.Writer, text string) error {
	code, err := controllink.ParseCode(text)
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("That is not a pairing code: %v", err)}
	}
	name := pairedName()
	// A terminal pairs as a Mac window does: the control plane has no kind for a CLI, and
	// the kind is only the icon its Devices list shows.
	m, err := controllink.PairClientAs(ctx, code, name, string(wire.ClientKindMac))
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Could not pair with %s (%s).", hostOf(code.URL), reason(err)) + debugLine(err)}
	}
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	for _, old := range h.Hosts {
		if old.URL == m.URL && old.Client != m.Client {
			// The old pairing's key opens nothing now.
			_ = lockKeys.remove(old.Client)
		}
	}
	h.put(*m)
	// A new pairing may be another person's: its lock is pinned on first use again.
	delete(h.LockPins, m.URL)
	if err := h.write(); err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Paired, but could not keep the pairing in %s: %v", hostsFile(), err)}
	}
	fmt.Fprintf(w, "Paired with %s as %q.\n", hostOf(m.URL), name)
	// Your machines take calls only from your devices: join the machine lock now (#6563).
	o := options{control: m.URL}
	s, err := dial(ctx, &o)
	if err != nil {
		return err
	}
	s.Close()
	fmt.Fprintln(w, "Try: loops status")
	return nil
}

func unpair(ctx context.Context, w io.Writer, o options) error {
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	m := h.pick(o.control)
	if m == nil {
		return errNotPaired
	}
	u := m.URL
	// The control plane forgets this client too, so its key opens nothing; when it does
	// not answer, the pairing goes here all the same, and its Devices list still shows it.
	forgot := false
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	if s, err := controllink.DialClient(cctx, m); err == nil {
		_, err = s.Call(cctx, "", "clients/forgetSelf", map[string]any{})
		forgot = err == nil
		s.Close()
	}
	cancel()
	if err := lockKeys.remove(m.Client); err != nil {
		fmt.Fprintf(os.Stderr, "loops: could not remove this terminal's machine lock key: %v\n", err)
	}
	h.remove(u)
	if err := h.write(); err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	if forgot {
		fmt.Fprintf(w, "Unpaired from %s.\n", hostOf(u))
	} else {
		fmt.Fprintf(w, "Unpaired here. %s did not answer: forget this terminal in Settings ▸ Control plane ▸ Clients.\n", hostOf(u))
	}
	return nil
}
