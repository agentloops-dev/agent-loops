package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// caller is what the commands need of a control plane: *controllink.Session, or a fake in
// a test.
type caller interface {
	Call(ctx context.Context, host, method string, params any) (json.RawMessage, error)
}

// place is where a command works: a machine of the control plane and, once found, one of
// its folders.
type place struct {
	machine wire.ControlMachine
	folder  wire.URL // empty when the directory is in no folder
	dir     string   // the directory the command looked from
}

func (p place) machineName() string {
	if p.machine.Name != "" {
		return p.machine.Name
	}
	return string(p.machine.ID)
}

func (p place) folderName() string { return filepath.Base(store.FolderPath(p.folder)) }

// machines is machines/list, which answers a list, or a page from a newer control plane.
func machines(ctx context.Context, c caller) ([]wire.ControlMachine, error) {
	r, err := c.Call(ctx, "", "machines/list", nil)
	if err != nil {
		return nil, err
	}
	if t := bytes.TrimSpace(r); len(t) > 0 && t[0] == '[' {
		var list []wire.ControlMachine
		if err := json.Unmarshal(t, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var page wire.MachineList
	if err := json.Unmarshal(r, &page); err != nil {
		return nil, err
	}
	return page.Machines, nil
}

// pickMachine is --machine (its name or id), else this computer: the machine whose id is
// this computer's (machine.ID, as agentsd says it in its hello), else the one with this
// computer's host name.
func pickMachine(list []wire.ControlMachine, want, here, hostname string) (wire.ControlMachine, error) {
	if want != "" {
		for _, m := range list {
			if strings.EqualFold(string(m.ID), want) || strings.EqualFold(m.Name, want) || m.MachineID != nil && strings.EqualFold(*m.MachineID, want) {
				return m, nil
			}
		}
		return wire.ControlMachine{}, &exit{code: exitError, msg: fmt.Sprintf("No machine %q on this control plane.\nSee them: loops auth status", want)}
	}
	for _, m := range list {
		if m.MachineID != nil && here != "" && strings.EqualFold(*m.MachineID, here) {
			return m, nil
		}
	}
	short := strings.TrimSuffix(hostname, ".local")
	for _, m := range list {
		if short != "" && strings.EqualFold(m.Name, short) {
			return m, nil
		}
	}
	return wire.ControlMachine{}, &exit{code: exitError, msg: "This computer is not a machine of this control plane.\nName one: loops --machine <name> …"}
}

// candidates is dir and each of its parents, nearest first: the folders folders/find is
// asked about, so a directory deep in a folder (or a worktree in it) finds the folder.
func candidates(dir string) []string {
	dir = filepath.Clean(dir)
	out := []string{dir}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return out
		}
		out = append(out, parent)
		dir = parent
	}
}

// nearest is the first found folder that is a folder of the machine. folders/find answers
// the typed paths in the order they were asked, so the first is the nearest.
func nearest(found []wire.FoundFolder) (wire.URL, bool) {
	for _, f := range found {
		if f.IsFolder {
			return f.Folder, true
		}
	}
	return "", false
}

// locate is the place of a command: the machine, then the folder of dir in one
// folders/find with dir and its parents. --folder names a folder by its path or its name.
func locate(ctx context.Context, c caller, o options, dir string) (place, error) {
	list, err := machines(ctx, c)
	if err != nil {
		return place{}, unreachable(o.controlName, err)
	}
	hostname, _ := os.Hostname()
	m, err := pickMachine(list, o.machine, machine.ID(), hostname)
	if err != nil {
		return place{}, err
	}
	p := place{machine: m, dir: dir}
	host := string(m.ID)
	switch {
	case o.folder != "" && !strings.Contains(o.folder, "/") && !strings.HasPrefix(o.folder, "~"):
		f, err := folderNamed(ctx, c, host, o.folder)
		if err != nil {
			return place{}, err
		}
		p.folder = f
		return p, nil
	case o.folder != "":
		dir = o.folder
		if !filepath.IsAbs(dir) && !strings.HasPrefix(dir, "~") {
			dir = filepath.Join(p.dir, dir)
		}
		p.dir = dir
	}
	r, err := c.Call(ctx, host, "folders/find", wire.FoldersFindRequest{Folders: candidates(dir)})
	if err != nil {
		return place{}, unreachable(p.machineName(), err)
	}
	var found []wire.FoundFolder
	if err := json.Unmarshal(r, &found); err != nil {
		return place{}, err
	}
	if f, ok := nearest(found); ok {
		p.folder = f
	}
	return p, nil
}

// folderNamed is the machine's folder with this name (its folder name, as the window shows it).
func folderNamed(ctx context.Context, c caller, host, name string) (wire.URL, error) {
	r, err := c.Call(ctx, host, "folders/list", wire.FoldersListRequest{})
	if err != nil {
		return "", unreachable(host, err)
	}
	var list []wire.FolderSummary
	if err := json.Unmarshal(r, &list); err != nil {
		return "", err
	}
	var hits []wire.URL
	for _, f := range list {
		if strings.EqualFold(f.Name, name) || strings.EqualFold(filepath.Base(store.FolderPath(f.Folder.Folder)), name) {
			hits = append(hits, f.Folder.Folder)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", &exit{code: exitNoFolder, msg: fmt.Sprintf("No folder %q on this machine.", name)}
	default:
		return "", &exit{code: exitError, msg: fmt.Sprintf("%d folders are named %q: name one by its path: loops --folder <path> …", len(hits), name)}
	}
}

// notInFolder is state 6 of the wireframes (exit 2).
func notInFolder(p place) error {
	return &exit{code: exitNoFolder, msg: fmt.Sprintf("%s is not in a folder on %s.\nAdd it: loops folders add",
		tilde(p.dir), p.machineName())}
}

// tilde writes a path under the home directory as ~/….
func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}
