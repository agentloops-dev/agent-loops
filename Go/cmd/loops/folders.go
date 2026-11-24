package main

import (
	"context"
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
		folders := &cobra.Command{
			Use:   "folders add [dir]",
			Short: "folders this machine has",
			Args:  cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return &exit{code: exitError, msg: "loops folders add [dir]"}
			},
		}
		folders.AddCommand(&cobra.Command{
			Use:   "add [dir]",
			Short: "add this directory (or dir) as a folder",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				dir := ""
				if len(args) == 1 {
					dir = args[0]
				}
				return folderAdd(a.ctx, os.Stderr, a.o, dir)
			},
		})
		root.AddCommand(folders)
	})
}

func folderAdd(ctx context.Context, say io.Writer, o options, dir string) error {
	s, err := dial(ctx, &o)
	if err != nil {
		return err
	}
	defer s.Close()
	return addFolder(ctx, say, s, o, dir)
}

func addFolder(ctx context.Context, say io.Writer, c caller, o options, dir string) error {
	list, err := machines(ctx, c)
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
	if _, err := c.Call(ctx, string(m.ID), "folders/add", wire.AddFolderRequest{Folder: store.PathURL(p.dir)}); err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Cannot add %s: %v\nNothing changed.", tilde(p.dir), reason(err)) + debugLine(err)}
	}
	fmt.Fprintf(say, "Added %s as a folder on %s.\n", tilde(p.dir), p.machineName())
	return nil
}
