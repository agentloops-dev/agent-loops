package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// app is what every command reads: the context, where it writes, and the global flags.
type app struct {
	ctx context.Context
	out io.Writer
	err io.Writer
	o   options
}

// command is a noun or a verb that adds itself to the root. A new one lives in its own
// file and calls register, so the root never changes when a command is added.
type command func(*app, *cobra.Command)

var commands []command

func register(c command) {
	commands = append(commands, c)
}

func newApp(ctx context.Context, out io.Writer) *app {
	return &app{ctx: ctx, out: out, err: io.Discard}
}

// root is the cobra tree today's commands plug into. The prompt is the root's own run:
// any words that are not a command are the prompt, as they were with the hand parser.
func (a *app) root() *cobra.Command {
	r := &cobra.Command{
		Use:           `loops ["prompt"]`,
		Short:         "Agent Loops on the command line",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := checkPrintFlags(a.o); err != nil {
				return err
			}
			return checkMode(a.o.mode)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !a.o.cont && a.o.resume == "" && !a.o.print && !a.o.detach {
				return a.pick()
			}
			return a.create(args)
		},
	}
	r.SetOut(a.out)
	r.SetErr(a.err)
	r.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &exit{code: exitError, msg: err.Error() + "\nSee: loops --help"}
	})
	r.CompletionOptions.DisableDefaultCmd = true
	r.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		fmt.Fprint(cmd.OutOrStdout(), helpText(cmd))
	})
	flags := r.PersistentFlags()
	flags.StringVar(&a.o.folder, "folder", "", "work in this folder, not the one of the current directory")
	flags.StringVar(&a.o.machine, "machine", "", "the folder's machine, when it is not this computer")
	flags.StringVar(&a.o.control, "control", "", "the control plane, when this terminal is paired with several")
	flags.BoolVar(&a.o.all, "all", false, "every page, archived sessions too")
	flags.BoolVarP(&a.o.cont, "continue", "c", false, "continue the newest session here (with a prompt)")
	flags.StringVarP(&a.o.resume, "resume", "r", "", "continue one session, by id or title")
	flags.StringVar(&a.o.runtime, "runtime", "", "the runtime a new session starts on (Auto by default)")
	flags.StringVar(&a.o.mode, "mode", "", "the permission a new session starts at: plan, ask, approve-for-me or bypass-permissions")
	flags.BoolVarP(&a.o.print, "print", "p", false, "start here, follow the turn, print the last reply")
	flags.BoolVar(&a.o.noInput, "no-input", false, "do not prompt for a session; bare loops needs a terminal")
	flags.BoolVar(&a.o.detach, "detach", false, "start a session here, print its id and exit")
	flags.BoolVar(&a.o.json, "json", false, "one JSON object per line: the session first, the end last")
	flags.StringVar(&a.o.onAsk, "on-ask", "", "a card waits: wait on any client, or fail with exit 3")
	flags.DurationVar(&a.o.timeout, "timeout", 0, "end with exit 4 after this long (as 10m); the session goes on")
	flags.BoolVar(&debug, "debug", false, "when the control plane cannot be reached, its whole error too")
	for _, c := range commands {
		c(a, r)
	}
	return r
}

// run executes one line of words as cobra does, so tests and main share the tree.
func run(ctx context.Context, args []string, w io.Writer) error {
	return runTo(ctx, args, w, io.Discard)
}

func runTo(ctx context.Context, args []string, w, errOut io.Writer) error {
	a := newApp(ctx, w)
	a.err = errOut
	r := a.root()
	r.SetArgs(args)
	return r.Execute()
}

// parse reads the global flags without running a command. Cobra keeps the words that are
// not flags, here including a command word, as pflag's interspersed positional arguments.
func parse(args []string) (options, []string, error) {
	a := newApp(context.Background(), io.Discard)
	r := a.root()
	if err := r.ParseFlags(args); err != nil {
		return options{}, nil, &exit{code: exitError, msg: err.Error() + "\nSee: loops --help"}
	}
	return a.o, r.Flags().Args(), nil
}

// helpText is the wireframe's help layout, filled with the commands registered today.
func helpText(c *cobra.Command) string {
	var b strings.Builder
	b.WriteString("Agent Loops on the command line. Works in the folder of this directory.\n\n")
	b.WriteString("USAGE\n")
	if c == c.Root() {
		b.WriteString("  loops [\"prompt\"]            choose a recent session in this folder, or start one\n")
		b.WriteString("  loops <command> [flags]     run one of the commands below\n\n")
	} else {
		fmt.Fprintf(&b, "  %s%s\n\n", c.CommandPath(), strings.TrimPrefix(c.Use, c.Name()))
		fmt.Fprintf(&b, "%s\n\n", c.Short)
	}
	if len(c.Commands()) > 0 {
		b.WriteString("COMMANDS\n")
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			fmt.Fprintf(&b, "  %-12s %s\n", sub.Name(), sub.Short)
		}
		b.WriteString("\n")
	}
	b.WriteString("GLOBAL FLAGS\n")
	b.WriteString(c.Root().PersistentFlags().FlagUsages())
	if c == c.Root() {
		b.WriteString("\nEXAMPLES\n")
		b.WriteString("  $ loops \"fix the flaky test\"\n")
		b.WriteString("  $ loops sessions list --all\n")
		b.WriteString("  $ loops sessions create --detach \"Fix the login redirect\"\n")
		b.WriteString("  $ loops -p \"summarise the diff\" --on-ask=fail\n\n")
		b.WriteString("Run 'loops <command> --help' for more.\n")
	}
	return b.String()
}
