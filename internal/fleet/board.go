package fleet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
)

// The board (DESIGN.md "Board"): the head's conversation filed under threads (topics). Every
// head timeline entry carries the thread of the turn it belongs to. A turn's thread comes
// from what started it: the topic the owner wrote in, the thread of mail (a project's lead
// writes in its project's thread; a reply stays in the thread it answers), or General. A
// General turn the owner did not file is moved when it turns out to be about a project
// (it created one, or only talked to one project's agents). The head files a turn itself
// with thread_set.

// boardRules is added to the head's system prompt.
const boardRules = `The owner follows your conversation on a board of topics. A turn filed under a topic starts with "[topic: <title>]"; ` +
	`chat messages are filed under their chat, anything else without that line under General; ` +
	`what you do and say in that turn (replies, messages, launches, questions) is filed there too. ` +
	`When the owner starts something that belongs elsewhere (new work, or another project), first call mcp__tmxg__thread_set with the right topic: ` +
	`a project's name files the turn with that project, a new title opens a new topic. Keep each turn to one topic. ` +
	`mcp__tmxg__threads lists the topics in the owner's order of urgency, most urgent first; when the owner asks, rename, archive, restore or delete one with mcp__tmxg__thread_update. ` +
	`Each topic keeps a log of short lines worth keeping (decision, action, info, link): file important outcomes with mcp__tmxg__thread_add as they happen (a correction is a new line with corrects), ` +
	`and read a topic's log with mcp__tmxg__thread_read before you work on it.`

const (
	maxThreadTitle = 80
	cardText       = 600
	cardNote       = 300
)

func isHead(r *agentRec) bool { return r != nil && r.a.Level == model.LevelHead }

func threadOr(id string) string {
	if id == "" {
		return model.GeneralThread
	}
	return id
}

// loadBoardLocked restores the head's thread at startup and files entries from before the
// board existed.
func (f *Fleet) loadBoardLocked(r *agentRec) {
	t, err := f.st.LastThread(r.a.ID)
	if err != nil {
		f.log.Error("board: last thread", "err", err)
	}
	r.thread, r.turnFrom = threadOr(t), r.seq+1
	has, err := f.st.HasUnthreaded(r.a.ID)
	if err != nil || !has {
		return
	}
	entries, err := f.st.BoardEntries(r.a.ID, 0)
	if err != nil {
		f.log.Error("board: read timeline", "err", err)
		return
	}
	turns := splitTurns(entries)
	for i, t := range turns {
		if t[0].ThreadID != "" {
			continue
		}
		to := int64(0)
		if i+1 < len(turns) {
			to = turns[i+1][0].Seq - 1
		}
		if err := f.st.FillTimelineThread(r.a.ID, t[0].Seq, to, f.inferThreadLocked(t)); err != nil {
			f.log.Error("board: file old turns", "err", err)
			return
		}
	}
	// Whatever came before the first turn (a start-up notice) is General.
	if err := f.st.FillTimelineThread(r.a.ID, 0, 0, model.GeneralThread); err != nil {
		f.log.Error("board: file old turns", "err", err)
	}
	if t, err := f.st.LastThread(r.a.ID); err == nil {
		r.thread = threadOr(t)
	}
}

// beginTurnLocked starts a head turn in a thread; explicit: the owner or mail chose it (an
// unfiled General turn may be moved when it ends).
func (f *Fleet) beginTurnLocked(r *agentRec, thread string, explicit bool) {
	r.thread, r.turnFrom, r.turnExplicit = threadOr(thread), r.seq+1, explicit
}

// endTurnLocked runs after a head turn's result: an unfiled General turn that was about a
// project moves to the project's thread.
func (f *Fleet) endTurnLocked(r *agentRec) {
	if r.turnExplicit || threadOr(r.thread) != model.GeneralThread || r.turnFrom <= 0 {
		return
	}
	entries, err := f.st.BoardEntries(r.a.ID, r.turnFrom)
	if err != nil || len(entries) == 0 {
		return
	}
	if t := f.inferThreadLocked(entries); t != model.GeneralThread {
		f.moveTurnLocked(r, r.turnFrom, 0, t)
	}
}

// moveTurnLocked files the head's entries from..to (to <= 0: to the end) under a thread.
func (f *Fleet) moveTurnLocked(r *agentRec, from, to int64, thread string) {
	if err := f.st.SetTimelineThread(r.a.ID, from, to, thread); err != nil {
		f.log.Error("board: move turn", "err", err)
		return
	}
	if r.turnFrom >= from && (to <= 0 || r.turnFrom <= to) {
		r.thread, r.turnExplicit = thread, true
	}
	f.bus.Publish(model.BusEvent{Type: model.EvBoardChanged, AgentID: r.a.ID, Data: map[string]string{"thread_id": thread}})
}

// inferThreadLocked guesses a turn's thread: the chat or the project of the agent whose mail
// started it, else the project it created, else the one project whose agents it launched or
// messaged; General otherwise.
func (f *Fleet) inferThreadLocked(turn []model.TimelineEntry) string {
	projects := map[string]bool{}
	byAgent := func(name string) {
		if r, err := f.resolveLocked(name); err == nil && r.a.ProjectID != "" {
			projects[r.a.ProjectID] = true
		}
	}
	for _, e := range turn {
		switch e.Kind {
		case model.EntryMessageIn:
			if from, _ := e.Meta["from"].(string); IsExternal(from) {
				if t := f.chatThreadLocked(from); t != "" {
					return t
				}
			} else if from != "" && from != model.Owner {
				if r, ok := f.agents[from]; ok && r.a.ProjectID != "" {
					if t := f.projectThreadLocked(r.a.ProjectID); t != "" {
						return t
					}
				}
			}
		case model.EntryToolUse:
			in := toolInput(e)
			switch e.Tool {
			case "mcp__tmxg__project_create":
				if p, err := f.projectLocked(Slugify(in["name"])); err == nil {
					if t := f.projectThreadLocked(p.ID); t != "" {
						return t
					}
				}
			case "mcp__tmxg__agent_launch":
				if p, err := f.projectLocked(in["project"]); in["project"] != "" && err == nil {
					projects[p.ID] = true
				} else {
					byAgent(in["name"])
				}
			case "mcp__tmxg__send":
				if in["to"] != model.Owner {
					byAgent(in["to"])
				}
			}
		}
	}
	if len(projects) == 1 {
		for id := range projects {
			if t := f.projectThreadLocked(id); t != "" {
				return t
			}
		}
	}
	return model.GeneralThread
}

// projectThreadLocked is a project's thread, created if missing ("" if no such project).
func (f *Fleet) projectThreadLocked(projectID string) string {
	p, ok := f.projects[projectID]
	if !ok {
		return ""
	}
	id := model.ProjectThread(p.ID)
	if _, err := f.st.GetThread(id); err == nil {
		return id
	}
	now := model.Now()
	t := model.Thread{ID: id, Title: p.Name, Kind: model.ThreadProject, ProjectID: p.ID, CreatedAt: now, UpdatedAt: now}
	if err := f.st.PutThread(t); err != nil {
		f.log.Error("board: project thread", "err", err)
		return ""
	}
	f.bus.Publish(model.BusEvent{Type: model.EvThreadUpdated, ProjectID: p.ID, Data: t})
	return id
}

// chatThreadLocked is the thread of a chat with other people ("chat:<id>" mail), created if
// missing and titled with the chat's name.
func (f *Fleet) chatThreadLocked(from string) string {
	chatID := strings.TrimPrefix(from, ExternalPrefix)
	if chatID == "" {
		return ""
	}
	id := model.ChatThread(chatID)
	if _, err := f.st.GetThread(id); err == nil {
		return id
	}
	title := "Chat"
	if c, err := f.st.GetChat(chatID); err == nil && c.Name != "" {
		title = c.Name
	}
	now := model.Now()
	t := model.Thread{ID: id, Title: title, Kind: model.ThreadChat, ChatID: chatID, CreatedAt: now, UpdatedAt: now}
	if err := f.st.PutThread(t); err != nil {
		f.log.Error("board: chat thread", "err", err)
		return ""
	}
	f.bus.Publish(model.BusEvent{Type: model.EvThreadUpdated, Data: t})
	return id
}

// threadTitleLocked is what the head sees in "[topic: …]".
func (f *Fleet) threadTitleLocked(id string) string {
	t, err := f.st.GetThread(threadOr(id))
	if err != nil {
		return "General"
	}
	if p, ok := f.projects[t.ProjectID]; ok && t.Kind == model.ThreadProject {
		return p.Name
	}
	return t.Title
}

// topicLineLocked is the line a head turn in a topic starts with: none for General, nor for
// a chat (its messages' label names the chat). It says how long the topic's log is, so the
// head knows there is something to read (thread_read).
func (f *Fleet) topicLineLocked(id string) string {
	if threadOr(id) == model.GeneralThread || strings.HasPrefix(id, model.ChatThread("")) {
		return ""
	}
	line := "[topic: " + f.threadTitleLocked(id)
	if all, err := f.st.Entries(id); err == nil {
		if n := len(liveEntries(all)); n > 0 {
			line += fmt.Sprintf(" · %d %s in its log", n, map[bool]string{true: "line", false: "lines"}[n == 1])
		}
	}
	return line + "]\n"
}

// mailThreadLocked is the thread of a message: what the head sends belongs to its current
// turn's thread; mail to the head from the owner is in the thread the owner chose, from an
// agent in the thread it answers or else the sender's project's thread.
func (f *Fleet) mailThreadLocked(c Caller, toID string, req SendRequest, orig *model.Message) (string, error) {
	h := f.headLocked()
	switch {
	case h == nil:
		return "", nil
	case c.ID() == h.a.ID:
		return h.thread, nil
	case toID != h.a.ID:
		return "", nil
	case c.Owner:
		if req.ThreadID == "" {
			return "", nil
		}
		t, err := f.st.GetThread(req.ThreadID)
		if err != nil {
			return "", errf(KindInvalid, "no topic %q", req.ThreadID)
		}
		if t.Archived {
			return "", errf(KindInvalid, "the topic %q is archived", t.Title)
		}
		return t.ID, nil
	case orig != nil && orig.ThreadID != "":
		return orig.ThreadID, nil
	}
	if r := f.agents[c.ID()]; r != nil && r.a.ProjectID != "" {
		return f.projectThreadLocked(r.a.ProjectID), nil
	}
	return "", nil
}

// Board returns the threads and the head's turns as cards.
func (f *Fleet) Board() (model.Board, error) {
	f.mu.Lock()
	h := f.headLocked()
	var headID string
	if h != nil {
		headID = h.a.ID
	}
	projects := map[string]model.Project{}
	for id, p := range f.projects {
		projects[id] = *p
	}
	f.mu.Unlock()

	threads, err := f.st.ListThreads()
	if err != nil {
		return model.Board{}, err
	}
	known := map[string]bool{}
	for i := range threads {
		t := &threads[i]
		known[t.ID] = true
		if t.Kind == model.ThreadChat {
			if c, err := f.st.GetChat(t.ChatID); err == nil {
				if c.Name != "" {
					t.Title = c.Name
				}
			} else {
				t.Archived = true // the chat was deleted
			}
		}
		if t.Kind != model.ThreadProject {
			continue
		}
		if p, ok := projects[t.ProjectID]; ok {
			t.Title = p.Name
			t.Archived = t.Archived || p.Status == model.ProjectArchived
		} else {
			t.Archived = true // the project was deleted
		}
	}
	b := model.Board{HeadID: headID, Threads: threads, Cards: []model.BoardCard{}}
	b.YourTurn = []model.Approval{}
	approvals, err := f.Approvals(model.ApprovalPending)
	if err != nil {
		return model.Board{}, err
	}
	for _, ap := range approvals {
		if ap.Card != nil && !ap.Deferred {
			b.YourTurn = append(b.YourTurn, ap)
		}
	}
	logs, err := f.st.Entries("")
	if err != nil {
		return model.Board{}, err
	}
	lastAt := map[string]int64{}
	for _, e := range logs {
		if !known[e.ThreadID] {
			e.ThreadID = model.GeneralThread
		}
		lastAt[e.ThreadID] = max(lastAt[e.ThreadID], e.At)
		b.Entries = append(b.Entries, e)
	}
	if b.Entries == nil {
		b.Entries = []model.ThreadEntry{}
	}
	if headID != "" {
		entries, err := f.st.BoardEntries(headID, 0)
		if err != nil {
			return model.Board{}, err
		}
		for _, t := range splitTurns(entries) {
			c := summarize(t)
			if !known[c.ThreadID] {
				c.ThreadID = model.GeneralThread
			}
			lastAt[c.ThreadID] = max(lastAt[c.ThreadID], c.Updated)
			b.Cards = append(b.Cards, c)
		}
	}
	b.Ordered = orderThreads(b.Threads, lastAt)
	return b, nil
}

// ThreadPatch is PATCH /api/threads/{id}; nil fields stay.
type ThreadPatch struct {
	Title    *string `json:"title"`
	Color    *string `json:"color"`
	Archived *bool   `json:"archived"`
}

var threadColors = []string{"", "slate", "red", "orange", "amber", "green", "teal", "blue", "violet", "pink"}

func cleanTitle(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", errf(KindInvalid, "the topic needs a title")
	}
	if utf8.RuneCountInString(s) > maxThreadTitle {
		return "", errf(KindInvalid, "the title is longer than %d characters", maxThreadTitle)
	}
	return s, nil
}

func validColor(c string) error {
	for _, ok := range threadColors {
		if c == ok {
			return nil
		}
	}
	return errf(KindInvalid, "unknown colour %q", c)
}

// CreateThread opens a topic (owner).
func (f *Fleet) CreateThread(title, color string) (model.Thread, error) {
	title, err := cleanTitle(title)
	if err != nil {
		return model.Thread{}, err
	}
	if err := validColor(color); err != nil {
		return model.Thread{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.findThreadLocked(title); ok && !t.Archived {
		return model.Thread{}, errf(KindConflict, "a topic %q already exists", t.Title)
	}
	return f.newTopicLocked(title, color)
}

func (f *Fleet) newTopicLocked(title, color string) (model.Thread, error) {
	now := model.Now()
	t := model.Thread{ID: "t-" + model.NewID(), Title: title, Kind: model.ThreadTopic, Color: color, CreatedAt: now, UpdatedAt: now}
	if err := f.st.PutThread(t); err != nil {
		return model.Thread{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvThreadUpdated, Data: t})
	return t, nil
}

// findThreadLocked finds a thread by title (any case), or a project's thread by the
// project's name or slug. Live threads win over archived ones.
func (f *Fleet) findThreadLocked(title string) (model.Thread, bool) {
	for _, p := range f.projects {
		if strings.EqualFold(p.Name, title) || p.Slug == Slugify(title) || p.ID == title {
			if id := f.projectThreadLocked(p.ID); id != "" {
				if t, err := f.st.GetThread(id); err == nil {
					return t, true
				}
			}
		}
	}
	threads, err := f.st.ListThreads()
	if err != nil {
		return model.Thread{}, false
	}
	var found *model.Thread
	for i := range threads {
		t := &threads[i]
		if strings.EqualFold(t.Title, title) && (found == nil || found.Archived && !t.Archived) {
			found = t
		}
	}
	if found == nil {
		return model.Thread{}, false
	}
	return *found, true
}

// boardCallerLocked lets the owner and the head arrange the board.
func (f *Fleet) boardCallerLocked(c Caller) (*agentRec, error) {
	r, err := f.callerRecLocked(c)
	if err != nil {
		return nil, err
	}
	if r != nil && !isHead(r) {
		return nil, errf(KindForbidden, "only the owner and the head arrange the board")
	}
	return r, nil
}

// threadByLocked finds a thread by ID, else by title (a project's name or slug, a topic).
func (f *Fleet) threadByLocked(idOrTitle string) (model.Thread, error) {
	if t, err := f.st.GetThread(idOrTitle); err == nil {
		return t, nil
	}
	if t, ok := f.findThreadLocked(strings.TrimSpace(idOrTitle)); ok {
		return t, nil
	}
	return model.Thread{}, errf(KindNotFound, "no topic %q on the board", idOrTitle)
}

// UpdateThread renames, recolours, archives or restores a thread (owner or head; by ID or
// title). General keeps its title and cannot be archived; a project's or chat's thread takes
// its name.
func (f *Fleet) UpdateThread(c Caller, idOrTitle string, p ThreadPatch) (model.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.boardCallerLocked(c); err != nil {
		return model.Thread{}, err
	}
	t, err := f.threadByLocked(idOrTitle)
	if err != nil {
		return model.Thread{}, err
	}
	if p.Title != nil {
		if t.Kind != model.ThreadTopic && t.Kind != model.ThreadIntent {
			return model.Thread{}, errf(KindInvalid, "only your own topics can be renamed (a project's or chat's string takes its name)")
		}
		title, err := cleanTitle(*p.Title)
		if err != nil {
			return model.Thread{}, err
		}
		if o, ok := f.findThreadLocked(title); ok && o.ID != t.ID && !o.Archived {
			return model.Thread{}, errf(KindConflict, "a topic %q already exists", o.Title)
		}
		t.Title = title
	}
	if p.Color != nil {
		if err := validColor(*p.Color); err != nil {
			return model.Thread{}, err
		}
		t.Color = *p.Color
	}
	if p.Archived != nil {
		if t.Kind == model.ThreadGeneral && *p.Archived {
			return model.Thread{}, errf(KindInvalid, "General cannot be archived")
		}
		t.Archived = *p.Archived
	}
	t.UpdatedAt = model.Now()
	if err := f.st.PutThread(t); err != nil {
		return model.Thread{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvThreadUpdated, ProjectID: t.ProjectID, Data: t})
	return t, nil
}

// DeleteThread removes a topic (owner or head; by ID or title). Its cards move to General
// (the head's conversation is never deleted); a project's or chat's thread and General can
// only be archived.
func (f *Fleet) DeleteThread(c Caller, idOrTitle string) (model.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.boardCallerLocked(c); err != nil {
		return model.Thread{}, err
	}
	t, err := f.threadByLocked(idOrTitle)
	if err != nil {
		return model.Thread{}, err
	}
	if t.Kind != model.ThreadTopic && t.Kind != model.ThreadIntent {
		return model.Thread{}, errf(KindInvalid, "%q belongs to a %s; archive it instead", t.Title, t.Kind)
	}
	if err := f.st.DeleteThread(t.ID, model.Now()); err != nil {
		return model.Thread{}, err
	}
	if h := f.headLocked(); h != nil && h.thread == t.ID {
		h.thread = model.GeneralThread
	}
	f.bus.Publish(model.BusEvent{Type: model.EvThreadRemoved, Data: map[string]string{"id": t.ID}})
	f.bus.Publish(model.BusEvent{Type: model.EvBoardChanged, Data: map[string]string{"thread_id": model.GeneralThread}})
	return t, nil
}

// ThreadInfo is a thread with how much hangs on it (GET /api/threads, the head's threads tool).
type ThreadInfo struct {
	model.Thread
	Cards   int   `json:"cards"`
	Entries int   `json:"entries"` // log lines in force (not corrected)
	LastAt  int64 `json:"last_at,omitempty"`
}

// Threads lists the board's threads with their card counts (owner or head).
func (f *Fleet) Threads(c Caller) ([]ThreadInfo, error) {
	f.mu.Lock()
	_, err := f.boardCallerLocked(c)
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b, err := f.Board()
	if err != nil {
		return nil, err
	}
	out := make([]ThreadInfo, 0, len(b.Threads))
	idx := map[string]int{}
	for _, t := range b.Threads {
		idx[t.ID] = len(out)
		out = append(out, ThreadInfo{Thread: t})
	}
	for _, c := range b.Cards {
		if i, ok := idx[c.ThreadID]; ok {
			out[i].Cards++
			out[i].LastAt = max(out[i].LastAt, c.Updated)
		}
	}
	for _, e := range liveEntries(b.Entries) {
		if i, ok := idx[e.ThreadID]; ok {
			out[i].Entries++
			out[i].LastAt = max(out[i].LastAt, e.At)
		}
	}
	return out, nil
}

// MoveCard files the head's turn that starts at seq `card` under another thread (owner).
func (f *Fleet) MoveCard(card int64, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.headLocked()
	if h == nil {
		return errf(KindNotFound, "there is no head")
	}
	t, err := f.st.GetThread(threadID)
	if err != nil {
		return errf(KindNotFound, "no topic %q", threadID)
	}
	if t.Archived {
		return errf(KindInvalid, "the topic %q is archived", t.Title)
	}
	entries, err := f.st.BoardEntries(h.a.ID, card)
	if err != nil {
		return err
	}
	turns := splitTurns(entries)
	if len(turns) == 0 || turns[0][0].Seq != card {
		return errf(KindNotFound, "no card %d", card)
	}
	to := int64(0)
	if len(turns) > 1 {
		to = turns[1][0].Seq - 1
	}
	f.moveTurnLocked(h, card, to, t.ID)
	return nil
}

// FileTurn is the head's thread_set: it files its current turn under the thread titled
// `title` (a project's name or slug, an existing topic, or a new topic).
func (f *Fleet) FileTurn(c Caller, title string) (model.Thread, []string, error) {
	title, err := cleanTitle(title)
	if err != nil {
		return model.Thread{}, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.callerRecLocked(c)
	if err != nil {
		return model.Thread{}, nil, err
	}
	if !isHead(r) {
		return model.Thread{}, nil, errf(KindForbidden, "only the head files its turns under topics")
	}
	t, ok := f.findThreadLocked(title)
	switch {
	case !ok:
		if t, err = f.newTopicLocked(title, ""); err != nil {
			return model.Thread{}, nil, err
		}
	case t.Archived && t.Kind != model.ThreadProject:
		t.Archived, t.UpdatedAt = false, model.Now()
		if err := f.st.PutThread(t); err != nil {
			return model.Thread{}, nil, err
		}
		f.bus.Publish(model.BusEvent{Type: model.EvThreadUpdated, Data: t})
	}
	from := r.turnFrom
	if from <= 0 || from > r.seq+1 {
		from = r.seq + 1
	}
	f.moveTurnLocked(r, from, 0, t.ID)
	r.thread, r.turnExplicit = t.ID, true
	var titles []string
	if all, err := f.st.ListThreads(); err == nil {
		for _, o := range all {
			if o.Archived {
				continue
			}
			if p, ok := f.projects[o.ProjectID]; ok && o.Kind == model.ThreadProject {
				if p.Status == model.ProjectArchived {
					continue
				}
				o.Title = p.Name
			} else if o.Kind == model.ThreadProject {
				continue
			}
			titles = append(titles, o.Title)
		}
	}
	if t.Kind == model.ThreadProject {
		if p, ok := f.projects[t.ProjectID]; ok {
			t.Title = p.Name
		}
	}
	return t, titles, nil
}

// splitTurns cuts board entries into turns: a turn starts with the owner's message or mail
// (several in a row are one delivery) and runs to the next one. Entries before the first
// start are left out.
func splitTurns(entries []model.TimelineEntry) [][]model.TimelineEntry {
	var turns [][]model.TimelineEntry
	prevStart := false
	for _, e := range entries {
		start := e.Kind == model.EntryUser || e.Kind == model.EntryMessageIn
		if start && !prevStart {
			turns = append(turns, nil)
		}
		prevStart = start
		if len(turns) > 0 {
			turns[len(turns)-1] = append(turns[len(turns)-1], e)
		}
	}
	return turns
}

// summarize makes a turn's card.
func summarize(turn []model.TimelineEntry) model.BoardCard {
	c := model.BoardCard{ID: turn[0].Seq, ThreadID: threadOr(turn[0].ThreadID), At: turn[0].At}
	var prompts, senders []string
	addSender := func(s string) {
		for _, o := range senders {
			if o == s {
				return
			}
		}
		senders = append(senders, s)
	}
	for _, e := range turn {
		c.To, c.Updated = e.Seq, e.At
		switch e.Kind {
		case model.EntryUser, model.EntryMessageIn:
			from, _ := e.Meta["from"].(string)
			kind, sender, text := "mail", from, e.Text
			switch {
			case from == model.Owner:
				kind, sender = "owner", model.Owner
			case e.Kind == model.EntryUser:
				kind, sender = "start", "tmx"
			case IsExternal(from):
				kind, sender, text = "chat", "chat", e.Text
				if m := chatLabel.FindStringSubmatch(e.Text); m != nil {
					sender, text = chatSender(m[1]), e.Text[len(m[0]):]
				}
				text = peertext.Unquote(text) // the owner reads the words, not the agent's quote marks
			default:
				if n, _ := e.Meta["from_name"].(string); n != "" {
					sender = n
				}
				if from == model.CompanionParty || from == model.WhatsAppParty {
					text = peertext.Unquote(text)
				}
			}
			if c.Kind == "" {
				c.Kind = kind
			}
			addSender(sender)
			prompts = append(prompts, text)
		case model.EntryAssistant:
			if strings.TrimSpace(e.Text) != "" {
				c.Reply = e.Text
			}
		case model.EntryToolUse:
			c.Tools++
			summarizeTool(&c, e)
		case model.EntryResult:
			c.Done = true
			c.Error = c.Error || e.IsError
		case model.EntryError:
			c.Error = true
		}
	}
	c.Sender = strings.Join(senders, ", ")
	c.Prompt = clip(strings.Join(prompts, "\n\n"), cardText)
	c.Reply = clip(c.Reply, cardText)
	return c
}

// chatLabel matches the label the chat layer puts on mail from a chat (internal/chat):
// `[chat "Dinner" (id) · from Alex's head · another person's agent: …] body`.
var chatLabel = regexp.MustCompile(`^\[chat "(?:[^"\\]|\\.)*" \([^)]*\) · from ([^\]]*)\]\s*`)

// chatSender shortens the label's "from …" part to who wrote: "Alex", "Alex's head", "owner".
func chatSender(from string) string {
	if strings.HasPrefix(from, "the owner") {
		return model.Owner
	}
	if i := strings.Index(from, " · "); i >= 0 {
		from = from[:i]
	}
	return strings.TrimSpace(from)
}

func summarizeTool(c *model.BoardCard, e model.TimelineEntry) {
	in := toolInput(e)
	switch e.Tool {
	case "mcp__tmxg__send":
		if in["to"] == model.Owner {
			c.Notes = append(c.Notes, clip(in["body"], cardNote))
		} else if in["to"] != "" {
			c.SentTo = appendUnique(c.SentTo, in["to"])
		}
	case "mcp__tmxg__report":
		c.Notes = append(c.Notes, clip(in["body"], cardNote))
	case "mcp__tmxg__agent_launch":
		if in["name"] != "" {
			c.Launched = appendUnique(c.Launched, in["name"])
		}
	case "mcp__tmxg__project_create":
		if in["name"] != "" {
			c.Projects = appendUnique(c.Projects, in["name"])
		}
	case "mcp__tmxg__ask_owner":
		if in["question"] != "" {
			c.Asked = append(c.Asked, clip(in["question"], cardNote))
		}
	}
}

// toolInput reads a tool call's string arguments.
func toolInput(e model.TimelineEntry) map[string]string {
	out := map[string]string{}
	var raw map[string]any
	if json.Unmarshal(e.Input, &raw) != nil {
		return out
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func appendUnique(list []string, s string) []string {
	for _, o := range list {
		if o == s {
			return list
		}
	}
	return append(list, s)
}

// clip shortens s to n runes, at a word boundary when one is near, with an ellipsis.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndexAny(cut, " \n"); i > len(cut)*3/4 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \n.,;:") + "…"
}

// liveThread is id when it names a thread that is not archived, else "" (General).
func (f *Fleet) liveThread(id string) string {
	if id == "" {
		return ""
	}
	if t, err := f.st.GetThread(id); err == nil && !t.Archived {
		return t.ID
	}
	return ""
}
