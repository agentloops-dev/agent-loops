package controlplane

import (
	"errors"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// notePeopleFull tells the admins, in the audit and the log, that someone could not join
// because the people are at their limit (#7019): no admin is there to read the refusal. At
// most once an hour, because a provider's sync asks again for each user.
func (s *Service) notePeopleFull(err error, actor wire.AuditActor, who string) {
	var limit *records.PersonLimitError
	if !errors.As(err, &limit) || !limit.Full {
		return
	}
	s.peopleFullMu.Lock()
	said := time.Since(s.peopleFullSaid) < time.Hour
	if !said {
		s.peopleFullSaid = time.Now()
	}
	s.peopleFullMu.Unlock()
	if said {
		return
	}
	s.log("people: " + who + " was refused: " + limit.Words)
	s.auditAs(actor, "people/refusedFull", who+": "+limit.Words)
}
