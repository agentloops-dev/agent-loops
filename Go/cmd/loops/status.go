package main

import (
	"context"
	"fmt"
	"io"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/spf13/cobra"
)

func init() {
	register(func(a *app, root *cobra.Command) {
		root.AddCommand(&cobra.Command{
			Use:   "status",
			Short: "the pairing, this machine and the folder here",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return status(a.ctx, a.out, a.o)
			},
		})
	})
}

func status(ctx context.Context, w io.Writer, o options) error {
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	m := h.pick(o.control)
	if m == nil {
		return errNotPaired
	}
	// m.Name is the control plane's own name, from the code, not this terminal's.
	at := hostOf(m.URL)
	if m.Name != "" && m.Name != at {
		at = fmt.Sprintf("%s at %s", m.Name, at)
	}
	fmt.Fprintf(w, "Paired with %s (%s)\n", at, tilde(hostsFile()))
	if len(h.Hosts) > 1 {
		for _, other := range h.Hosts {
			if other.URL != m.URL {
				fmt.Fprintf(w, "Also paired with %s (--control %s)\n", hostOf(other.URL), hostOf(other.URL))
			}
		}
	}
	s, p, err := connect(ctx, &o)
	if err != nil {
		return err
	}
	defer s.Close()
	fmt.Fprintf(w, "Machine: %s\n", p.machineName())
	if p.folder == "" {
		fmt.Fprintf(w, "Folder:  none for %s (loops folder add)\n", tilde(p.dir))
		return nil
	}
	fmt.Fprintf(w, "Folder:  %s (%s)\n", p.folderName(), tilde(store.FolderPath(p.folder)))
	return nil
}
