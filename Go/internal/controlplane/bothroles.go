package controlplane

import (
	"bytes"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
)

// Both roles on one machine (#7305): a machine runs agents and the control plane only for a
// single-user control plane (#6608).

// refuseBothRoles refuses a host that would newly give a machine of this control plane the
// agents role while several people use it. A host that is enrolled with its key already
// stays, so the same join again answers as it did.
func (s *Service) refuseBothRoles(machineID string, publicKey []byte) error {
	if !s.runsControl(machineID) || !s.records.SeveralPeople() {
		return nil
	}
	for _, h := range s.records.Hosts() {
		if len(publicKey) > 0 && bytes.Equal(h.PublicKey, publicKey) {
			return nil
		}
	}
	s.log("refused a host on the control plane's own machine: several people use this control plane (#7305)")
	return fail(notPermitted, "%s", machine.BothRolesRefused)
}

// runsControl says whether that machine runs a copy of this control plane: this copy's own,
// or one whose record has the control role. A hosted copy's machine is no customer's.
func (s *Service) runsControl(machineID string) bool {
	if s.cfg.Hosted || machineID == "" {
		return false
	}
	if machineID == s.cfg.MachineID {
		return true
	}
	for _, h := range s.records.Machines() {
		if h.HasRole(records.RoleControl) && h.MachineID != nil && *h.MachineID == machineID {
			return true
		}
	}
	return false
}

// refuseMovedHello refuses the hello of a host that says, after it joined, that it is on a
// machine of this control plane: it joined as another machine, or said none. A host whose
// record already names the machine is an earlier set-up and goes on.
func (s *Service) refuseMovedHello(known *records.MachineRecord, machineID string) error {
	if known == nil || (known.MachineID != nil && *known.MachineID == machineID) {
		return nil
	}
	return s.refuseBothRoles(machineID, nil)
}

// warnBothRoles logs, at each start, a team's machine that runs agents and the control plane
// from before this was refused (Alex, 2026-11-25: it stays, and no agent stops).
func (s *Service) warnBothRoles() {
	if s.cfg.Hosted || !s.records.SeveralPeople() {
		return
	}
	for _, h := range s.records.Hosts() {
		if h.HasRole(records.RoleControl) {
			s.log("warning: machine " + h.ID + ": " + machine.BothRolesWarning)
		}
	}
}
