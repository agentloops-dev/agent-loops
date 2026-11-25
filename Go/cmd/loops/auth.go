package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
	"github.com/spf13/cobra"
)

func init() {
	register(func(a *app, root *cobra.Command) {
		auth := &cobra.Command{
			Use:   "auth <login|logout|status>",
			Short: "pair this terminal, forget it, or see its state",
			Args:  cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return &exit{code: exitError, msg: "loops auth <login|logout|status>"}
			},
		}
		auth.AddCommand(&cobra.Command{
			Use:   "login '<code>'",
			Short: "pair this terminal with a control plane",
			Args: func(cmd *cobra.Command, args []string) error {
				if len(args) != 1 {
					return &exit{code: exitError, msg: "loops auth login '<code>'"}
				}
				return nil
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				return authLogin(a.ctx, os.Stderr, args[0])
			},
		})
		auth.AddCommand(&cobra.Command{
			Use:   "logout",
			Short: "forget the pairing, here and on the control plane",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return authLogout(a.ctx, os.Stderr, a.o)
			},
		})
		auth.AddCommand(&cobra.Command{
			Use:   "status",
			Short: "the pairing, this machine and the folder here",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return authStatus(a.ctx, a.out, a.o)
			},
		})
		root.AddCommand(auth)
	})
}

func authLogin(ctx context.Context, say io.Writer, text string) error {
	code, err := controllink.ParseCode(text)
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("That is not a pairing code: %v", err)}
	}
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	// Before the code is used: a pairing that is not kept would stay on the control plane.
	if err := h.room(text, code.URL); err != nil {
		return err
	}
	name := pairedName()
	// A terminal pairs as a Mac window does: the control plane has no kind for a CLI, and
	// the kind is only the icon its Devices list shows.
	m, err := controllink.PairClientAs(ctx, code, name, string(wire.ClientKindMac))
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Could not pair with %s (%s).", hostOf(code.URL), reason(err)) + debugLine(err)}
	}
	if err := fits(m); err != nil {
		_ = controllink.ForgetSelf(ctx, m) // best effort: its Devices list shows one that stays
		return &exit{code: exitError, msg: fmt.Sprintf("The pairing with %s is not kept: %v.", hostOf(code.URL), err)}
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
	fmt.Fprintf(say, "Paired with %s as %s.\n", hostOf(m.URL), m.Client)
	fmt.Fprintln(say, "Profile: default")
	// Your machines take calls only from your devices: join the machine lock now (#6563).
	o := options{control: m.URL}
	s, err := dial(ctx, &o)
	if err != nil {
		return err
	}
	s.Close()
	return nil
}

func authLogout(ctx context.Context, say io.Writer, o options) error {
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
	forgot := controllink.ForgetSelf(ctx, m) == nil
	if err := lockKeys.remove(m.Client); err != nil {
		fmt.Fprintf(say, "loops: could not remove this terminal's machine lock key: %v\n", err)
	}
	h.remove(u)
	if err := h.write(); err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	if forgot {
		fmt.Fprintf(say, "Unpaired. %s forgets this terminal too.\n", hostOf(u))
	} else {
		fmt.Fprintf(say, "Unpaired here. %s did not answer: forget this terminal in Settings ▸ Devices.\n", hostOf(u))
	}
	return nil
}

func authStatus(ctx context.Context, out io.Writer, o options) error {
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	m := h.pick(o.control)
	if m == nil {
		return errNotPaired
	}
	s, p, err := connect(ctx, &o)
	if err != nil {
		return err
	}
	defer s.Close()
	folder := "none"
	if p.folder != "" {
		folder = tilde(store.FolderPath(p.folder))
	}
	printStatus(out, hostOf(m.URL), m.Client, p.machineName(), folder)
	return nil
}

func printStatus(w io.Writer, control, client, machine, folder string) {
	fmt.Fprintln(w, "PROFILE   CONTROL PLANE               CLIENT          MACHINE    FOLDER")
	fmt.Fprintf(w, "%-10s%-28s%-16s%-11s%s\n", "default", control, client, machine, folder)
}
