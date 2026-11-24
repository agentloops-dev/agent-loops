package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// What a warm pool needs of the providers (#5363, epic #5353): to make a machine for a
// person as New Machine does, and to delete one, joined or not, with no backup first: a pool
// machine holds nothing of anyone's.

// PoolMake makes a machine called name on a provider for a person, as providers/create does,
// with the pool's size and region laid over what New Machine's form shows there. It returns the
// provider's id of the workspace it made; the machine joins on its own first boot.
func (p *Providers) PoolMake(person, provider, name, size, region string) (string, error) {
	options := p.formValues(person, provider)
	if size = strings.TrimSpace(size); size != "" {
		options["size"] = size
	}
	if region = strings.TrimSpace(region); region != "" {
		options["region"] = region
	}
	id := wire.ProviderID(provider)
	created, err := p.create(ProviderCreate{ID: &id, Name: &name, Options: jsonValue(options)}, "warm pool", &Caller{Person: person})
	if err != nil {
		return "", err
	}
	made, ok := created.(ProviderCreated)
	if !ok {
		return "", fail(CodeProviderFailed, "%s made no machine.", provider)
	}
	return made.Workspace.ID, nil
}

// PoolDelete deletes a pool's machine: the machine and its host record when it has joined,
// else the provider's workspace alone.
func (p *Providers) PoolDelete(person, provider, workspace string) error {
	for _, h := range p.records.Hosts() {
		if link := LinkOf(&h); link != nil && link.Provider == provider && link.Workspace == workspace {
			return p.removeWorkspace(h.ID, *link, fmt.Sprintf("%s (%s/%s)", h.Name, provider, workspace), "warm pool", Caller{Person: person})
		}
	}
	return p.deleteWorkspace(provider, workspace, "warm pool")
}

// PoolHost is the host record of the machine in a workspace, once it has joined; nil before.
func (p *Providers) PoolHost(provider, workspace string) *records.MachineRecord {
	for _, h := range p.records.Hosts() {
		if link := LinkOf(&h); link != nil && link.Provider == provider && link.Workspace == workspace {
			h := h
			return &h
		}
	}
	return nil
}

// HasProvider says a provider is here to make machines.
func (p *Providers) HasProvider(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.providers[id]
	return ok
}

// PoolListed says the provider still lists the workspace (#5365): a claim takes only a machine
// the provider has.
func (p *Providers) PoolListed(provider, workspace string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.workspaceLocked(WorkspaceLink{Provider: provider, Workspace: workspace}) != nil
}

// PoolOnline says the host answers: its uplink to the control plane is up.
func (p *Providers) PoolOnline(host string) bool { return p.router.Online(host) }

// PoolBind makes a pool machine the person's (#5365): the host's owner, and the person's
// Stop when idle on its link (minutes; nil keeps the link's, 0 turns it off).
func (p *Providers) PoolBind(host, person string, stopWhenIdle *int) error {
	record := p.records.Host(host)
	if record == nil {
		return fmt.Errorf("no machine %s", host)
	}
	record.Owner = &person
	if link := LinkOf(record); link != nil && stopWhenIdle != nil {
		if *stopWhenIdle > 0 {
			link.StopWhenIdleMinutes = stopWhenIdle
		} else {
			link.StopWhenIdleMinutes = nil
		}
		setLink(record, link)
	}
	return p.records.SaveHost(context.Background(), *record)
}
