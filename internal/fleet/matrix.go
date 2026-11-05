package fleet

// The Matrix (DESIGN.md "Matrix"), M0: links from a thread to everything working on it. Most
// are made by tmx as things happen (an agent launched for a thread joins it); the owner and
// agents attach the rest. A link holds a name and a reference, never a secret's value.

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

const (
	maxLinkRef   = 1000
	maxLinkLabel = 200
	maxLinkWhy   = 300
	maxLinkRole  = 40
)

// LinkRequest attaches one thing to a thread (by the thread's ID or title).
type LinkRequest struct {
	Thread string `json:"thread"`
	Axis   string `json:"axis"`
	Ref    string `json:"ref"`
	Role   string `json:"role,omitempty"`
	Label  string `json:"label,omitempty"`
	Why    string `json:"why,omitempty"`
}

// secretAxes hold names of keys and secrets: only the owner and the head attach them by hand
// (tmx attaches them itself when a grant is given).
var secretAxes = []string{model.AxisKey, model.AxisSecret}

// matrixThreadLocked returns the thread a caller may link in: the owner and the head any; an
// agent in a project its project's threads (the project's own and those under it); any agent
// the threads it is on.
func (f *Fleet) matrixThreadLocked(c Caller, idOrTitle string) (model.Thread, *agentRec, error) {
	r, err := f.callerRecLocked(c)
	if err != nil {
		return model.Thread{}, nil, err
	}
	if strings.TrimSpace(idOrTitle) == "" {
		return model.Thread{}, nil, errf(KindInvalid, "say which thread")
	}
	t, err := f.threadByLocked(idOrTitle)
	if err != nil {
		return model.Thread{}, nil, err
	}
	if r == nil || isHead(r) || f.onThreadLocked(r, t) {
		return t, r, nil
	}
	return model.Thread{}, nil, errf(KindForbidden, "%q is not one of your threads; ask your lead", t.Title)
}

// onThreadLocked reports whether an agent works on a thread: its project's, or one it is linked to.
// Never a chat's thread: what is filed there concerns people outside (go-reviewer-o on #132).
func (f *Fleet) onThreadLocked(r *agentRec, t model.Thread) bool {
	if t.Kind == model.ThreadChat {
		return false
	}
	if p := r.a.ProjectID; p != "" && (t.ProjectID == p || t.ID == model.ProjectThread(p)) {
		return true
	}
	links, err := f.st.LinksTo(model.AxisAgent, r.a.ID)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(links, func(l model.ThreadLink) bool { return l.ThreadID == t.ID })
}

// leadsThreadLocked reports whether an agent is the lead of the project a thread belongs to.
func (f *Fleet) leadsThreadLocked(r *agentRec, t model.Thread) bool {
	p := f.projects[t.ProjectID]
	if p == nil && strings.HasPrefix(t.ID, "p-") {
		p = f.projects[strings.TrimPrefix(t.ID, "p-")]
	}
	return p != nil && p.LeadID == r.a.ID
}

func linkerName(r *agentRec) string {
	if r == nil {
		return model.Owner
	}
	return r.a.Name
}

// AttachLink links one thing to a thread by hand; the same open link twice is one link.
func (f *Fleet) AttachLink(c Caller, req LinkRequest) (model.ThreadLink, error) {
	axis := strings.ToLower(strings.TrimSpace(req.Axis))
	ref, role := strings.TrimSpace(req.Ref), strings.ToLower(strings.TrimSpace(req.Role))
	label, why := strings.TrimSpace(req.Label), strings.TrimSpace(req.Why)
	switch {
	case !slices.Contains(model.LinkAxes, axis):
		return model.ThreadLink{}, errf(KindInvalid, "axis must be one of %s", strings.Join(model.LinkAxes, ", "))
	case ref == "":
		return model.ThreadLink{}, errf(KindInvalid, "say what to attach (ref)")
	case utf8.RuneCountInString(ref) > maxLinkRef, utf8.RuneCountInString(label) > maxLinkLabel,
		utf8.RuneCountInString(why) > maxLinkWhy, utf8.RuneCountInString(role) > maxLinkRole:
		return model.ThreadLink{}, errf(KindInvalid, "too long: a link is a reference and a short line; put the details in a document and attach it")
	case companion.Scrub(ref) != ref || companion.Scrub(label) != label || companion.Scrub(why) != why:
		return model.ThreadLink{}, errf(KindInvalid, "this looks like it contains a secret (a key, token or password); attach the secret's name, never its value")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	t, r, err := f.matrixThreadLocked(c, req.Thread)
	if err != nil {
		return model.ThreadLink{}, err
	}
	if slices.Contains(secretAxes, axis) && r != nil && !isHead(r) {
		return model.ThreadLink{}, errf(KindForbidden, "only the owner and the head attach keys and secrets; ask your lead")
	}
	// Being on a thread lets an agent link in it, so putting an agent on a thread is the owner's,
	// the head's or the thread's project lead's call (launches do it themselves).
	if axis == model.AxisAgent && r != nil && !isHead(r) && !f.leadsThreadLocked(r, t) {
		return model.ThreadLink{}, errf(KindForbidden, "only the owner, the head and the project's lead put agents on a thread; launch the agent for it instead")
	}
	return f.addLinkLocked(model.ThreadLink{ThreadID: t.ID, Axis: axis, Ref: ref, Role: role, Label: label, Why: why,
		By: linkerName(r), How: model.LinkHand})
}

// DetachLink ends a link (it stays in the thread's history). The owner, the head, and whoever
// may link in its thread may end it.
func (f *Fleet) DetachLink(c Caller, id string) (model.ThreadLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, err := f.st.GetLink(id)
	if err != nil {
		return model.ThreadLink{}, errf(KindNotFound, "no link %q", id)
	}
	_, r, err := f.matrixThreadLocked(c, l.ThreadID)
	if err != nil {
		return model.ThreadLink{}, err
	}
	if slices.Contains(secretAxes, l.Axis) && r != nil && !isHead(r) {
		return model.ThreadLink{}, errf(KindForbidden, "only the owner and the head detach keys and secrets; ask your lead")
	}
	if l.EndedAt != 0 {
		return l, nil
	}
	now := model.Now()
	if err := f.st.EndLink(l.ID, now); err != nil {
		return model.ThreadLink{}, err
	}
	l.EndedAt = now
	f.bus.Publish(model.BusEvent{Type: model.EvThreadLink, Data: l})
	return l, nil
}

// ThreadLinks lists a thread's links (ended ones too with past) for whoever may link in it.
func (f *Fleet) ThreadLinks(c Caller, idOrTitle string, past bool) ([]model.ThreadLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, _, err := f.matrixThreadLocked(c, idOrTitle)
	if err != nil {
		return nil, err
	}
	return f.st.Links(t.ID, past)
}

// addLinkLocked stores a link (new ID and time) and announces it if it is new.
func (f *Fleet) addLinkLocked(l model.ThreadLink) (model.ThreadLink, error) {
	l.ID, l.At = model.NewID(), model.Now()
	got, isNew, err := f.st.AddLink(l)
	if err != nil {
		return model.ThreadLink{}, err
	}
	if isNew {
		f.bus.Publish(model.BusEvent{Type: model.EvThreadLink, Data: got})
	}
	return got, nil
}

// launchThreadLocked is the thread a new agent joins: the one the launch names (checked
// against what the caller may link in), else the head's current topic when the head launches,
// else its project's thread. "" = none.
func (f *Fleet) launchThreadLocked(c Caller, req LaunchRequest, a model.Agent) (string, error) {
	if strings.TrimSpace(req.ThreadID) != "" {
		t, _, err := f.matrixThreadLocked(c, req.ThreadID)
		if err != nil {
			return "", err
		}
		return t.ID, nil
	}
	if r, _ := f.callerRecLocked(c); isHead(r) && r.thread != "" && r.thread != model.GeneralThread &&
		!strings.HasPrefix(r.thread, model.ChatThread("")) {
		return r.thread, nil
	}
	if a.ProjectID != "" {
		return f.projectThreadLocked(a.ProjectID), nil
	}
	return "", nil
}

// linkLaunchLocked links a newly launched agent to its thread (tmx did it: auto).
func (f *Fleet) linkLaunchLocked(by string, threadID string, a model.Agent) {
	if threadID == "" {
		return
	}
	role := "worker"
	if a.Level == model.LevelLead {
		role = "lead"
	}
	if _, err := f.addLinkLocked(model.ThreadLink{ThreadID: threadID, Axis: model.AxisAgent, Ref: a.ID, Role: role,
		Label: a.Name, Why: "launched for this thread", By: by, How: model.LinkAuto}); err != nil {
		f.log.Error("matrix: link launched agent", "agent", a.ID, "thread", threadID, "err", err)
	}
}

// ThreadView is one thread with what is on it: the Matrix's "get".
type ThreadView struct {
	Thread model.Thread        `json:"thread"`
	Links  []model.ThreadLink  `json:"links"`
	Events []model.ThreadEntry `json:"events"` // newest last; chatter (info) only with all
	More   int                 `json:"more,omitempty"`
}

const viewEvents = 30

// View returns a thread with its open links (ended ones too with past) and its last events,
// for whoever may link in it.
func (f *Fleet) View(c Caller, idOrTitle string, past, all bool) (ThreadView, error) {
	f.mu.Lock()
	t, _, err := f.matrixThreadLocked(c, idOrTitle)
	if err == nil {
		t.Title = f.threadTitleLocked(t.ID)
	}
	f.mu.Unlock()
	if err != nil {
		return ThreadView{}, err
	}
	links, err := f.st.Links(t.ID, past)
	if err != nil {
		return ThreadView{}, err
	}
	entries, err := f.st.Entries(t.ID)
	if err != nil {
		return ThreadView{}, err
	}
	events := []model.ThreadEntry{}
	for _, e := range entries {
		if all || e.Kind != model.Chatter {
			events = append(events, e)
		}
	}
	v := ThreadView{Thread: t, Links: links, Events: events}
	if n := len(events); n > viewEvents {
		v.Events, v.More = events[n-viewEvents:], n-viewEvents
	}
	return v, nil
}

// LinksTo lists the open links to one thing across the threads the caller may see (the
// Matrix's "see by": every thread an agent, a shell or a secret's name is on).
func (f *Fleet) LinksTo(c Caller, axis, ref string) ([]model.ThreadLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.callerRecLocked(c)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(model.LinkAxes, axis) {
		return nil, errf(KindInvalid, "axis must be one of %s", strings.Join(model.LinkAxes, ", "))
	}
	links, err := f.st.LinksTo(axis, ref)
	if err != nil || r == nil || isHead(r) {
		return links, err
	}
	out := []model.ThreadLink{}
	for _, l := range links {
		if t, err := f.st.GetThread(l.ThreadID); err == nil && f.onThreadLocked(r, t) {
			out = append(out, l)
		}
	}
	return out, nil
}
