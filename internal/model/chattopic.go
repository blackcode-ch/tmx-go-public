package model

// Chat boards (DESIGN.md "Chat boards"): every chat has a board shared by its members. Its
// strings are topics; a message hangs on its topic's string (no topic: General). Topics,
// which message is on which topic, and the topics' logs travel to every member over p2p
// (chat.topic envelopes); the order of the strings is each person's own.

// ChatGeneralTopic is the ID of the catch-all topic every chat board has. It is not stored:
// messages without a topic (or with one this install does not know) hang on it.
const ChatGeneralTopic = "general"

// ChatTopic is a string on a chat's board. Every field is a last-writer-wins register (the
// latest change by (time, peer ID) wins); topics are never deleted, archived ones leave the
// board and stay readable.
type ChatTopic struct {
	ChatID string `json:"chat_id"`
	ID     string `json:"id"`
	// Kind: "general" for the catch-all, "topic" otherwise (derived).
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Color     string `json:"color,omitempty"`
	Archived  bool   `json:"archived,omitempty"`
	CreatedBy string `json:"created_by"` // peer ID of the member who opened it
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	// The clocks of the registers: when each field was last set and by whom (peer ID).
	TitleAt    int64  `json:"-"`
	TitleBy    string `json:"-"`
	ColorAt    int64  `json:"-"`
	ColorBy    string `json:"-"`
	ArchivedAt int64  `json:"-"`
	ArchivedBy string `json:"-"`
	// Derived: messages on it, and its place in this owner's order (1 = most urgent).
	Messages int `json:"messages"`
	Position int `json:"position,omitempty"`
}

// ChatTopicEntry is a line in a chat topic's log (kinds: LogKinds). Append-only like a thread
// log: the ID is a hash of topic, kind, normalised text and day, so the same line filed twice
// the same day (by anyone) is one entry; a correction is a new entry naming the old one.
type ChatTopicEntry struct {
	ID      string `json:"id"`
	ChatID  string `json:"chat_id"`
	TopicID string `json:"topic_id"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Link    string `json:"link,omitempty"`
	// By: the member's peer ID, or "me" (this install's owner); ByName the person, ByAgent the
	// agent's name when an agent filed it.
	By       string `json:"by"`
	ByName   string `json:"by_name"`
	ByAgent  string `json:"by_agent,omitempty"`
	At       int64  `json:"at"`
	Corrects string `json:"corrects,omitempty"`
}

// ChatBoard is GET /api/chats/{id}/board: the topics in this owner's order (General
// included), their logs, and the latest messages of each topic (with Topic set).
type ChatBoard struct {
	ChatID   string           `json:"chat_id"`
	Topics   []ChatTopic      `json:"topics"`
	Ordered  bool             `json:"ordered"`
	Entries  []ChatTopicEntry `json:"entries"`
	Messages []ChatMessage    `json:"messages"`
}

// Bus event types of chat boards.
const (
	EvChatTopic      = "chat.topic"       // data: ChatTopic (opened or changed)
	EvChatTopicEntry = "chat.topic.entry" // data: ChatTopicEntry
	EvChatBoard      = "chat.board"       // data: {chat_id}: this owner's order changed
)
