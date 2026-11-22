package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
	"github.com/agentloops-dev/agent-loops/Go/wire"
	"github.com/spf13/cobra"
)

func init() {
	register(func(a *app, root *cobra.Command) {
		folder := &cobra.Command{
			Use:   "folder",
			Short: "folders this terminal works in",
			Args:  cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return &exit{code: exitError, msg: "loops folder add [dir]"}
			},
		}
		folder.AddCommand(&cobra.Command{
			Use:   "add [dir]",
			Short: "add this directory (or dir) as a folder",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				dir := ""
				if len(args) == 1 {
					dir = args[0]
				}
				return folderAdd(a.ctx, a.out, a.o, dir)
			},
		})
		root.AddCommand(folder)
	})
}

func folderAdd(ctx context.Context, w io.Writer, o options, dir string) error {
	s, err := dial(ctx, &o)
	if err != nil {
		return err
	}
	defer s.Close()
	list, err := machines(ctx, s)
	if err != nil {
		return unreachable(o.controlName, err)
	}
	hostname, _ := os.Hostname()
	m, err := pickMachine(list, o.machine, machine.ID(), hostname)
	if err != nil {
		return err
	}
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	} else if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = filepath.Join(cwd, dir)
	}
	p := place{machine: m, dir: filepath.Clean(dir)}
	r, err := s.Call(ctx, string(m.ID), "folders/add", wire.AddFolderRequest{Folder: store.PathURL(p.dir)})
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Could not add %s on %s: %v", tilde(p.dir), p.machineName(), err)}
	}
	var added wire.FolderSummary
	_ = json.Unmarshal(r, &added)
	p.folder = added.Folder.Folder
	if p.folder == "" {
		p.folder = store.PathURL(p.dir)
	}
	fmt.Fprintf(w, "Added %s on %s.\nStart a session: loops \"what this folder needs\"\n", p.folderName(), p.machineName())
	return nil
}
