package providers

import (
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
)

// The providers' bounds (#6762, epic #6669). A provider is configuration an admin made, so it
// has no age, and a write over a limit is refused: nothing is evicted to make room.
const (
	// mostProviders is gate 1's limit for an organisation. One more is refused; a control
	// plane that is past it already keeps what it has, and can change it.
	mostProviders = 10
	// providerMostBytes is the largest provider record.
	providerMostBytes = 64 << 10
	// MostTombstones is how many removed providers' tombstones the sweep leaves; each goes a
	// week on in any case.
	MostTombstones = 100
)

// room refuses a new provider when there are mostProviders, with those being added.
func (p *Providers) room() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.roomLocked()
}

func (p *Providers) roomLocked() error {
	n := len(p.providers)
	for id := range p.adding {
		if _, ok := p.providers[id]; !ok {
			n++
		}
	}
	if n >= mostProviders {
		return invalid("There are %d machine providers, and %d is the most. Remove one first.", n, mostProviders)
	}
	return nil
}

// reserve keeps room for the provider while it is checked and kept, so that two added at
// once do not pass the limit together. The caller calls release when it is done.
func (p *Providers) reserve(id string) (release func(), err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.roomLocked(); err != nil {
		return nil, err
	}
	p.adding[id] = true
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.adding, id)
	}, nil
}

// encodeWithin is the record as it is kept. One over providerMostBytes is refused, unless it
// is a change that does not grow it.
func encodeWithin(record ProviderRecord, before *ProviderRecord) ([]byte, error) {
	data, err := records.Encode(record)
	if err != nil || len(data) <= providerMostBytes {
		return data, err
	}
	if before != nil {
		if was, err := records.Encode(*before); err == nil && len(data) <= len(was) {
			return data, nil
		}
	}
	return nil, invalid("A machine provider is %d KiB at most, and this one is %d KiB. Make its commands shorter.",
		providerMostBytes>>10, (len(data)+1023)>>10)
}

// tombstone is what is kept of a removed provider, until the sweep deletes it.
func (p ProviderRecord) tombstone() ProviderRecord {
	yes, now := true, records.Now()
	return ProviderRecord{ID: p.ID, Name: p.Name, Kind: p.Kind, Contract: p.Contract, Rev: p.Rev + 1,
		Forgotten: &yes, ForgottenAt: &now}
}
