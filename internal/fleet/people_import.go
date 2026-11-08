package fleet

import (
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/people"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// ImportReport: the plan's counts, what is in the store afterwards, and whether they match.
// Numbers only - never a name, number or address.
type ImportReport struct {
	Planned  string `json:"planned"`
	Stored   string `json:"stored"`
	Verified bool   `json:"verified"`
	Rejected int    `json:"rejected"` // people whose phone/email no longer validates (kept out, counted)
}

// ImportPeople applies a Python import plan (owner only). Idempotent: a person (by key) or a
// promise (by its Python-derived ID) that is already here is left as it is.
func (f *Fleet) ImportPeople(c Caller, plan people.Plan) (ImportReport, error) {
	if !c.Owner {
		return ImportReport{}, errf(KindForbidden, "only the owner can import people")
	}
	rep := ImportReport{Planned: plan.Counts().String()}
	keys := map[string]bool{}
	var kept people.Plan
	kept.Reminded = map[string]int64{}
	kept.SkippedMessages = plan.SkippedMessages
	for _, p := range plan.People {
		if err := cleanPerson(&p); err != nil {
			rep.Rejected++
			continue
		}
		if _, err := f.st.GetPerson(p.Key); errors.Is(err, store.ErrNotFound) {
			p.UpdatedAt = model.Now()
			if err := f.st.PutPerson(p); err != nil {
				return rep, err
			}
		} else if err != nil {
			return rep, err
		} // imported (or added) before: the entry here wins
		if sp, err := f.st.GetPerson(p.Key); err == nil {
			p.Kind = sp.Kind // verify against the kind stored now
		}
		keys[p.Key] = true
		kept.People = append(kept.People, p)
	}
	for _, pr := range plan.Promises {
		if !keys[pr.PersonKey] {
			continue
		}
		kept.Promises = append(kept.Promises, pr)
		if _, err := f.st.GetPromise(pr.ID); err == nil {
			continue // imported before: its state here wins
		} else if !errors.Is(err, store.ErrNotFound) {
			return rep, err
		}
		if err := f.st.PutPromise(pr); err != nil {
			return rep, err
		}
		if at, ok := plan.Reminded[pr.ID]; ok {
			kept.Reminded[pr.ID] = at
			if err := f.st.AddReminder(model.Reminder{ID: "p" + pr.ID + "py", FireAt: at, Target: pr.Owner, Text: "(reminded by Python tmx)",
				PromiseID: pr.ID, State: "fired", FiredAt: at, Attempts: 1, CreatedAt: at}); err != nil {
				return rep, err
			}
			continue
		}
		if pr.Status == people.Open && pr.Due != "" {
			if err := f.schedulePromise(pr); err != nil {
				return rep, err
			}
		}
	}
	for _, pr := range kept.Promises { // imported before: still expect its fired record
		if at, ok := plan.Reminded[pr.ID]; ok {
			kept.Reminded[pr.ID] = at
		}
	}
	// Verify kind by kind against what the store now holds: every person with the planned kind,
	// every promise present, every "already reminded" recorded as fired.
	var got people.Plan
	got.Reminded = map[string]int64{}
	got.SkippedMessages = plan.SkippedMessages
	for _, p := range kept.People {
		sp, err := f.st.GetPerson(p.Key)
		if err != nil {
			return rep, err
		}
		got.People = append(got.People, sp)
	}
	for _, pr := range kept.Promises {
		if _, err := f.st.GetPromise(pr.ID); err != nil {
			return rep, err
		}
		got.Promises = append(got.Promises, pr) // present; the planned status is what we report
		if _, ok := kept.Reminded[pr.ID]; ok {
			if r, err := f.st.GetReminder("p" + pr.ID + "py"); err == nil && r.State == "fired" {
				got.Reminded[pr.ID] = r.FiredAt
			}
		}
	}
	rep.Stored = got.Counts().String()
	rep.Verified = rep.Stored == kept.Counts().String() && rep.Rejected == 0
	return rep, nil
}

// cleanPerson re-validates an imported entry with today's rules.
func cleanPerson(p *model.Person) error {
	if err := people.ValidKey(p.Key); err != nil {
		return err
	}
	k, err := people.NormKind(p.Kind)
	if err != nil {
		return err
	}
	p.Kind = k
	if v := p.Channels["whatsapp"]; v != "" {
		if p.Channels["whatsapp"], err = people.Phone(v); err != nil {
			return err
		}
	}
	if v := p.Channels["email"]; v != "" {
		if p.Channels["email"], err = people.Email(v); err != nil {
			return err
		}
	}
	return nil
}
