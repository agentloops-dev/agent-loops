package main

import (
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	register(func(a *app, root *cobra.Command) {
		sessions := &cobra.Command{
			Use:   "sessions",
			Short: "list, start and follow sessions in this folder",
			Args:  cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return &exit{code: exitError, msg: "loops sessions <list|create|follow>"}
			},
		}
		sessions.AddCommand(&cobra.Command{
			Use:   "list",
			Short: "this folder's sessions (20; --all for every one)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.list()
			},
		})
		sessions.AddCommand(&cobra.Command{
			Use:   `create ["prompt"]`,
			Short: "start a session here with this prompt, and follow it",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.create(args)
			},
		})
		sessions.AddCommand(&cobra.Command{
			Use:   "follow [<id>]",
			Short: "continue the newest session here, or one by id or title",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.follow(args)
			},
		})
		root.AddCommand(sessions)
	})
}

func (a *app) create(args []string) error {
	if a.o.print || a.o.detach {
		return printMode(a.ctx, a.out, a.o, args)
	}
	return chat(a.ctx, a.o, chatArgs{text: strings.Join(args, " "), cont: a.o.cont, resume: a.o.resume})
}

func (a *app) pick() error {
	if a.o.noInput || !terminalAvailable() {
		return errNoTerminal
	}
	return chat(a.ctx, a.o, chatArgs{pick: true})
}

func (a *app) follow(args []string) error {
	o := a.o
	o.cont, o.resume = false, ""
	switch len(args) {
	case 0:
		o.cont = true
	case 1:
		o.resume = args[0]
	default:
		return &exit{code: exitError, msg: "loops sessions follow [<id>]"}
	}
	return chat(a.ctx, o, chatArgs{cont: o.cont, resume: o.resume})
}

func (a *app) list() error {
	s, p, err := connect(a.ctx, &a.o)
	if err != nil {
		return err
	}
	defer s.Close()
	if p.folder == "" {
		return notInFolder(p)
	}
	list, more, err := listSessions(a.ctx, s, p, a.o.all)
	if err != nil {
		return err
	}
	printList(a.out, a.err, styleOf(a.out), p, list, more, a.o.all, time.Now())
	return nil
}
