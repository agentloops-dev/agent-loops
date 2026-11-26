package main

// Pairing from the app (#6875): loops with no pairing, on a Mac where Agent Loops runs, asks
// this Mac's agentsd for a pairing over its local socket (clients/pairTerminal). A person
// approves it once in Needs you there. The window then makes a one-use client code for this
// Mac's control plane and puts this terminal's key in the machine lock, so there is no code to
// copy and no Compare Digits. On Linux, or with no agentsd answering, nothing changes: loops
// auth login '<code>'.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// errNoApp says this machine has no Agent Loops to pair from.
var errNoApp = errors.New("no Agent Loops on this Mac")

// appSocket is this Mac's agentsd socket: $AGENTS_ROOT's, else the standard root's. Empty on
// another OS, or when there is none.
var appSocket = func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	root := os.Getenv("AGENTS_ROOT")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = store.StandardRoot(home)
	}
	socket := store.At(root).Socket()
	if fi, err := os.Stat(socket); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return ""
	}
	return socket
}

// appCallWait is how long one call may take: agentsd holds it up to 45 s on the request.
var appCallWait = 90 * time.Second

// pairFromApp pairs this terminal through Agent Loops on this Mac. errNoApp when there is no
// app to ask; else the pairing is kept, or the error has words.
func pairFromApp(ctx context.Context, say io.Writer) error {
	socket := appSocket()
	if socket == "" {
		return errNoApp
	}
	h, err := readHosts()
	if err != nil {
		return &exit{code: exitError, msg: err.Error()}
	}
	key, err := machinelock.NewKey()
	if err != nil {
		return err
	}
	raw, err := key.Bytes()
	if err != nil {
		return err
	}
	client := controllink.NewClientID()
	request := wire.TerminalPairingRequest{Name: pairedName(), Client: client, Key: machinelock.PublicKey(key)}
	if c := commandName(); c != "" {
		request.Command = &c
	}
	text, err := askApp(ctx, socket, request, say)
	if errors.Is(err, errNoApp) {
		return err
	}
	if err != nil {
		return &exit{code: exitNotPair, msg: fmt.Sprintf("This terminal is not paired: %v\n", err) +
			"Get a code in Settings ▸ Devices, then: loops auth login '<code>'"}
	}
	code, err := controllink.ParseCode(text)
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Agent Loops gave a code that is not a pairing code: %v", err)}
	}
	if err := h.room(text, code.URL); err != nil {
		return err
	}
	m, err := controllink.PairClientWithID(ctx, code, client, pairedName(), string(wire.ClientKindMac))
	if err != nil {
		return &exit{code: exitError, msg: fmt.Sprintf("Could not pair with %s (%s).", hostOf(code.URL), reason(err)) + debugLine(err)}
	}
	// The window put this key in the lock for the id asked for; it is the key that counts.
	if err := lockKeys.save(m.Client, raw); err != nil {
		_ = controllink.ForgetSelf(ctx, m)
		return &exit{code: exitError, msg: fmt.Sprintf("Could not keep this terminal's machine lock key (%v).", err)}
	}
	return keepPairing(ctx, say, h, m, code)
}

// askApp asks agentsd on socket until a person answered: the code, or an error with words.
// errNoApp when agentsd does not answer at all.
func askApp(ctx context.Context, socket string, r wire.TerminalPairingRequest, say io.Writer) (string, error) {
	said := false
	for first := true; ; first = false {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var got wire.TerminalPairingReply
		err := callApp(ctx, socket, wire.CallClientsPairTerminal.Name, r, &got)
		var refused *appError
		switch {
		case err != nil && first && !errors.As(err, &refused):
			return "", errNoApp // nothing answers there: the app is not running
		case errors.As(err, &refused) && refused.code == methodNotFound:
			return "", errNoApp // an Agent Loops from before #6875
		case err != nil:
			return "", err
		}
		if got.Code != nil {
			return *got.Code, nil
		}
		if got.Request == nil {
			return "", errors.New("agentsd gave no code")
		}
		if !said && got.Words != nil {
			fmt.Fprintln(say, *got.Words)
			said = true
		}
	}
}

// appError is agentsd's refusal, in its own words.
type appError struct {
	code    int
	message string
}

func (e *appError) Error() string { return e.message }

// callApp makes one JSON-RPC call on agentsd's socket and decodes its result.
func callApp(ctx context.Context, socket, method string, params, result any) error {
	var dialer net.Dialer
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	c, err := dialer.DialContext(dctx, "unix", socket)
	cancel()
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(appCallWait))
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return err
	}
	r := bufio.NewReaderSize(c, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		var reply struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &reply) != nil || string(reply.ID) != "1" {
			continue // a notice, not the reply
		}
		if reply.Error != nil {
			return &appError{code: reply.Error.Code, message: reply.Error.Message}
		}
		return json.Unmarshal(reply.Result, result)
	}
}

// commandName is the program that ran loops, as the request names it (a shell, or an agent's
// runtime), so the person sees who asks. agentsd cannot check it.
var commandName = func() string {
	ppid := os.Getppid()
	if ppid <= 1 {
		return ""
	}
	out, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(ppid)).Output() //nolint:gosec // G204: /bin/ps with a pid, a number
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}
