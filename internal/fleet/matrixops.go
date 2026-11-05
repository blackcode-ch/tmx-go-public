package fleet

// The Matrix (DESIGN.md "Matrix"), M1b: opening intent threads, moving them through their
// lifecycle (each change an event), and the lists behind "mine" and "see by".

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

const (
	maxIntentText = 2000
	maxStory      = 600
	// cardIntent is how much of the owner's words a list card carries (the panel has them all).
	cardIntent = 160
)

// NewThreadRequest opens an intent thread: one of the owner's requests (the Matrix's "new").
type NewThreadRequest struct {
	Title        string `json:"title"`
	IntentText   string `json:"intent_text,omitempty"`   // the owner's own words, verbatim
	IntentSource string `json:"intent_source,omitempty"` // where and when they said it
	Story        string `json:"story,omitempty"`         // "As the owner, ... I want ... so that ..."
	Parent       string `json:"parent,omitempty"`        // a project's or thread's id or title
	Owner        string `json:"owner,omitempty"`         // the agent accountable (name or id); default: the caller
	Lifecycle    string `json:"lifecycle,omitempty"`     // task | ongoing
}

func validLifecycle(life string) bool {
	return life == "" || life == model.LifecycleTask || life == model.LifecycleOngoing
}

// NewThread opens an intent thread (owner, head, or a lead under its own project) and records
// its start as an event; its owner joins it.
func (f *Fleet) NewThread(c Caller, req NewThreadRequest) (model.Thread, error) {
	title, err := cleanTitle(req.Title)
	if err != nil {
		return model.Thread{}, err
	}
	intent, source, story := strings.TrimSpace(req.IntentText), strings.TrimSpace(req.IntentSource), strings.TrimSpace(req.Story)
	life := strings.ToLower(strings.TrimSpace(req.Lifecycle))
	switch {
	case utf8.RuneCountInString(intent) > maxIntentText, utf8.RuneCountInString(story) > maxStory,
		utf8.RuneCountInString(source) > maxLinkWhy:
		return model.Thread{}, errf(KindInvalid, "too long: attach the details as a document")
	case !validLifecycle(life):
		return model.Thread{}, errf(KindInvalid, "lifecycle must be task or ongoing")
	case companion.Scrub(title) != title || companion.Scrub(intent) != intent || companion.Scrub(story) != story ||
		companion.Scrub(source) != source:
		return model.Thread{}, errf(KindInvalid, "this looks like it contains a secret (a key, token or password); threads never hold secrets")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.callerRecLocked(c)
	if err != nil {
		return model.Thread{}, err
	}
	var parent model.Thread
	if strings.TrimSpace(req.Parent) != "" {
		if parent, err = f.threadByLocked(req.Parent); err != nil {
			return model.Thread{}, err
		}
	}
	if r != nil && !isHead(r) {
		if r.a.Level != model.LevelLead || r.a.ProjectID == "" {
			return model.Thread{}, errf(KindForbidden, "only the owner, the head and leads open threads; ask your lead")
		}
		if parent.ID == "" {
			if parent, err = f.st.GetThread(f.projectThreadLocked(r.a.ProjectID)); err != nil {
				return model.Thread{}, errf(KindNotFound, "your project has no thread")
			}
		} else if !f.leadsThreadLocked(r, parent) {
			return model.Thread{}, errf(KindForbidden, "a lead opens threads under its own project")
		}
	}
	if o, ok := f.findThreadLocked(title); ok && !o.Archived {
		return model.Thread{}, errf(KindConflict, "a thread %q already exists", o.Title)
	}
	owner := r
	if strings.TrimSpace(req.Owner) != "" {
		if owner, err = f.resolveLocked(req.Owner); err != nil {
			return model.Thread{}, errf(KindInvalid, "owner: %s", err.Error())
		}
		// Owning a thread puts the agent on it and lets it change its state.
		if r != nil && !isHead(r) && owner.a.ProjectID != r.a.ProjectID {
			return model.Thread{}, errf(KindForbidden, "a lead names an owner from its own project")
		}
	}
	now := model.Now()
	t := model.Thread{ID: "i-" + model.NewID(), Title: title, Kind: model.ThreadIntent, ParentID: parent.ID,
		ProjectID: parent.ProjectID, Status: model.ThreadActive, Lifecycle: life, Story: story, IntentText: intent,
		IntentSource: source, CreatedAt: now, UpdatedAt: now}
	if owner != nil {
		t.Owner = owner.a.ID
	}
	if err := f.st.PutThread(t); err != nil {
		return model.Thread{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvThreadUpdated, ProjectID: t.ProjectID, Data: t})
	f.eventLocked(t, r, model.EventStart, "Opened: "+title)
	if owner != nil {
		f.linkLaunchLocked(linkerName(r), t.ID, owner.a)
	}
	return t, nil
}

// StateRequest moves a thread through its lifecycle.
type StateRequest struct {
	Status    string `json:"status,omitempty"`    // active | paused | waiting_on_owner | blocked | done
	Lifecycle string `json:"lifecycle,omitempty"` // task | ongoing
	Why       string `json:"why,omitempty"`       // one line, recorded with the event
}

// stateEvent is the event a status change records.
var stateEvent = map[string]string{
	model.ThreadActive:  model.EventStart,
	model.ThreadPaused:  model.EventPause,
	model.ThreadWaiting: model.EventAsk,
	model.ThreadBlocked: model.EventBlocker,
	model.ThreadDone:    model.EventDone,
}

// SetState changes a thread's status or lifecycle (the owner, the head, the thread's owner, or
// its project's lead); every change is an event, with who and why.
func (f *Fleet) SetState(c Caller, idOrTitle string, req StateRequest) (model.Thread, error) {
	status := strings.ToLower(strings.TrimSpace(req.Status))
	life := strings.ToLower(strings.TrimSpace(req.Lifecycle))
	why := strings.TrimSpace(req.Why)
	switch {
	case status == "" && life == "":
		return model.Thread{}, errf(KindInvalid, "say the new status or lifecycle")
	case status != "" && !slices.Contains(model.ThreadStatuses, status):
		return model.Thread{}, errf(KindInvalid, "status must be one of %s", strings.Join(model.ThreadStatuses, ", "))
	case !validLifecycle(life):
		return model.Thread{}, errf(KindInvalid, "lifecycle must be task or ongoing")
	case utf8.RuneCountInString(why) > maxLinkWhy:
		return model.Thread{}, errf(KindInvalid, "keep why to one line")
	case companion.Scrub(why) != why:
		return model.Thread{}, errf(KindInvalid, "this looks like it contains a secret (a key, token or password)")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	t, r, err := f.matrixThreadLocked(c, idOrTitle)
	if err != nil {
		return model.Thread{}, err
	}
	if r != nil && !isHead(r) && t.Owner != r.a.ID && !f.leadsThreadLocked(r, t) {
		return model.Thread{}, errf(KindForbidden, "only the thread's owner, its project's lead, the head and the owner change its state")
	}
	if t.Kind == model.ThreadGeneral && status == model.ThreadDone {
		return model.Thread{}, errf(KindInvalid, "General is never done")
	}
	var said []string
	if status != "" && status != threadStatus(t) {
		t.Status = status
		said = append(said, "status "+status)
	}
	if life != "" && life != threadLifecycle(t) {
		t.Lifecycle = life
		said = append(said, "lifecycle "+life)
	}
	if len(said) == 0 {
		return t, nil
	}
	t.UpdatedAt = model.Now()
	if err := f.st.PutThread(t); err != nil {
		return model.Thread{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvThreadUpdated, ProjectID: t.ProjectID, Data: t})
	kind := model.LogInfo
	if status != "" && t.Status == status {
		kind = stateEvent[status]
	}
	text := "Now " + strings.Join(said, ", ")
	if why != "" {
		text += ": " + why
	}
	f.eventLocked(t, r, kind, text)
	return t, nil
}

func threadStatus(t model.Thread) string {
	if t.Status == "" {
		return model.ThreadActive
	}
	return t.Status
}

func threadLifecycle(t model.Thread) string {
	if t.Lifecycle == "" {
		return model.LifecycleTask
	}
	return t.Lifecycle
}

// eventLocked records an event tmx files itself (a thread opened, a state change), by the caller.
func (f *Fleet) eventLocked(t model.Thread, r *agentRec, kind, text string) {
	if utf8.RuneCountInString(text) > maxEntryText {
		text = string([]rune(text)[:maxEntryText-1]) + "…"
	}
	now := model.Now()
	e := model.ThreadEntry{ThreadID: t.ID, Kind: kind, Text: text, By: model.Owner, At: now}
	if r != nil {
		e.By, e.ByID = r.a.Name, r.a.ID
	}
	e.ID = entryID(t.ID, kind, text, now)
	added, err := f.st.AddEntry(e)
	if err != nil {
		f.log.Error("matrix: event", "thread", t.ID, "err", err)
		return
	}
	if added {
		f.bus.Publish(model.BusEvent{Type: model.EvThreadEntry, AgentID: e.ByID, Data: e})
	}
}

// ThreadCard is a thread in a list: what the Matrix shows before a thread is opened.
type ThreadCard struct {
	Thread      model.Thread   `json:"thread"`
	Counts      map[string]int `json:"counts"`                  // open links per axis
	LastEventAt int64          `json:"last_event_at,omitempty"` // 0: none yet
	SilentHours int            `json:"silent_hours,omitempty"`  // > 0: active, and quiet for longer than its lifecycle allows
}

// Mine lists the threads a caller works on (an agent: its project's and those it is on; the
// owner and the head: every thread that isn't archived), with counts per axis.
func (f *Fleet) Mine(c Caller) ([]ThreadCard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.callerRecLocked(c)
	if err != nil {
		return nil, err
	}
	threads, err := f.st.ListThreads()
	if err != nil {
		return nil, err
	}
	// Three queries for the whole list, whatever its length (the Matrix's first screen).
	counts, err := f.st.LinkCounts()
	if err != nil {
		return nil, err
	}
	last, err := f.st.LastEntryAt()
	if err != nil {
		return nil, err
	}
	on := map[string]bool{} // an agent's linked threads (onThreadLocked, for every thread at once)
	if r != nil && !isHead(r) {
		links, err := f.st.LinksTo(model.AxisAgent, r.a.ID)
		if err != nil {
			return nil, err
		}
		for _, l := range links {
			on[l.ThreadID] = true
		}
	}
	out := []ThreadCard{}
	now := model.Now()
	for _, t := range threads {
		if t.Archived {
			continue
		}
		if r != nil && !isHead(r) {
			mine := r.a.ProjectID != "" && (t.ProjectID == r.a.ProjectID || t.ID == model.ProjectThread(r.a.ProjectID))
			if t.Kind == model.ThreadChat || (!mine && !on[t.ID]) {
				continue
			}
		}
		// A card carries what the list and the canvas show; the panel loads the rest (View).
		t.Story, t.IntentSource = "", ""
		if utf8.RuneCountInString(t.IntentText) > cardIntent {
			t.IntentText = string([]rune(t.IntentText)[:cardIntent-1]) + "…"
		}
		card := ThreadCard{Thread: t, Counts: counts[t.ID], LastEventAt: last[t.ID]}
		if card.Counts == nil {
			card.Counts = map[string]int{}
		}
		card.SilentHours = int(silentFor(t, card.LastEventAt, now).Hours())
		if p, ok := f.projects[t.ProjectID]; ok && t.Kind == model.ThreadProject {
			card.Thread.Title = p.Name // as threadTitleLocked, without a query per thread
		}
		out = append(out, card)
	}
	return out, nil
}

// PivotRow is one value of an axis with the threads it is on (the Matrix's "see by").
type PivotRow struct {
	Ref     string   `json:"ref"`
	Label   string   `json:"label,omitempty"`
	Threads []string `json:"threads"` // thread IDs
}

// Pivot groups the open links of one axis by what they point at, over the threads the caller
// may see: "see by agents", "by shells", "by secrets" (names only).
func (f *Fleet) Pivot(c Caller, axis string) ([]PivotRow, error) {
	axis = strings.ToLower(strings.TrimSpace(axis))
	if !slices.Contains(model.LinkAxes, axis) {
		return nil, errf(KindInvalid, "axis must be one of %s", strings.Join(model.LinkAxes, ", "))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.callerRecLocked(c)
	if err != nil {
		return nil, err
	}
	links, err := f.st.LinksOn(axis)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{} // thread ID -> the caller may see it
	visible := func(id string) bool {
		if r == nil || isHead(r) {
			return true
		}
		v, ok := seen[id]
		if !ok {
			t, err := f.st.GetThread(id)
			v = err == nil && f.onThreadLocked(r, t)
			seen[id] = v
		}
		return v
	}
	rows := []PivotRow{}
	at := map[string]int{}
	for _, l := range links {
		if !visible(l.ThreadID) {
			continue
		}
		i, ok := at[l.Ref]
		if !ok {
			i = len(rows)
			at[l.Ref] = i
			rows = append(rows, PivotRow{Ref: l.Ref, Label: l.Label})
		}
		if !slices.Contains(rows[i].Threads, l.ThreadID) {
			rows[i].Threads = append(rows[i].Threads, l.ThreadID)
		}
	}
	return rows, nil
}
