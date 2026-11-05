package fleet

import "github.com/blackcode-ch/tmx-go/internal/model"

// projectWriterLocked is shared by project state/page/contribution writers.
// Nil callerRec is not sufficient authority: only the explicit owner flag is.
func (f *Fleet) projectWriterLocked(c Caller, p *model.Project) (*agentRec, error) {
	me, err := f.callerRecLocked(c)
	if err != nil {
		return nil, err
	}
	if c.Owner {
		return nil, nil
	}
	if me != nil && (me.a.ProjectID == p.ID || me.a.ID == p.LeadID || me == f.cardArchitectLocked()) {
		return me, nil
	}
	return nil, errf(KindForbidden, "only the project's members, assigned lead, configured architect or owner can write this project")
}
