package main

import (
	"context"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	register(func(a *app, root *cobra.Command) {
		root.AddCommand(&cobra.Command{
			Use:   "ls",
			Short: "this folder's sessions (20; --all for every one)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return ls(a.ctx, a.out, a.o)
			},
		})
	})
}

func ls(ctx context.Context, w io.Writer, o options) error {
	s, p, err := connect(ctx, &o)
	if err != nil {
		return err
	}
	defer s.Close()
	if p.folder == "" {
		return notInFolder(p)
	}
	list, more, err := listSessions(ctx, s, p, o.all)
	if err != nil {
		return err
	}
	printList(w, p, list, more, o.all, time.Now())
	return nil
}
