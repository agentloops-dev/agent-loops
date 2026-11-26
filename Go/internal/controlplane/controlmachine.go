package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
)

// controlMachine is the id of the machine record of the machine this copy runs on (#6609),
// which a copy record names. "" before takeUp has found or made it.

func (s *Service) controlMachineID() string {
	if p := s.controlMachine.Load(); p != nil {
		return *p
	}
	return ""
}

// controlMachineHostID is the id of a control-only machine record: mac, as the home host
// of a Mac is called, so a host that joins from it takes the record over; else control.
func controlMachineHostID() string {
	if runtime.GOOS == "darwin" {
		return "mac"
	}
	return "control"
}

// ensureControlMachine gives the machine this copy runs on a machine record with the control
// role (#6609): a record that has none gets roles ["control"], one that runs a host gets
// ["agents", "control"], so a machine that runs the control plane shows as a machine whether
// or not it runs agents. A hosted copy is no customer's machine and has none.
func (s *Service) ensureControlMachine(ctx context.Context) error {
	if s.cfg.Hosted || s.cfg.MachineID == "" {
		return nil
	}
	for _, h := range s.records.Machines() {
		if h.MachineID == nil || *h.MachineID != s.cfg.MachineID {
			continue
		}
		s.controlMachine.Store(&h.ID)
		if h.HasRole(records.RoleControl) {
			return nil
		}
		h.Roles = records.NormalRoles(append(records.RolesOf(h), records.RoleControl))
		return s.records.SaveHost(ctx, h)
	}
	mid := s.cfg.MachineID
	record := records.MachineRecord{ID: controlMachineHostID(), Name: s.cfg.Name, Platform: controlPlatform(),
		Version: s.cfg.Version, MachineID: &mid, Roles: []string{records.RoleControl}}
	if s.records.Host(record.ID) != nil {
		// A host on another machine took the id: this one has its own.
		record.ID = records.NewHostID()
	}
	err := s.records.AddHost(ctx, record)
	if errors.Is(err, records.ErrHostTaken) {
		record.ID = records.NewHostID()
		err = s.records.AddHost(ctx, record)
	}
	if err != nil {
		return err
	}
	s.controlMachine.Store(&record.ID)
	return nil
}

func controlPlatform() string {
	if runtime.GOOS == "darwin" {
		return "macOS"
	}
	return "Linux"
}

// takeOverControlMachine is a host that joins from the machine the control plane runs on,
// when that machine's record has only the control role: the record becomes the host's, with
// both roles, so the machine stays one record. It says whether it did.
func (s *Service) takeOverControlMachine(ctx context.Context, record records.MachineRecord) (bool, error) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	old := s.records.Host(record.ID)
	if old == nil || !old.ControlOnly() || len(old.PublicKey) > 0 {
		return false, nil
	}
	record.Roles = []string{records.RoleAgents, records.RoleControl}
	record.Rev = old.Rev
	return true, s.records.SaveHost(ctx, record)
}

// mergeControlMachine is for a host that enrolled under an id of its own on the machine the
// control plane runs on: its record gets both roles and the control-only one is dropped, so
// the machine is one record (#6609).
func (s *Service) mergeControlMachine(ctx context.Context, host string, record records.MachineRecord) {
	// Only the home host (mac) takes the machine's record: one machine, one record.
	mine := s.controlMachineID()
	if host != "mac" || mine == "" || mine == host || record.MachineID == nil || s.cfg.MachineID == "" || *record.MachineID != s.cfg.MachineID {
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	only := s.records.Host(mine)
	if only == nil || !only.ControlOnly() {
		return
	}
	if err := s.records.ChangeHost(ctx, host, func(h *records.MachineRecord) bool {
		h.Roles = []string{records.RoleAgents, records.RoleControl}
		return true
	}); err != nil {
		s.log("could not give " + host + " the control role: " + err.Error())
		return
	}
	if _, err := s.records.RemoveHost(ctx, mine); err != nil {
		s.log("could not drop the control machine record " + mine + ": " + err.Error())
		return
	}
	s.controlMachine.Store(&host)
}

// macFree says the id mac is free for the home host: no record has it, or the control
// plane's own machine record of that id has no agents yet (#6609).
func (s *Service) macFree() bool {
	h := s.records.Host("mac")
	return h == nil || (h.ControlOnly() && len(h.PublicKey) == 0)
}

// runsCopy says whether a copy of this control plane runs on the machine (#7346): this
// copy, or another whose live copy record names it.
func (s *Service) runsCopy(ctx context.Context, host string) (bool, error) {
	if host != "" && s.controlMachineID() == host {
		return true, nil
	}
	listed, err := s.cfg.Store.List(ctx, copiesPrefix)
	if err != nil {
		return false, err
	}
	for _, e := range listed {
		object, err := s.cfg.Store.Get(ctx, e.Key)
		if err != nil {
			return false, err
		}
		var c copyRecord
		if object == nil || json.Unmarshal(object.Data, &c) != nil || c.ID == "" || c.PeerURL == "" {
			continue
		}
		if c.Machine == host && time.Since(c.Heartbeat.Time) <= copyGone {
			return true, nil
		}
	}
	return false, nil
}
