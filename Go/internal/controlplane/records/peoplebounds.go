package records

import (
	"fmt"

	"github.com/agentloops-dev/agent-loops/Go/internal/machine"
	"github.com/agentloops-dev/agent-loops/Go/internal/sizewords"
)

// The people's bounds (#7019, epic #6669). A person's record is never evicted: a new person
// over a limit is refused, and so is a longer name, sign-in or SCIM user. What the control
// plane itself notes (a role, a suspension, a removal, the first sign-in of a person SCIM
// made, the local owner's key) is never refused, so a person who is here signs in, and can
// be suspended or removed, at the limit too.
const (
	// PersonMostBytes is the largest person record.
	PersonMostBytes = 16 << 10
	// PeopleMostBytes is the most an organisation's people come to (Alex, 2026-11-24).
	// Removed people, gone in 90 days, may come to as much again: a removal makes room at once.
	PeopleMostBytes = 1 << 20
	// invitedBytes is more than any invite's new person takes.
	invitedBytes = 1 << 10
)

// PersonLimitError is a write refused at a limit, in the words a person reads. Full says
// that the people are at their limit, not that one record is too large.
type PersonLimitError struct {
	Words string
	Full  bool
}

func (e *PersonLimitError) Error() string { return e.Words }

// PersonRoom refuses when there is no room for one more invited person, or no second person
// may join.
func (r *Records) PersonRoom() error {
	if err := r.secondPerson(""); err != nil {
		return err
	}
	return r.peopleWithin(invitedBytes)
}

// secondPerson refuses the person id who would make a single-user control plane a team while
// a machine of it runs agents too (#7305; Alex, 2026-11-25). A set-up that is a team with
// both roles already stays as it is, so its third person is not refused.
func (r *Records) secondPerson(id string) error {
	if r.otherPeople(id) != 1 {
		return nil
	}
	for _, h := range r.Machines() {
		if h.HasRole(RoleControl) && h.HasRole(RoleAgents) {
			return &PersonLimitError{Full: true, Words: machine.SecondPersonRefused}
		}
	}
	return nil
}

// otherPeople counts the people but for id: no bots, no removed people.
func (r *Records) otherPeople(id string) int {
	n := 0
	for _, p := range r.People() {
		if p.ID != id && !p.IsRemoved() && !p.IsBot() {
			n++
		}
	}
	return n
}

// peopleWithin refuses when the people this copy holds and grow more bytes are over
// PeopleMostBytes. It lists nothing: a first sync makes a person a call (#2430).
func (r *Records) peopleWithin(grow int) error {
	live, all := grow, grow
	r.mu.Lock()
	for id, size := range r.peopleBytes {
		all += size
		if h, ok := r.people[id]; !ok || !h.record.IsRemoved() {
			live += size
		}
	}
	r.mu.Unlock()
	if live > PeopleMostBytes {
		return &PersonLimitError{Full: true, Words: "This control plane keeps as many people as it can. " +
			"An admin must remove a person who is not needed. Then try again."}
	}
	if all > 2*PeopleMostBytes {
		return &PersonLimitError{Full: true, Words: "Too many people were removed from this control plane in the last 90 days. " +
			"A new person can join when those records are gone."}
	}
	return nil
}

// personWithin refuses a person, new when before is nil, over either limit.
func (r *Records) personWithin(before *Person, after Person) error {
	size, err := personSize(after)
	if err != nil {
		return err
	}
	if !after.IsBot() && !after.IsRemoved() && (before == nil || before.IsRemoved()) {
		if err := r.secondPerson(after.ID); err != nil {
			return err
		}
	}
	grow := size
	if before != nil {
		// A person SCIM made who signs in for the first time.
		if after.IsRemoved() || before.Provisioned && !after.Provisioned {
			return nil
		}
		was, err := personSize(*before)
		if err != nil {
			return err
		}
		had, err := personSaid(*before)
		if err != nil {
			return err
		}
		has, err := personSaid(after)
		if err != nil || has <= had || size <= was {
			return err
		}
		grow = size - was
	}
	if size > PersonMostBytes {
		return &PersonLimitError{Words: fmt.Sprintf("This person's details are too large to keep: %s, and the most is %s. Remove some of their sign-ins or logins.", sizewords.Int(size), sizewords.Int(PersonMostBytes))}
	}
	return r.peopleWithin(grow)
}

// personSaid is the bytes of what a person or their provider says of them, without what
// the control plane notes.
func personSaid(p Person) (int, error) {
	said := Person{Name: p.Name, SignIns: p.SignIns, LastNames: p.LastNames, GitHubLogin: p.GitHubLogin, GitLabLogin: p.GitLabLogin,
		LinearLogin: p.LinearLogin, JiraLogin: p.JiraLogin, ForgejoLogin: p.ForgejoLogin, BitbucketLogin: p.BitbucketLogin,
		AzureDevOpsLogin: p.AzureDevOpsLogin}
	if p.SCIM != nil {
		scim := *p.SCIM
		scim.Active, scim.Deleted, scim.Created, scim.Changed = true, false, nil, nil
		said.SCIM = &scim
	}
	return personSize(said)
}

func personSize(p Person) (int, error) {
	data, err := Encode(p)
	return len(data), err
}
