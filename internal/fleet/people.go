package fleet

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/people"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// People, Clients, Promises and Reminders (docs/PEOPLE_PROMISES.md). The owner sees and edits
// everything; an agent sees people without their phone/email, may open and close promises, and
// may set reminders for itself only. Nothing here logs a name, number or address.

const (
	maxPromiseTitle = 200
	maxPromiseText  = 4000
	maxReminderText = 2000
)

// PersonInput is an add/edit. Channels: a value "" removes that channel.
type PersonInput struct {
	Key       string            `json:"key"`
	Name      *string           `json:"name,omitempty"`
	Role      *string           `json:"role,omitempty"`
	Lang      *string           `json:"lang,omitempty"`
	Kind      *string           `json:"kind,omitempty"`
	Preferred *string           `json:"preferred,omitempty"`
	Notes     *string           `json:"notes,omitempty"`
	Aliases   []string          `json:"aliases,omitempty"`
	Channels  map[string]string `json:"channels,omitempty"`
	// Projects the person shares with the owner (authority.go: a team member's requests within
	// them proceed without asking). nil = unchanged; empty = none.
	Projects []string `json:"projects,omitempty"`
}

func storeErr(err error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return errf(KindNotFound, "no %s", what)
	}
	return err
}

// People lists everyone; kind "" = all. Agents get the public view.
func (f *Fleet) People(c Caller, kind string) (any, error) {
	all, err := f.st.ListPeople()
	if err != nil {
		return nil, err
	}
	var list []model.Person
	for _, p := range all {
		if kind == "" || p.Kind == kind {
			list = append(list, p)
		}
	}
	if list == nil {
		list = []model.Person{}
	}
	if c.Owner {
		return list, nil
	}
	open, err := f.st.ListPromises("", people.Open)
	if err != nil {
		return nil, err
	}
	n := map[string]int{}
	for _, pr := range open {
		n[pr.PersonKey]++
	}
	out := make([]model.PublicPerson, 0, len(list))
	for _, p := range list {
		pp := p.Public()
		pp.OpenPromises = n[p.Key]
		out = append(out, pp)
	}
	return out, nil
}

// Person is one entry (owner: full, agent: public).
func (f *Fleet) Person(c Caller, key string) (any, error) {
	p, err := f.st.GetPerson(strings.ToLower(key))
	if err != nil {
		return nil, storeErr(err, "such person")
	}
	if c.Owner {
		return p, nil
	}
	return p.Public(), nil
}

// PutPerson adds or edits a person (owner only).
func (f *Fleet) PutPerson(c Caller, in PersonInput) (model.Person, error) {
	if !c.Owner {
		return model.Person{}, errf(KindForbidden, "only the owner can edit people")
	}
	key := strings.ToLower(strings.TrimSpace(in.Key))
	if err := people.ValidKey(key); err != nil {
		return model.Person{}, errf(KindInvalid, "%v", err)
	}
	all, err := f.st.ListPeople()
	if err != nil {
		return model.Person{}, err
	}
	p := model.Person{Key: key, Kind: "team"}
	for _, e := range all {
		if e.Key == key {
			p = e
		}
	}
	set := func(dst *string, v *string, max int) {
		if v != nil {
			s := strings.TrimSpace(*v)
			*dst = limitRunes(s, max)
		}
	}
	set(&p.Name, in.Name, 120)
	set(&p.Role, in.Role, 120)
	set(&p.Lang, in.Lang, 16)
	set(&p.Preferred, in.Preferred, 16)
	set(&p.Notes, in.Notes, 4000)
	if in.Kind != nil {
		k, err := people.NormKind(*in.Kind)
		if err != nil {
			return model.Person{}, errf(KindInvalid, "%v", err)
		}
		p.Kind = k
	}
	if in.Aliases != nil {
		var aliases []string
		for _, a := range in.Aliases {
			a = strings.ToLower(strings.TrimSpace(a))
			if a == "" || a == key {
				continue
			}
			if err := people.ValidKey(a); err != nil {
				return model.Person{}, errf(KindInvalid, "alias %q: %v", a, err)
			}
			for _, e := range all {
				if e.Key == key {
					continue
				}
				if e.Key == a || contains(e.Aliases, a) {
					return model.Person{}, errf(KindConflict, "%q is already used by another person", a)
				}
			}
			aliases = append(aliases, a)
		}
		p.Aliases = aliases
	}
	if p.Channels == nil {
		p.Channels = map[string]string{}
	}
	for ch, v := range in.Channels {
		v = strings.TrimSpace(v)
		if v == "" {
			delete(p.Channels, ch)
			continue
		}
		switch ch {
		case "whatsapp":
			v, err = people.Phone(v)
		case "email":
			v, err = people.Email(v)
		case "github":
			if len(v) > 64 {
				err = errors.New("github name too long")
			}
		case "tmx": // the peer ID of their own tmx install (Contacts shows it), for chats between installs
			if len(v) < 8 || len(v) > 128 || strings.ContainsAny(v, " \t\r\n") {
				err = errors.New("tmx must be the peer ID of their install")
			}
		default:
			err = errors.New("channel must be whatsapp, email, github or tmx")
		}
		if err != nil {
			return model.Person{}, errf(KindInvalid, "%s: %v", ch, err)
		}
		p.Channels[ch] = v
	}
	if in.Projects != nil {
		var projects []string
		for _, name := range in.Projects {
			name = limitRunes(strings.TrimSpace(name), 80)
			if name == "" || contains(projects, name) {
				continue
			}
			if len(projects) == 32 {
				return model.Person{}, errf(KindInvalid, "at most 32 projects per person")
			}
			projects = append(projects, name)
		}
		p.Projects = projects
	}
	p.UpdatedAt = model.Now()
	return p, f.st.PutPerson(p)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (f *Fleet) DeletePerson(c Caller, key string) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner can remove people")
	}
	return storeErr(f.st.DeletePerson(strings.ToLower(key)), "such person")
}

// PromiseInput opens or edits a promise.
type PromiseInput struct {
	PersonKey string  `json:"person_key"`
	Title     *string `json:"title,omitempty"`
	Text      *string `json:"text,omitempty"`
	Owner     *string `json:"owner,omitempty"`
	Due       *string `json:"due,omitempty"`
	Status    *string `json:"status,omitempty"`
}

// Promises: the owner sees all, an agent only the promises it opened or is assigned, anyone else none.
func (f *Fleet) Promises(c Caller, person, status string) ([]model.Promise, error) {
	if !c.Owner && c.AgentID == "" {
		return nil, errf(KindForbidden, "not allowed")
	}
	all, err := f.st.ListPromises(strings.ToLower(person), status)
	if err != nil || c.Owner {
		return all, err
	}
	out := []model.Promise{}
	for _, p := range all {
		if f.promiseIsMine(c.AgentID, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// isAgent reports whether ref (an agent name or ID, as stored in Promise.Owner) is agent id.
func (f *Fleet) isAgent(id, ref string) bool {
	if ref == "" || id == "" {
		return false
	}
	if ref == id {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, err := f.resolveLocked(ref); err == nil {
		return r.a.ID == id
	}
	return false
}

// promiseIsAssigned: the agent is the promise's accountable owner (its reminder goes to it).
func (f *Fleet) promiseIsAssigned(id string, p model.Promise) bool { return f.isAgent(id, p.Owner) }

// promiseIsMine: the agent opened the promise or is assigned it.
func (f *Fleet) promiseIsMine(id string, p model.Promise) bool {
	return (p.CreatedBy != "" && p.CreatedBy == id) || f.promiseIsAssigned(id, p)
}

// PutPromise creates (id "") or edits a promise and keeps its due-date reminder in step.
func (f *Fleet) PutPromise(c Caller, id string, in PromiseInput) (model.Promise, error) {
	if !c.Owner && c.AgentID == "" {
		return model.Promise{}, errf(KindForbidden, "not allowed")
	}
	var p model.Promise
	if id == "" {
		key := strings.ToLower(strings.TrimSpace(in.PersonKey))
		if _, err := f.st.GetPerson(key); err != nil {
			return p, storeErr(err, "such person")
		}
		p = model.Promise{ID: model.NewID(), PersonKey: key, Status: people.Open, CreatedAt: model.Now()}
		if !c.Owner {
			p.Owner = f.nameOf(c.AgentID)
			p.CreatedBy = c.AgentID
		}
	} else {
		var err error
		if p, err = f.st.GetPromise(id); err != nil {
			return p, storeErr(err, "such promise")
		}
		if !c.Owner && !f.promiseIsMine(c.AgentID, p) {
			// Same answer as a missing one: an agent learns nothing about others' promises.
			return model.Promise{}, errf(KindNotFound, "no such promise")
		}
	}
	// Owner decides whose inbox the due reminder lands in: only the human or the promise's own
	// (assigned) agent may move it; a new promise from an agent is always its own.
	if in.Owner != nil && !c.Owner {
		to := strings.TrimSpace(*in.Owner)
		if !f.isAgent(c.AgentID, to) && (id == "" || !f.promiseIsAssigned(c.AgentID, p)) {
			return model.Promise{}, errf(KindForbidden, "only the owner or the promise's own agent can reassign it")
		}
	}
	if in.Title != nil {
		p.Title = limitRunes(strings.TrimSpace(*in.Title), maxPromiseTitle)
	}
	if in.Text != nil {
		p.Text = limitRunes(*in.Text, maxPromiseText)
	}
	if in.Owner != nil {
		p.Owner = limitRunes(strings.TrimSpace(*in.Owner), 80)
	}
	oldDue := p.Due
	if in.Due != nil {
		d, err := people.Due(*in.Due)
		if err != nil {
			return p, errf(KindInvalid, "%v", err)
		}
		p.Due = d
	}
	oldStatus := p.Status
	if in.Status != nil {
		if !people.ValidStatus(*in.Status) {
			return p, errf(KindInvalid, "status must be open, done or dropped")
		}
		p.Status = *in.Status
	}
	if p.Title == "" {
		return p, errf(KindInvalid, "a promise needs a title")
	}
	p.UpdatedAt = model.Now()
	if err := f.st.PutPromise(p); err != nil {
		return p, err
	}
	if p.Status != people.Open || p.Due != oldDue || oldStatus != p.Status || id == "" {
		if err := f.st.CancelPromiseReminders(p.ID); err != nil {
			return p, err
		}
		if p.Status == people.Open && p.Due != "" {
			if err := f.schedulePromise(p); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}

// schedulePromise sets the due-date reminder (09:00 local) for the promise's owner (the head
// when the owner is not an agent here). An already-past due date fires on the next tick.
func (f *Fleet) schedulePromise(p model.Promise) error {
	at, err := people.DueFireAt(p.Due, time.Local)
	if err != nil {
		return err
	}
	target := f.HeadID()
	if p.Owner != "" {
		f.mu.Lock()
		if r, err := f.resolveLocked(p.Owner); err == nil {
			target = r.a.ID
		}
		f.mu.Unlock()
	}
	if target == "" {
		return nil
	}
	who := p.PersonKey
	if pp, err := f.st.GetPerson(p.PersonKey); err == nil && pp.Name != "" {
		who = pp.Name
	}
	text := "Promise due today (" + p.Due + ") for " + who + ": " + p.Title + "\nClose it on the Promises page or with promise id " + p.ID + "."
	r := model.Reminder{ID: "p" + p.ID + strings.ReplaceAll(p.Due, "-", ""), FireAt: at.UnixMilli(), Target: target, Text: text,
		PromiseID: p.ID, State: "pending", CreatedAt: model.Now()}
	if err := f.st.AddReminder(r); err != nil {
		return err
	}
	f.wakeReminders()
	return nil
}

// limitRunes cuts s to at most n runes (never inside a UTF-8 sequence).
func limitRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (f *Fleet) nameOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.agents[id]; ok {
		return r.a.Name
	}
	return id
}

// ReminderInput: At (unix ms) or In (seconds from now). Target defaults to the caller; an agent may only remind itself.
type ReminderInput struct {
	At     int64  `json:"at,omitempty"`
	In     int64  `json:"in,omitempty"`
	Target string `json:"target,omitempty"`
	Text   string `json:"text"`
}

func (f *Fleet) AddReminder(c Caller, in ReminderInput) (model.Reminder, error) {
	var target string
	switch {
	case c.Owner:
		if in.Target == "" {
			target = f.HeadID()
		} else {
			f.mu.Lock()
			r, err := f.resolveLocked(in.Target)
			f.mu.Unlock()
			if err != nil {
				return model.Reminder{}, err
			}
			target = r.a.ID
		}
	case c.AgentID != "":
		target = c.AgentID
		if in.Target != "" && f.nameOf(c.AgentID) != in.Target && in.Target != c.AgentID {
			return model.Reminder{}, errf(KindForbidden, "an agent can only set reminders for itself")
		}
	default:
		return model.Reminder{}, errf(KindForbidden, "not allowed")
	}
	text := strings.TrimSpace(in.Text)
	if text == "" || len(text) > maxReminderText {
		return model.Reminder{}, errf(KindInvalid, "text must be 1-%d bytes", maxReminderText)
	}
	at := in.At
	if at == 0 {
		if in.In <= 0 {
			return model.Reminder{}, errf(KindInvalid, "give at (unix ms) or in (seconds)")
		}
		at = model.Now() + in.In*1000
	}
	if at > model.Now()+5*365*24*3600*1000 {
		return model.Reminder{}, errf(KindInvalid, "a reminder can be at most 5 years ahead")
	}
	r := model.Reminder{ID: model.NewID(), FireAt: at, Target: target, Text: text, State: "pending", CreatedAt: model.Now()}
	if err := f.st.AddReminder(r); err != nil {
		return r, err
	}
	f.wakeReminders()
	return r, nil
}

// Reminders: the owner sees all, an agent its own.
func (f *Fleet) Reminders(c Caller, state string) ([]model.Reminder, error) {
	target := ""
	if !c.Owner {
		if c.AgentID == "" {
			return nil, errf(KindForbidden, "not allowed")
		}
		target = c.AgentID
	}
	list, err := f.st.ListReminders(state, target)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].FireAt < list[j].FireAt })
	return list, nil
}

func (f *Fleet) CancelReminder(c Caller, id string) error {
	r, err := f.st.GetReminder(id)
	if err != nil {
		return storeErr(err, "such reminder")
	}
	if !c.Owner && (c.AgentID == "" || r.Target != c.AgentID) {
		return errf(KindForbidden, "not your reminder")
	}
	ok, err := f.st.CancelReminder(id)
	if err != nil {
		return err
	}
	if !ok {
		return errf(KindConflict, "the reminder is already %s", r.State)
	}
	return nil
}

// deliverReminder queues the reminder as mail. The message ID is derived from the reminder ID,
// so a second attempt (after a crash between delivering and marking) is detected, not doubled.
func (f *Fleet) deliverReminder(rm model.Reminder) error {
	mid := "rm" + rm.ID
	if _, err := f.st.GetMessage(mid); err == nil {
		return people.ErrAlreadyDelivered
	}
	f.mu.Lock()
	r, ok := f.agents[rm.Target]
	if !ok { // an imported reminder names its agent
		if rr, err := f.resolveLocked(rm.Target); err == nil {
			r, ok, rm.Target = rr, true, rr.a.ID
		}
	}
	if !ok {
		f.mu.Unlock()
		// The agent is gone: give it to the head rather than lose it.
		head := f.HeadID()
		if head == "" || head == rm.Target {
			return errf(KindNotFound, "no agent for the reminder")
		}
		rm.Target = head
		return f.deliverReminder(rm)
	}
	m := model.Message{ID: mid, From: model.ReminderParty, To: rm.Target, Body: rm.Text, State: model.MsgQueued, CreatedAt: model.Now()}
	if err := f.st.PutMessage(m); err != nil {
		f.mu.Unlock()
		return err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: rm.Target, Data: m})
	f.refreshQueuedLocked(r)
	f.mu.Unlock()
	f.tryDeliver(rm.Target)
	return nil
}

func (f *Fleet) reminderScheduler() *people.Scheduler {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reminders == nil {
		f.reminders = &people.Scheduler{St: f.st, Deliver: f.deliverReminder,
			OnError: func(id string, err error) { f.log.Warn("reminder", "id", id, "err", err) }}
	}
	return f.reminders
}

func (f *Fleet) wakeReminders() { f.reminderScheduler().Wake() }

// FireDueReminders runs one scheduler tick now (tests, and the start-up catch-up).
func (f *Fleet) FireDueReminders() (int, error) { return f.reminderScheduler().Tick() }

func (f *Fleet) startReminderLoop() {
	s := f.reminderScheduler()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.reminderLoopStarted {
		return
	}
	f.reminderLoopStarted = true
	f.goTracked(func() { s.Run(f.ctx) })
}
