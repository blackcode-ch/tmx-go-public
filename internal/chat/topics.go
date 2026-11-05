package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Chat boards (DESIGN.md "Chat boards"). Every chat has a board its members share: topics
// (strings), which message hangs on which topic, and a short log per topic. Any member may
// open, rename, recolour and archive topics, move messages between them and add log lines;
// each change goes as a chat.topic envelope from its author to every other member through
// the outbox (an offline member gets it later). Conflicts resolve the same way everywhere:
// every topic field and every message's topic is a register where the newest change wins,
// by (time, peer ID); an author's own change is always newer than what it has seen (its
// clock is at least the register's plus one). Nothing is deleted: archived topics leave the
// board and stay readable. The order of a board's strings is each owner's own (never sent).
//
// Old installs drop chat.topic envelopes (they acknowledge, then log an unknown type) and
// ignore the topic field of messages: their board is not updated, their chats work as before.

const (
	// MaxTopics bounds a chat's topics (archived ones included).
	MaxTopics = 200
	// MaxTopicEntries bounds a chat's log lines (every topic).
	MaxTopicEntries = 5000
	// MaxMove bounds the messages moved at once.
	MaxMove = 100

	maxTopicTitle     = 80
	maxTopicEntryText = 400
	maxTopicEntryLink = 500
	// maxTopicItems bounds the topics, moves and entries of one chat.topic envelope (each).
	maxTopicItems = 200
	// topicBodyBudget: a chat.topic body is cut into several below this (p2p.MaxBody is 48 KB).
	topicBodyBudget = 32 << 10
	// snapshotEntries: the log lines per topic a new member gets from the admin.
	snapshotEntries = 50
	// BoardPerTopic is how many of each topic's latest messages GET .../board returns by default.
	BoardPerTopic    = 40
	maxBoardPerTopic = 200
)

// topicColors are the board's colours ("" = automatic), as on the head's board.
var topicColors = []string{"", "slate", "red", "orange", "amber", "green", "teal", "blue", "violet", "pink"}

// ---- wire

// wireTopicRef is the topic a message was written in (msgBody.Topic): enough for a member
// who does not know the topic yet to show it.
type wireTopicRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Color string `json:"color,omitempty"`
}

// wireTopic is a change to a topic (or its whole state, for a new member): each field with
// its time; a field whose time is 0 is not part of the change. Its author is Envelope.From.
type wireTopic struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	TitleAt    int64  `json:"title_at,omitempty"`
	Color      string `json:"color,omitempty"`
	ColorAt    int64  `json:"color_at,omitempty"`
	Archived   bool   `json:"archived,omitempty"`
	ArchivedAt int64  `json:"archived_at,omitempty"`
	CreatedAt  int64  `json:"created_at,omitempty"`
}

// wireMove files a message under a topic ("" = General).
type wireMove struct {
	Message string `json:"message"`
	Topic   string `json:"topic"`
	At      int64  `json:"at"`
}

// wireEntry is a line in a topic's log; ID = topicEntryID(Topic, Kind, Text, At).
type wireEntry struct {
	ID       string `json:"id"`
	Topic    string `json:"topic"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Link     string `json:"link,omitempty"`
	Corrects string `json:"corrects,omitempty"`
	Agent    string `json:"agent,omitempty"`
	At       int64  `json:"at"`
}

// topicBody is a chat.topic envelope: topic changes, then moves, then log lines.
type topicBody struct {
	V       int         `json:"v"`
	Topics  []wireTopic `json:"topics,omitempty"`
	Moves   []wireMove  `json:"moves,omitempty"`
	Entries []wireEntry `json:"entries,omitempty"`
}

// topicChunks cuts a body into bodies that each fit an envelope.
func topicChunks(b topicBody) []topicBody {
	var out []topicBody
	cur := topicBody{V: WireVersion}
	size, n := 64, 0
	add := func(v any, put func()) {
		j, _ := json.Marshal(v)
		if n > 0 && (size+len(j)+1 > topicBodyBudget || n >= maxTopicItems) {
			out = append(out, cur)
			cur = topicBody{V: WireVersion}
			size, n = 64, 0
		}
		put()
		size += len(j) + 1
		n++
	}
	for _, t := range b.Topics {
		add(t, func() { cur.Topics = append(cur.Topics, t) })
	}
	for _, m := range b.Moves {
		add(m, func() { cur.Moves = append(cur.Moves, m) })
	}
	for _, e := range b.Entries {
		add(e, func() { cur.Entries = append(cur.Entries, e) })
	}
	if n > 0 {
		out = append(out, cur)
	}
	return out
}

// ---- validation

func cleanTopicTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func checkTopicTitle(s string) (string, error) {
	s = cleanTopicTitle(s)
	if s == "" {
		return "", invalid("the topic needs a title")
	}
	if utf8.RuneCountInString(s) > maxTopicTitle {
		return "", invalid("the title is longer than %d characters", maxTopicTitle)
	}
	if strings.EqualFold(s, "general") {
		return "", invalid("every board has a General topic already")
	}
	return s, nil
}

func validTopicColor(c string) bool { return slices.Contains(topicColors, c) }

// validTopicID: a topic's ID, as made by newTopicID (General is not a stored topic).
func validTopicID(id string) bool {
	if id == "" || len(id) > 40 || id == model.ChatGeneralTopic {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func newTopicID() string { return "t" + model.NewID() }

// generalOr maps "" and "general" to "" (General); other IDs are kept.
func generalOr(id string) string {
	if id == model.ChatGeneralTopic {
		return ""
	}
	return id
}

// topicEntryID is a hash of the topic, kind, normalised text and day (UTC): the same line
// filed twice the same day, by anyone, is one entry.
func topicEntryID(topic, kind, text string, at int64) string {
	norm := strings.ToLower(strings.Join(strings.Fields(text), " "))
	day := time.UnixMilli(at).UTC().Format("2006-01-02")
	sum := sha256.Sum256([]byte("tmx-chat-topic|" + topic + "\x00" + kind + "\x00" + norm + "\x00" + day))
	return hex.EncodeToString(sum[:8])
}

func validEntryID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// checkEntry validates a log line (the same rules as the head's board logs).
func checkEntry(kind, text, link string) error {
	switch {
	case !slices.Contains(model.LogKinds, kind):
		return invalid("kind must be one of %s", strings.Join(model.LogKinds, ", "))
	case text == "":
		return invalid("the entry is empty")
	case utf8.RuneCountInString(text) > maxTopicEntryText:
		return invalid("keep it to %d characters (one to three lines); put the details in a message or a document and link it", maxTopicEntryText)
	case strings.Count(text, "\n") > 2:
		return invalid("keep it to three lines")
	case utf8.RuneCountInString(link) > maxTopicEntryLink:
		return invalid("the link is too long")
	case companion.Scrub(text) != text || companion.Scrub(link) != link:
		return invalid("this looks like it contains a secret (a key, token or password); logs never hold secrets")
	}
	return nil
}

// tick is the time of a change to a register last set at cur: now, but always after cur
// (so our change wins over what we have seen even if its author's clock is ahead).
func tick(cur int64) int64 { return max(model.Now(), cur+1) }

// newer: a change (at, by) wins over a register set at (curAt, curBy).
func newer(at int64, by string, curAt int64, curBy string) bool {
	return at > curAt || at == curAt && by > curBy
}

// ---- helpers

// writableBoardLocked is a chat whose board c (the owner, or the chat's participating agent)
// may change now.
func (s *Service) writableBoardLocked(c fleet.Caller, id string) (model.Chat, error) {
	ch, err := s.chatForLocked(c, id)
	if err != nil {
		return ch, err
	}
	switch ch.Status {
	case model.ChatPending:
		return ch, errf(fleet.KindConflict, "still waiting for the group's member list from its admin")
	case model.ChatLeft, model.ChatRemoved:
		return ch, errf(fleet.KindConflict, "this chat is over; its board can be read, not changed")
	}
	return ch, nil
}

// boardTargetsLocked: who gets a board change (the DM's peer, or every other member).
func (s *Service) boardTargetsLocked(ch model.Chat) []string {
	if ch.Kind == model.ChatDM {
		return []string{ch.PeerID}
	}
	return s.othersLocked(ch.ID)
}

// sendBoardLocked queues board changes for peers.
func (s *Service) sendBoardLocked(chatID string, peers []string, b topicBody) {
	for _, part := range topicChunks(b) {
		env, err := envelope(model.NewID(), TypeTopic, chatID, part)
		if err != nil {
			s.log.Error("chat: board envelope", "err", err)
			return
		}
		s.sendLocked(peers, env, "")
	}
}

func generalTopic(ch model.Chat) model.ChatTopic {
	return model.ChatTopic{ChatID: ch.ID, ID: model.ChatGeneralTopic, Kind: "general", Title: "General",
		CreatedAt: ch.CreatedAt, UpdatedAt: ch.CreatedAt}
}

// topicByLocked finds a topic by ID, else by title (any case; live ones first).
func (s *Service) topicByLocked(ch model.Chat, idOrTitle string) (model.ChatTopic, bool) {
	idOrTitle = strings.TrimSpace(idOrTitle)
	if generalOr(idOrTitle) == "" || strings.EqualFold(idOrTitle, "general") {
		return generalTopic(ch), true
	}
	if t, err := s.st.GetChatTopic(ch.ID, idOrTitle); err == nil {
		return t, true
	}
	list, _ := s.st.ChatTopics(ch.ID)
	want := cleanTopicTitle(idOrTitle)
	var archived *model.ChatTopic
	for i, t := range list {
		if strings.EqualFold(t.Title, want) {
			if !t.Archived {
				return t, true
			}
			if archived == nil {
				archived = &list[i]
			}
		}
	}
	if archived != nil {
		return *archived, true
	}
	return model.ChatTopic{}, false
}

// liveTitleTakenLocked: another live topic of the chat has this title.
func (s *Service) liveTitleTakenLocked(chatID, title, except string) bool {
	list, _ := s.st.ChatTopics(chatID)
	for _, t := range list {
		if t.ID != except && !t.Archived && strings.EqualFold(t.Title, title) {
			return true
		}
	}
	return false
}

// withTopicsLocked sets each message's topic.
func (s *Service) withTopicsLocked(msgs []model.ChatMessage) {
	if len(msgs) == 0 {
		return
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	topics, err := s.st.MessageTopics(msgs[0].ChatID, ids)
	if err != nil {
		return
	}
	for i := range msgs {
		msgs[i].Topic = topics[msgs[i].ID]
	}
}

// topicNoteLocked is what an agent's chat mail says about the topic of a message:
// ` [topic "Hotels"]`, or nothing for General. The title is the members' (data, like the
// rest of the mail): peertext.Field keeps it on one line and turns brackets into parentheses,
// so it cannot pose as a label.
func (s *Service) topicNoteLocked(chatID, topicID string) string {
	if generalOr(topicID) == "" {
		return ""
	}
	t, err := s.st.GetChatTopic(chatID, topicID)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(" [topic %q]", peertext.Field(t.Title))
}

// ---- reading

// Board is GET /api/chats/{id}/board (the owner, or the chat's participating agent): the
// topics in this owner's order (General first until there is an order), every log line,
// and the latest perTopic messages of each topic (perTopic 0: none).
func (s *Service) Board(c fleet.Caller, chatID string, perTopic int) (model.ChatBoard, error) {
	if !s.Enabled() {
		return model.ChatBoard{}, ErrOff
	}
	perTopic = min(max(perTopic, 0), maxBoardPerTopic)
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, err := s.chatForLocked(c, chatID)
	if err != nil {
		return model.ChatBoard{}, err
	}
	topics, err := s.st.ChatTopics(ch.ID)
	if err != nil {
		return model.ChatBoard{}, err
	}
	entries, err := s.st.ChatTopicEntries(ch.ID)
	if err != nil {
		return model.ChatBoard{}, err
	}
	counts, err := s.st.TopicMessageCounts(ch.ID)
	if err != nil {
		return model.ChatBoard{}, err
	}
	msgs := []model.ChatMessage{}
	if perTopic > 0 {
		if msgs, err = s.st.BoardMessages(ch.ID, perTopic); err != nil {
			return model.ChatBoard{}, err
		}
		s.withFilesLocked(msgs)
	}
	order, err := s.st.ChatBoardOrder(ch.ID)
	if err != nil {
		return model.ChatBoard{}, err
	}
	gen := generalTopic(ch)
	known := map[string]bool{}
	for i := range topics {
		known[topics[i].ID] = true
		topics[i].Messages = counts[topics[i].ID]
	}
	for id, n := range counts {
		if !known[id] {
			gen.Messages += n // no topic, or one this install does not know (yet)
		}
	}
	all := append([]model.ChatTopic{gen}, topics...)
	lastAt := map[string]int64{}
	for _, m := range msgs {
		id := m.Topic
		if !known[id] {
			id = model.ChatGeneralTopic
		}
		lastAt[id] = max(lastAt[id], m.ReceivedAt)
	}
	for _, e := range entries {
		lastAt[e.TopicID] = max(lastAt[e.TopicID], e.At)
	}
	ordered := orderTopics(all, order, lastAt)
	return model.ChatBoard{ChatID: ch.ID, Topics: all, Ordered: ordered, Entries: entries, Messages: msgs}, nil
}

// orderTopics sorts a board's topics: with an order, the placed ones by it (Position set),
// then the others oldest first (a new topic joins at the end); without, General first, then
// the most recently active.
func orderTopics(ts []model.ChatTopic, order []string, lastAt map[string]int64) bool {
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i + 1
	}
	ordered := false
	for i := range ts {
		ts[i].Position = pos[ts[i].ID]
		ordered = ordered || ts[i].Position > 0
	}
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if ordered {
			if (a.Position > 0) != (b.Position > 0) {
				return a.Position > 0
			}
			if a.Position > 0 {
				return a.Position < b.Position
			}
			return a.CreatedAt < b.CreatedAt
		}
		if (a.Kind == "general") != (b.Kind == "general") {
			return a.Kind == "general"
		}
		return max(a.CreatedAt, lastAt[a.ID]) > max(b.CreatedAt, lastAt[b.ID])
	})
	return ordered
}

// ---- changing the board (the owner; topics and moves also the chat's participating agent)

// Agent board changes: at most AgentBoardChanges per AgentBoardWindow per agent, so an agent in
// a loop cannot flood every member with topic envelopes.
const (
	AgentBoardChanges = 30
	AgentBoardWindow  = 10 * time.Minute
)

// agentBoardChangeLocked counts one board change by c; it refuses when an agent's budget is
// spent. The owner is never limited.
func (s *Service) agentBoardChangeLocked(c fleet.Caller) error {
	if c.Owner {
		return nil
	}
	if s.boardChanges == nil {
		s.boardChanges = map[string][]int64{}
	}
	now, since := model.Now(), model.Now()-AgentBoardWindow.Milliseconds()
	kept := s.boardChanges[c.AgentID][:0]
	for _, at := range s.boardChanges[c.AgentID] {
		if at > since {
			kept = append(kept, at)
		}
	}
	if len(kept) >= AgentBoardChanges {
		s.boardChanges[c.AgentID] = kept
		return errf(fleet.KindTooMany, "you changed chat boards %d times in the last 10 minutes; wait a little", len(kept))
	}
	s.boardChanges[c.AgentID] = append(kept, now)
	return nil
}

// TopicRequest is POST /api/chats/{id}/topics.
type TopicRequest struct {
	Title string `json:"title"`
	Color string `json:"color,omitempty"`
}

// CreateTopic opens a topic on a chat's board (the owner) and tells every member.
func (s *Service) CreateTopic(chatID string, req TopicRequest) (model.ChatTopic, error) {
	return s.CreateTopicAs(fleet.OwnerCaller, chatID, req)
}

// CreateTopicAs is CreateTopic by c: the owner, or the chat's participating agent.
func (s *Service) CreateTopicAs(c fleet.Caller, chatID string, req TopicRequest) (model.ChatTopic, error) {
	if !s.Enabled() {
		return model.ChatTopic{}, ErrOff
	}
	s.mu.Lock()
	defer s.unlock(false)
	ch, err := s.writableBoardLocked(c, chatID)
	if err != nil {
		return model.ChatTopic{}, err
	}
	if err := s.agentBoardChangeLocked(c); err != nil {
		return model.ChatTopic{}, err
	}
	return s.createTopicLocked(ch, req.Title, req.Color)
}

func (s *Service) createTopicLocked(ch model.Chat, title, color string) (model.ChatTopic, error) {
	title, err := checkTopicTitle(title)
	if err != nil {
		return model.ChatTopic{}, err
	}
	if !validTopicColor(color) {
		return model.ChatTopic{}, invalid("unknown colour %q", color)
	}
	if s.liveTitleTakenLocked(ch.ID, title, "") {
		return model.ChatTopic{}, errf(fleet.KindConflict, "a topic %q already exists", title)
	}
	if n, _ := s.st.CountChatTopics(ch.ID); n >= MaxTopics {
		return model.ChatTopic{}, errf(fleet.KindTooMany, "this board has %d topics (archived ones count), the most it can have", MaxTopics)
	}
	now, self := model.Now(), s.selfLocked()
	t := model.ChatTopic{ChatID: ch.ID, ID: newTopicID(), Kind: "topic", Title: title, TitleAt: now, TitleBy: self,
		Color: color, ColorAt: now, ColorBy: self, ArchivedAt: now, ArchivedBy: self, CreatedBy: self, CreatedAt: now, UpdatedAt: now}
	if err := s.st.PutChatTopic(t); err != nil {
		return model.ChatTopic{}, err
	}
	s.sendBoardLocked(ch.ID, s.boardTargetsLocked(ch), topicBody{V: WireVersion, Topics: []wireTopic{fullWire(t)}})
	s.publish(model.EvChatTopic, t)
	return t, nil
}

func fullWire(t model.ChatTopic) wireTopic {
	return wireTopic{ID: t.ID, Title: t.Title, TitleAt: t.TitleAt, Color: t.Color, ColorAt: t.ColorAt,
		Archived: t.Archived, ArchivedAt: t.ArchivedAt, CreatedAt: t.CreatedAt}
}

// TopicPatch is PATCH /api/chats/{id}/topics/{topic}; nil fields stay.
type TopicPatch struct {
	Title    *string `json:"title,omitempty"`
	Color    *string `json:"color,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
}

// UpdateTopic renames, recolours, archives or restores a topic (the owner) and tells every member.
func (s *Service) UpdateTopic(chatID, topic string, p TopicPatch) (model.ChatTopic, error) {
	return s.UpdateTopicAs(fleet.OwnerCaller, chatID, topic, p)
}

// UpdateTopicAs is UpdateTopic by c: the owner, or the chat's participating agent.
func (s *Service) UpdateTopicAs(c fleet.Caller, chatID, topic string, p TopicPatch) (model.ChatTopic, error) {
	if !s.Enabled() {
		return model.ChatTopic{}, ErrOff
	}
	s.mu.Lock()
	defer s.unlock(false)
	ch, err := s.writableBoardLocked(c, chatID)
	if err != nil {
		return model.ChatTopic{}, err
	}
	t, ok := s.topicByLocked(ch, topic)
	if !ok {
		return model.ChatTopic{}, notFound("no topic %q on this board", topic)
	}
	if t.Kind == "general" {
		return model.ChatTopic{}, invalid("General keeps its name and colour, and is never archived")
	}
	self := s.selfLocked()
	w := wireTopic{ID: t.ID}
	if p.Title != nil {
		title, err := checkTopicTitle(*p.Title)
		if err != nil {
			return t, err
		}
		if title != t.Title {
			if s.liveTitleTakenLocked(ch.ID, title, t.ID) {
				return t, errf(fleet.KindConflict, "a topic %q already exists", title)
			}
			w.Title, w.TitleAt = title, tick(t.TitleAt)
			t.Title, t.TitleAt, t.TitleBy = w.Title, w.TitleAt, self
		}
	}
	if p.Color != nil && *p.Color != t.Color {
		if !validTopicColor(*p.Color) {
			return t, invalid("unknown colour %q", *p.Color)
		}
		w.Color, w.ColorAt = *p.Color, tick(t.ColorAt)
		t.Color, t.ColorAt, t.ColorBy = w.Color, w.ColorAt, self
	}
	if p.Archived != nil && *p.Archived != t.Archived {
		w.Archived, w.ArchivedAt = *p.Archived, tick(t.ArchivedAt)
		t.Archived, t.ArchivedAt, t.ArchivedBy = w.Archived, w.ArchivedAt, self
	}
	if w.TitleAt == 0 && w.ColorAt == 0 && w.ArchivedAt == 0 {
		return t, nil // nothing changes
	}
	if err := s.agentBoardChangeLocked(c); err != nil {
		return t, err
	}
	if err := s.st.PutChatTopic(t); err != nil {
		return t, err
	}
	t.UpdatedAt = max(t.CreatedAt, t.TitleAt, t.ColorAt, t.ArchivedAt)
	s.sendBoardLocked(ch.ID, s.boardTargetsLocked(ch), topicBody{V: WireVersion, Topics: []wireTopic{w}})
	s.publish(model.EvChatTopic, t)
	return t, nil
}

// MoveRequest is POST /api/chats/{id}/board/move: messages to file under a topic
// ("" or "general" = General).
type MoveRequest struct {
	Messages []string `json:"messages"`
	Topic    string   `json:"topic"`
}

// MoveMessages files messages under a topic (the owner) and tells every member.
func (s *Service) MoveMessages(chatID string, req MoveRequest) error {
	return s.MoveMessagesAs(fleet.OwnerCaller, chatID, req)
}

// MoveMessagesAs is MoveMessages by c: the owner, or the chat's participating agent.
func (s *Service) MoveMessagesAs(c fleet.Caller, chatID string, req MoveRequest) error {
	if !s.Enabled() {
		return ErrOff
	}
	if len(req.Messages) == 0 {
		return invalid("say which messages to move")
	}
	if len(req.Messages) > MaxMove {
		return invalid("move at most %d messages at once", MaxMove)
	}
	s.mu.Lock()
	defer s.unlock(false)
	ch, err := s.writableBoardLocked(c, chatID)
	if err != nil {
		return err
	}
	to, ok := s.topicByLocked(ch, req.Topic)
	if !ok {
		return notFound("no topic %q on this board", req.Topic)
	}
	if to.Archived {
		return invalid("the topic %q is archived; restore it first", to.Title)
	}
	if err := s.agentBoardChangeLocked(c); err != nil {
		return err
	}
	topic := generalOr(to.ID)
	self := s.selfLocked()
	var moves []wireMove
	for _, id := range req.Messages {
		m, err := s.st.GetChatMessage(ch.ID, id)
		if err != nil || m.Sender == model.ChatSystem {
			return notFound("no message %q in this chat", id)
		}
		cur, at, _, err := s.st.MessageTopic(ch.ID, id)
		if err == nil && cur == topic {
			continue
		}
		mv := wireMove{Message: id, Topic: topic, At: tick(max(at, m.At))}
		if _, err := s.st.SetMessageTopic(ch.ID, id, topic, mv.At, self); err != nil {
			return err
		}
		moves = append(moves, mv)
	}
	if len(moves) == 0 {
		return nil
	}
	s.sendBoardLocked(ch.ID, s.boardTargetsLocked(ch), topicBody{V: WireVersion, Moves: moves})
	for _, mv := range moves {
		if m, err := s.messageLocked(ch.ID, mv.Message); err == nil {
			s.publish(model.EvChatMessage, m)
		}
	}
	return nil
}

// TopicEntryRequest is POST /api/chats/{id}/board/log.
type TopicEntryRequest struct {
	Topic    string `json:"topic"` // ID or title; "" or "general" = General
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Link     string `json:"link,omitempty"`
	Corrects string `json:"corrects,omitempty"`
}

// AddTopicEntry files a line in a topic's log and tells every member. It reports whether
// the line is new (the same line again the same day returns the existing one).
func (s *Service) AddTopicEntry(chatID string, req TopicEntryRequest) (model.ChatTopicEntry, bool, error) {
	if !s.Enabled() {
		return model.ChatTopicEntry{}, false, ErrOff
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	text, link := strings.TrimSpace(req.Text), strings.TrimSpace(req.Link)
	if err := checkEntry(kind, text, link); err != nil {
		return model.ChatTopicEntry{}, false, err
	}
	s.mu.Lock()
	defer s.unlock(false)
	ch, err := s.writableBoardLocked(fleet.OwnerCaller, chatID)
	if err != nil {
		return model.ChatTopicEntry{}, false, err
	}
	t, ok := s.topicByLocked(ch, req.Topic)
	if !ok {
		return model.ChatTopicEntry{}, false, notFound("no topic %q on this board", req.Topic)
	}
	if t.Archived {
		return model.ChatTopicEntry{}, false, invalid("the topic %q is archived", t.Title)
	}
	if req.Corrects != "" {
		old, err := s.st.GetChatTopicEntry(ch.ID, req.Corrects)
		if err != nil || old.TopicID != t.ID {
			return model.ChatTopicEntry{}, false, invalid("no entry %q in this log to correct", req.Corrects)
		}
	}
	if n, _ := s.st.CountChatTopicEntries(ch.ID); n >= MaxTopicEntries {
		return model.ChatTopicEntry{}, false, errf(fleet.KindTooMany, "this board's logs hold %d lines, the most they can", MaxTopicEntries)
	}
	now := model.Now()
	e := model.ChatTopicEntry{ID: topicEntryID(t.ID, kind, text, now), ChatID: ch.ID, TopicID: t.ID, Kind: kind, Text: text,
		Link: link, By: model.ChatMe, ByName: s.myName(), At: now, Corrects: req.Corrects}
	added, err := s.st.AddChatTopicEntry(e)
	if err != nil {
		return e, false, err
	}
	if !added {
		old, err := s.st.GetChatTopicEntry(ch.ID, e.ID)
		return old, false, err
	}
	s.sendBoardLocked(ch.ID, s.boardTargetsLocked(ch), topicBody{V: WireVersion, Entries: []wireEntry{{ID: e.ID, Topic: e.TopicID,
		Kind: kind, Text: text, Link: link, Corrects: e.Corrects, At: now}}})
	s.publish(model.EvChatTopicEntry, e)
	return e, true, nil
}

// SetBoardOrder is this owner's order of a board's strings, most urgent first (General
// included); it stays on this install. Topics not named keep their place; new ones join
// at the end.
func (s *Service) SetBoardOrder(chatID string, ids []string) error {
	if !s.Enabled() {
		return ErrOff
	}
	if len(ids) > MaxTopics+1 {
		return invalid("too many topics")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, err := s.chatForLocked(fleet.OwnerCaller, chatID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return invalid("topic %q is listed twice", id)
		}
		seen[id] = true
		if id == model.ChatGeneralTopic {
			continue
		}
		if _, err := s.st.GetChatTopic(ch.ID, id); err != nil {
			return notFound("no topic %q on this board", id)
		}
	}
	if err := s.st.SetChatBoardOrder(ch.ID, ids, model.Now()); err != nil {
		return err
	}
	s.publish(model.EvChatBoard, map[string]string{"chat_id": ch.ID})
	return nil
}

// ---- messages and topics

// sendTopicLocked is the topic an outgoing message is written in (nil = General): a topic's
// ID or title; a title the board does not have opens a new topic.
func (s *Service) sendTopicLocked(ch model.Chat, idOrTitle string) (*wireTopicRef, error) {
	if strings.TrimSpace(idOrTitle) == "" {
		return nil, nil
	}
	t, ok := s.topicByLocked(ch, idOrTitle)
	if !ok {
		var err error
		if t, err = s.createTopicLocked(ch, idOrTitle, ""); err != nil {
			return nil, err
		}
	}
	if t.Kind == "general" {
		return nil, nil
	}
	if t.Archived {
		return nil, invalid("the topic %q is archived; restore it to write in it", t.Title)
	}
	return &wireTopicRef{ID: t.ID, Title: t.Title, Color: t.Color}, nil
}

// fileMessageLocked files a message under the topic it was written in: the register starts
// at the message's own time and author, the same on every install.
func (s *Service) fileMessageLocked(ch model.Chat, author string, m model.ChatMessage, ref *wireTopicRef) {
	if ref == nil || generalOr(ref.ID) == "" {
		return
	}
	if !validTopicID(ref.ID) {
		return
	}
	if _, err := s.st.GetChatTopic(ch.ID, ref.ID); errors.Is(err, store.ErrNotFound) {
		// A topic we have not heard of yet (its change is still on its way, or we joined
		// later): show it from the message's hint; any real change to it wins.
		title := cleanTopicTitle(ref.Title)
		n, _ := s.st.CountChatTopics(ch.ID)
		if title == "" || utf8.RuneCountInString(title) > maxTopicTitle || n >= MaxTopics {
			return
		}
		color := ref.Color
		if !validTopicColor(color) {
			color = ""
		}
		t := model.ChatTopic{ChatID: ch.ID, ID: ref.ID, Kind: "topic", Title: title, Color: color, CreatedBy: author, CreatedAt: m.At}
		if err := s.st.PutChatTopic(t); err != nil {
			return
		}
		s.publish(model.EvChatTopic, t)
	}
	if author == model.ChatMe {
		author = s.selfLocked()
	}
	_, _ = s.st.SetMessageTopic(ch.ID, m.ID, ref.ID, m.At, author)
}

// ---- a new member gets the board

// sendBoardToLocked sends a chat's topics and the latest lines of their logs to one peer
// (the admin, to a member who just joined).
func (s *Service) sendBoardToLocked(chatID, peer string) {
	topics, err := s.st.ChatTopics(chatID)
	if err != nil {
		return
	}
	entries, err := s.st.ChatTopicEntries(chatID)
	if err != nil {
		return
	}
	b := topicBody{V: WireVersion}
	for _, t := range topics {
		b.Topics = append(b.Topics, fullWire(t))
	}
	per := map[string]int{}
	for i := len(entries) - 1; i >= 0; i-- {
		if per[entries[i].TopicID]++; per[entries[i].TopicID] > snapshotEntries {
			entries = slices.Delete(entries, i, i+1)
		}
	}
	for _, e := range entries {
		b.Entries = append(b.Entries, wireEntry{ID: e.ID, Topic: e.TopicID, Kind: e.Kind, Text: e.Text, Link: e.Link,
			Corrects: e.Corrects, Agent: e.ByAgent, At: e.At})
	}
	if len(b.Topics) == 0 && len(b.Entries) == 0 {
		return
	}
	s.sendBoardLocked(chatID, []string{peer}, b)
}

// ---- receiving

func (s *Service) onTopic(env p2p.Envelope) error { return s.onTopicAt(env, false) }

// onTopicAt applies a member's board changes. Like a message, it is accepted only from the
// DM's peer or a current member of the group (one our state does not list yet is held until
// the next state from the admin). Invalid items are skipped; the rest apply.
func (s *Service) onTopicAt(env p2p.Envelope, replay bool) error {
	var b topicBody
	if err := decodeBody(env.Body, &b); err != nil {
		return err
	}
	if env.ChatID == "" {
		return errors.New("board change without a chat")
	}
	if len(b.Topics) > maxTopicItems || len(b.Moves) > maxTopicItems || len(b.Entries) > maxTopicItems {
		return errors.New("too many board changes in one envelope")
	}
	s.mu.Lock()
	defer s.unlock(false)
	ch, err := s.st.GetChat(env.ChatID)
	if err != nil {
		return errors.New("unknown chat")
	}
	var senderName string
	switch ch.Kind {
	case model.ChatDM:
		if ch.Status != model.ChatActive || env.From != ch.PeerID {
			return errors.New("not from the DM's peer")
		}
		if ct, err := s.st.GetContact(env.From); err == nil {
			senderName = ct.Name
		}
	case model.ChatGroup:
		if ch.Status != model.ChatActive && ch.Status != model.ChatPending {
			return errors.New("chat is not active")
		}
		mem, ok := s.memberLocked(ch.ID, env.From)
		if !ok || ch.Status == model.ChatPending {
			if !replay && s.holdLocked(env) {
				return errors.New("sender is not a member (yet): held until the group's next state")
			}
			return errors.New("sender is not a member")
		}
		senderName = mem.Name
	}
	if senderName == "" {
		senderName = "Someone"
	}
	var skipped []string
	for _, w := range b.Topics {
		if err := s.applyTopicLocked(ch, env.From, w); err != nil {
			skipped = append(skipped, "topic "+w.ID+": "+err.Error())
		}
	}
	for _, mv := range b.Moves {
		if err := s.applyMoveLocked(ch, env.From, mv); err != nil {
			skipped = append(skipped, "move "+mv.Message+": "+err.Error())
		}
	}
	for _, e := range b.Entries {
		if err := s.applyEntryLocked(ch, env.From, senderName, e); err != nil {
			skipped = append(skipped, "entry "+e.ID+": "+err.Error())
		}
	}
	if len(skipped) > 0 {
		s.log.Info("chat: board changes skipped", "from", env.From, "chat", ch.ID, "skipped", strings.Join(skipped, "; "))
	}
	return nil
}

// applyTopicLocked merges a member's change to a topic, field by field.
func (s *Service) applyTopicLocked(ch model.Chat, by string, w wireTopic) error {
	if !validTopicID(w.ID) {
		return errors.New("bad topic id")
	}
	if w.TitleAt < 0 || w.ColorAt < 0 || w.ArchivedAt < 0 {
		return errors.New("bad time")
	}
	if w.TitleAt > 0 {
		title := cleanTopicTitle(w.Title)
		if title == "" || utf8.RuneCountInString(title) > maxTopicTitle {
			return errors.New("bad title")
		}
		w.Title = title
	}
	if w.ColorAt > 0 && !validTopicColor(w.Color) {
		return errors.New("bad colour")
	}
	t, err := s.st.GetChatTopic(ch.ID, w.ID)
	isNew := errors.Is(err, store.ErrNotFound)
	switch {
	case err != nil && !isNew:
		return err
	case isNew && w.TitleAt == 0:
		return errors.New("a new topic without a title")
	case isNew:
		if n, _ := s.st.CountChatTopics(ch.ID); n >= MaxTopics {
			return errors.New("the board has the most topics it can have")
		}
		created := w.CreatedAt
		if created <= 0 {
			created = w.TitleAt
		}
		t = model.ChatTopic{ChatID: ch.ID, ID: w.ID, Kind: "topic", CreatedBy: by, CreatedAt: created}
	}
	changed := isNew
	if w.TitleAt > 0 && newer(w.TitleAt, by, t.TitleAt, t.TitleBy) {
		t.Title, t.TitleAt, t.TitleBy, changed = w.Title, w.TitleAt, by, true
	}
	if w.ColorAt > 0 && newer(w.ColorAt, by, t.ColorAt, t.ColorBy) {
		t.Color, t.ColorAt, t.ColorBy, changed = w.Color, w.ColorAt, by, true
	}
	if w.ArchivedAt > 0 && newer(w.ArchivedAt, by, t.ArchivedAt, t.ArchivedBy) {
		t.Archived, t.ArchivedAt, t.ArchivedBy, changed = w.Archived, w.ArchivedAt, by, true
	}
	if !changed {
		return nil
	}
	if err := s.st.PutChatTopic(t); err != nil {
		return err
	}
	t.UpdatedAt = max(t.CreatedAt, t.TitleAt, t.ColorAt, t.ArchivedAt)
	s.publish(model.EvChatTopic, t)
	return nil
}

// applyMoveLocked files a message under a topic if the move is newer than what we have. The
// message may not be here yet (its author's copy is still on its way): the register waits
// for it.
func (s *Service) applyMoveLocked(ch model.Chat, by string, mv wireMove) error {
	if mv.Message == "" || len(mv.Message) > 64 || mv.At <= 0 {
		return errors.New("bad move")
	}
	topic := generalOr(mv.Topic)
	if topic != "" && !validTopicID(topic) {
		return errors.New("bad topic id")
	}
	changed, err := s.st.SetMessageTopic(ch.ID, mv.Message, topic, mv.At, by)
	if err != nil || !changed {
		return err
	}
	if m, err := s.messageLocked(ch.ID, mv.Message); err == nil {
		s.publish(model.EvChatMessage, m)
	}
	return nil
}

// applyEntryLocked stores a member's log line (the same rules as ours; its ID must be the
// hash of its content, so the same line from two members is one entry).
func (s *Service) applyEntryLocked(ch model.Chat, by, byName string, w wireEntry) error {
	topic := w.Topic
	if topic != model.ChatGeneralTopic && !validTopicID(topic) {
		return errors.New("bad topic id")
	}
	text, link := strings.TrimSpace(w.Text), strings.TrimSpace(w.Link)
	if err := checkEntry(w.Kind, text, link); err != nil {
		return err
	}
	if w.At <= 0 || !validEntryID(w.ID) || w.ID != topicEntryID(topic, w.Kind, text, w.At) {
		return errors.New("the entry's id does not match its content")
	}
	if w.Corrects != "" && !validEntryID(w.Corrects) {
		return errors.New("bad corrects")
	}
	if n, _ := s.st.CountChatTopicEntries(ch.ID); n >= MaxTopicEntries {
		return errors.New("the board's logs are full")
	}
	agent := cleanName(w.Agent)
	if len(agent) > 40 {
		agent = agent[:40]
	}
	e := model.ChatTopicEntry{ID: w.ID, ChatID: ch.ID, TopicID: topic, Kind: w.Kind, Text: text, Link: link, By: by,
		ByName: byName, ByAgent: agent, At: w.At, Corrects: w.Corrects}
	added, err := s.st.AddChatTopicEntry(e)
	if err != nil || !added {
		return err
	}
	s.publish(model.EvChatTopicEntry, e)
	return nil
}
