package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The warm pools' ties to the rest of the control plane (#5363): the providers that make and
// delete their machines, the set-up record that says when one is ready, and pool/*.

// poolsBackend is poolBackend over the providers and the machine set-up.
type poolsBackend struct{ s *Service }

func (b poolsBackend) Make(person, provider, name, size, region string) (string, error) {
	return b.s.providers.PoolMake(person, provider, name, size, region)
}

func (b poolsBackend) Delete(person, provider, workspace string) error {
	return b.s.providers.PoolDelete(person, provider, workspace)
}

func (b poolsBackend) HasProvider(provider string) bool { return b.s.providers.HasProvider(provider) }

// Stage: a pool machine is ready when its set-up has done steps 1 to 6 (create to projects);
// it fails when a step failed or it has not got so far in poolJoinGiveUp. Steps 7 and 8 are the
// claim's.
func (b poolsBackend) Stage(provider, workspace string, madeAt, now time.Time) poolStage {
	late := now.Sub(madeAt) > poolJoinGiveUp
	host := b.s.providers.PoolHost(provider, workspace)
	if host == nil {
		if late {
			return stageFailed
		}
		return stageSettingUp
	}
	stored, _, err := b.s.readMachineSetup(context.Background(), host.ID)
	if err != nil || stored == nil {
		if late {
			return stageFailed
		}
		return stageSettingUp
	}
	if stored.failed() {
		return stageFailed
	}
	for _, step := range stored.Steps[:6] {
		if step.State != wire.MachineSetupStateDone {
			if late {
				return stageFailed
			}
			return stageSettingUp
		}
	}
	return stageReady
}

// Ping: the host answers and the provider still lists the machine (#5365).
func (b poolsBackend) Ping(provider, workspace string) bool {
	host := b.s.providers.PoolHost(provider, workspace)
	return host != nil && b.s.providers.PoolOnline(host.ID) && b.s.providers.PoolListed(provider, workspace)
}

// Bind gives a pool machine to a person: its owner and Stop when idle, then the catch-up (#5365).
// Steps 1 to 5 were done in the pool; 6 (projects), 7 (sign in) and 8 (smoke) wait to run again
// on the machine: its host asks for them with machine/setupWork (#6896). Step 6 brings the
// pool's projects up to their last commit, and gets those the pool did not have yet.
func (b poolsBackend) Bind(person, provider, workspace string, stopWhenIdle *int, projects []string) (string, error) {
	host := b.s.providers.PoolHost(provider, workspace)
	if host == nil {
		return "", fmt.Errorf("the machine has not joined")
	}
	if err := b.s.providers.PoolBind(host.ID, person, stopWhenIdle); err != nil {
		return "", err
	}
	_, err := b.s.changeMachineSetup(context.Background(), host.ID, true, func(st *storedSetup) error {
		for i := range st.Steps {
			if i < 5 {
				st.Steps[i].FromPool = true
				continue
			}
			st.Steps[i] = storedSetupStep{ID: st.Steps[i].ID, State: wire.MachineSetupStateWaiting}
		}
		st.Ready, st.Projects = nil, projects
		return nil
	})
	if err != nil {
		return "", err
	}
	b.s.setupChanged(context.Background(), host.ID)
	return host.ID, nil
}

// Detail is the line of the step a pool machine's set-up is on: the running one, else the
// failed one (#5366).
func (b poolsBackend) Detail(provider, workspace string) string {
	host := b.s.providers.PoolHost(provider, workspace)
	if host == nil {
		return "being made"
	}
	stored, _, err := b.s.readMachineSetup(context.Background(), host.ID)
	if err != nil || stored == nil {
		return ""
	}
	for _, step := range stored.Steps {
		switch step.State {
		case wire.MachineSetupStateFailed:
			if step.Error != "" {
				return step.Error
			}
			return step.Detail
		case wire.MachineSetupStateRunning:
			return step.Detail
		}
	}
	return ""
}

// ReadyAt is when a machine's set-up was ready, for a pool's median time to ready (#5366).
func (b poolsBackend) ReadyAt(host string) *time.Time {
	stored, _, err := b.s.readMachineSetup(context.Background(), host)
	if err != nil || stored == nil {
		return nil
	}
	return stored.Ready
}

// Version is the Agents release a new machine installs: the one this control plane is.
func (b poolsBackend) Version() string { return b.s.cfg.Version }

// Pins marks the runtime pins. Nothing pins a runtime yet, so a change cannot be seen.
func (b poolsBackend) Pins() string { return "" }

// Stopped says a pool stopped. It is in the log and in the pool's state, which clients read;
// the notice that carries it to a person's devices comes with the pool's page (#5365).
func (b poolsBackend) Stopped(person, pool, why string) {
	b.s.log(fmt.Sprintf("warm pool %s stopped: %s", pool, why))
}

// ProjectsWait is why a machine made now would not get the projects, or "" (#6890).
func (b poolsBackend) ProjectsWait(person string, projects []string) string {
	if len(projects) == 0 {
		return ""
	}
	rows, err := b.s.setupProjects(context.Background(), person, "", projects)
	if err != nil {
		return "Where its projects are could not be read"
	}
	for _, row := range rows {
		name := filepath.Base(row.Path)
		if len(row.Sources) == 0 {
			return "No machine of yours has " + row.Path + " in a project"
		}
		if !row.Sources[0].Online {
			return row.Sources[0].Name + " is offline, and it has " + name
		}
	}
	return ""
}

// setupProjects is each project folder with the person's machines that have it in a project,
// those online first. host, which gets them, and the pools' own machines are no source.
func (s *Service) setupProjects(ctx context.Context, person, host string, projects []string) ([]wire.MachineSetupProject, error) {
	out := []wire.MachineSetupProject{}
	if len(projects) == 0 {
		return out, nil
	}
	joined, err := s.joinedFolders(ctx)
	if err != nil {
		return nil, fail(storeUnavailable, "The machines' folders could not be read: %v", err)
	}
	pools, err := s.pools.all(ctx, "")
	if err != nil {
		return nil, fail(storeUnavailable, "The warm pools could not be read: %v", err)
	}
	pooled := map[string]bool{}
	for _, p := range pools {
		for _, m := range p.Machines {
			pooled[p.Provider+"\x00"+m.Workspace] = true
		}
	}
	for id := range joined {
		link := providers.LinkOf(s.records.Host(id))
		if id == host || s.records.Host(id) == nil || s.ownerOf(id) != person ||
			link != nil && pooled[link.Provider+"\x00"+link.Workspace] {
			delete(joined, id)
		}
	}
	for _, path := range projects {
		path = cleanFolder(path)
		row := wire.MachineSetupProject{Path: path, Sources: []wire.MachineSetupSource{}}
		for id, folders := range joined {
			at := slices.IndexFunc(folders, func(f wire.JoinedFolder) bool { return cleanFolder(string(f.Folder)) == path })
			h := s.records.Host(id)
			if at < 0 || h == nil {
				continue
			}
			row.Sources = append(row.Sources, wire.MachineSetupSource{Machine: wire.MachineRef(id), Name: h.Name, Online: s.providers.PoolOnline(id)})
			if row.Project == nil {
				row.Project = optional(folders[at].Project)
			}
		}
		slices.SortFunc(row.Sources, onlineFirst)
		out = append(out, row)
	}
	return out, nil
}

func onlineFirst(a, b wire.MachineSetupSource) int {
	switch {
	case a.Online && !b.Online:
		return -1
	case b.Online && !a.Online:
		return 1
	}
	return strings.Compare(a.Name+string(a.Machine), b.Name+string(b.Machine))
}

// cleanFolder is a folder's path, of a path or a file URL.
func cleanFolder(folder string) string {
	if u, err := url.Parse(folder); err == nil && u.Scheme == "file" {
		folder = u.Path
	}
	if len(folder) > 1 {
		folder = strings.TrimRight(folder, "/")
	}
	return folder
}

// setupWork is machine/setupWork: what the set-up of host has left (#6890, #6896).
func (s *Service) setupWork(ctx context.Context, host string) (wire.MachineSetupWork, error) {
	none := wire.MachineSetupWork{Done: true, Projects: []wire.MachineSetupProject{}}
	if s.pools == nil {
		return none, nil
	}
	stored, _, err := s.readMachineSetup(ctx, host)
	if err != nil {
		return none, fail(storeUnavailable, "The machine's set-up could not be read: %v", err)
	}
	if stored != nil && stored.claimed() {
		work := wire.MachineSetupWork{Pool: true, Claimed: true, Done: stored.Ready != nil, From: stored.next()}
		work.Projects, err = s.setupProjects(ctx, s.ownerOf(host), host, stored.Projects)
		return work, err
	}
	p, err := s.pools.poolOf(ctx, s.records.Host(host))
	if err != nil {
		return none, fail(storeUnavailable, "The warm pools could not be read: %v", err)
	}
	if p == nil {
		return none, nil
	}
	work := wire.MachineSetupWork{Pool: true}
	work.Projects, err = s.setupProjects(ctx, p.Owner, host, p.Projects)
	return work, err
}

// startPools starts the reconcile loop, on a control plane that has providers.
func (s *Service) startPools() {
	if s.providers == nil {
		return
	}
	s.pools = &warmPools{s: s, backend: poolsBackend{s}}
	go s.pools.every(poolEvery)
}

// poolMethod is pool/list, pool/set, pool/delete and pool/claim: a person's own pools.
func (m *Methods) poolMethod(ctx context.Context, method string, params json.RawMessage, from Caller) (any, error) {
	if m.s.pools == nil {
		return nil, m.s.providersOff()
	}
	switch method {
	case "pool/claim":
		request, err := require[wire.WarmPoolClaim](params, "WarmPoolClaim")
		if err != nil {
			return nil, err
		}
		var stop *int
		if request.StopWhenIdle != nil {
			minutes := int(*request.StopWhenIdle)
			stop = &minutes
		}
		got, err := m.s.pools.claim(ctx, from.Person, claim{Provider: request.Provider, Size: deref(request.Size),
			Region: deref(request.Region), StopWhenIdle: stop})
		if err != nil {
			return nil, err
		}
		return wire.WarmPoolClaimed{Host: optional(got.Host), Workspace: got.Workspace, Name: got.Name,
			Pool: optional(got.Pool), FromPool: got.FromPool, Note: optional(got.Note)}, nil
	case "pool/list":
		return m.s.pools.list(ctx, from.Person)
	case "pool/set":
		set, err := require[wire.WarmPoolSet](params, "WarmPoolSet")
		if err != nil {
			return nil, err
		}
		return m.s.pools.set(ctx, from.Person, set)
	default:
		request, err := require[wire.WarmPoolRequest](params, "WarmPoolRequest")
		if err != nil {
			return nil, err
		}
		if err := m.s.pools.delete(ctx, from.Person, request.ID); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	}
}
