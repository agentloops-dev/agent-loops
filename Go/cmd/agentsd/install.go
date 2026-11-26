package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlsetup"
	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
)

const installUsage = `usage: agentsd install --join <host code> [--name NAME] [--runtime ID]... [--project DIR]... [--from-pool]
       agentsd install --control [--no-agents] [--domain NAME [--acme-email ADDR]] [--port N] [--name NAME] [--runtime ID]...
       agentsd install --control --no-agents --join <host code> [--domain NAME [--acme-email ADDR]] [--port N] [--name NAME]

--control also runs the control plane (agents-control) as a systemd user unit beside this host (#6610): no root, no container. Alone with --no-agents, the machine only runs the control plane. With both roles the host joins that control plane by itself, so no --join: this is for one person's machine only, never a team's (an agent there could read the control key). With --no-agents and --join, the machine also joins the current control plane with the control role alone (#7314), so a team's control plane can move to it: it takes no sessions, folders or providers. --domain makes a publicly trusted certificate (ACME) in the unit; without it the certificate is its own, pinned in every code.

Sets this machine up (#5356): installs this host, joins the control plane, installs the
runtimes, runs each project's .agents/setup.sh, and starts one short session to check the machine answers (step 8; --no-smoke leaves it out). Each step is reported to the control plane
as it ends. Run it again to do only what is missing.`

// installCommand is `agentsd install --join <code>`, the one installer.
func installCommand(args []string) int {
	var o hostsetup.Options
	var c controlsetup.Options
	noSmoke, control, noAgents := false, false, false
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch a := args[i]; {
		case a == "--join":
			o.Join = next()
		case a == "--name":
			o.Name = next()
		case a == "--runtime":
			o.Runtimes = append(o.Runtimes, next())
		case a == "--project":
			o.Projects = append(o.Projects, next())
		case a == "--no-smoke":
			noSmoke = true
		case a == "--from-pool":
			o.FromPool = true
		case a == "--control":
			control = true
		case a == "--no-agents":
			noAgents = true
		case a == "--domain":
			c.Domain = next()
		case a == "--acme-email":
			c.ACMEEmail = next()
		case a == "--port":
			c.Port, _ = strconv.Atoi(next())
		case strings.HasPrefix(a, "--join="):
			o.Join = strings.TrimPrefix(a, "--join=")
		default:
			fmt.Fprintln(os.Stderr, installUsage)
			return 2
		}
	}
	o.Smoke = !noSmoke
	if noAgents && !control || control && !noAgents && o.Join != "" || (c.Domain != "" || c.Port != 0 || c.ACMEEmail != "") && !control ||
		noAgents && (len(o.Runtimes) > 0 || len(o.Projects) > 0 || o.FromPool) ||
		o.Join == "" && !control {
		fmt.Fprintln(os.Stderr, installUsage)
		return 2
	}
	var err error
	if o.Home, err = os.UserHomeDir(); err != nil {
		fmt.Fprintln(os.Stderr, "no home folder:", err)
		return 1
	}
	if o.Executable, err = os.Executable(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	o.ControlOnly = noAgents && o.Join != ""
	if why := refuseInstall(o.Home, control, noAgents); why != "" {
		fmt.Fprintln(os.Stderr, "The machine is not set up:", why)
		return 1
	}
	for i, p := range o.Projects {
		o.Projects[i], _ = filepath.Abs(p)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if control {
		c.Home, c.Executable, c.Name, c.Agents = o.Home, o.Executable, o.Name, !noAgents
		if code, ok := installControl(ctx, c, noAgents); !ok {
			return 1
		} else if noAgents && o.Join == "" {
			return 0
		} else if !noAgents {
			o.Join = code
		}
	}
	socket := store.At(hostsetup.LayoutOf(o.Home).Root).Socket()
	deps := hostsetup.Deps{
		StartHost: startHost,
		Running:   func(root string) bool { return running(store.At(root).Lock(), 0) },
		Call: func(method string, params any) (json.RawMessage, error) {
			return callSocketResult(socket, method, params)
		},
		Out: os.Stdout,
	}
	if err := hostsetup.Run(ctx, o, deps); err != nil {
		fmt.Fprintln(os.Stderr, "The machine is not set up:", err)
		return 1
	}
	fmt.Println("This machine is set up.")
	return 0
}

// refuseInstall says why this install would put the other role beside the one this machine
// has (#7305), before it changes anything; "" when it would not. install --control, with the
// agents too, asks the control plane itself (HostCode), as only it knows its people.
func refuseInstall(home string, control, noAgents bool) string {
	l := hostsetup.LayoutOf(home)
	joined, controlOnly := l.Joined(), controllink.ControlOnly(l.Root)
	switch {
	case control && noAgents && joined && !controlOnly:
		return "agents run on this machine already, and could read the control plane's key and its data. Run the control plane on another machine, or remove this host first. When you are the only person, leave out --no-agents."
	case !control && !joined && controlsetup.Installed(home):
		return "this machine runs a control plane, and agents here could read its key and its data. Join another machine instead. When you are the only person, run agentsd install --control with no --no-agents."
	case controlOnly && !noAgents:
		return "this machine joined with the control role alone (#7314), and agents here could read the control plane's key and its data. Join another machine instead."
	}
	return ""
}

// installControl runs the control role (#6610); with the agents role too, it returns the host
// code the host joins with. The bool is false when it failed, having said why.
func installControl(ctx context.Context, c controlsetup.Options, noAgents bool) (string, bool) {
	deps := controlsetup.Deps{Systemd: controlsetup.SystemdUser, Run: controlsetup.RunCommand, Out: os.Stdout}
	if err := controlsetup.Install(ctx, c, deps); err != nil {
		fmt.Fprintln(os.Stderr, "The control plane is not set up:", err)
		return "", false
	}
	if noAgents {
		fmt.Println("This machine runs the control plane only.")
		return "", true
	}
	code, err := controlsetup.HostCode(ctx, c.Home, deps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "The control plane is not set up:", err)
		return "", false
	}
	return code, true
}

// startHost starts the host as install.sh does: a systemd user service where systemd runs
// (an /run/systemd/system folder; a codespace's systemctl is a shim, #3366), else detached.
func startHost(binary, root, name string) (string, error) {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		if _, err := exec.LookPath("systemctl"); err == nil && exec.Command("systemctl", "--user", "show-environment").Run() == nil {
			unit := filepath.Join(home, ".config", "systemd", "user", "agents-host.service")
			text := fmt.Sprintf(`[Unit]
Description=Agent Loops host (%s)
After=network-online.target

[Service]
ExecStart=%%h/.agents-server/bin/current --root %%h/.agents-server/root --serve --control-network --host-name %q
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, name, name)
			if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
				return "", err
			}
			if err := os.WriteFile(unit, []byte(text), 0o600); err != nil {
				return "", err
			}
			for _, step := range [][]string{{"daemon-reload"}, {"enable", "agents-host.service"}, {"restart", "agents-host.service"}} {
				if out, err := exec.Command("systemctl", append([]string{"--user"}, step...)...).CombinedOutput(); err != nil {
					return "", fmt.Errorf("systemctl %s: %v: %s", step[0], err, strings.TrimSpace(string(out)))
				}
			}
			return "as the systemd user service agents-host", nil
		}
	}
	cmd := exec.Command(binary, "--root", root, "--serve", "--detach", "--control-network", "--host-name", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return "in the background", nil
}
