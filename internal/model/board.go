package model

// The board (DESIGN.md "Board"): the head's conversation as strings of cards, one string per
// thread (topic). A card is one turn: what started it (the owner's message, or mail) and
// what the head did and said.

type ThreadKind string

const (
	ThreadGeneral ThreadKind = "general" // the one catch-all thread
	ThreadProject ThreadKind = "project" // one per project, opened when the project is created
	ThreadTopic   ThreadKind = "topic"   // opened by the owner or the head
	ThreadChat    ThreadKind = "chat"    // one per chat with other people that reaches the head
	ThreadIntent  ThreadKind = "intent"  // one of the owner's requests (the Matrix); may sit under a project
)

// GeneralThread is the ID of the general thread; entries with no thread belong to it.
const GeneralThread = "general"

// ProjectThread is the ID of a project's thread.
func ProjectThread(projectID string) string { return "p-" + projectID }

// ChatThread is the ID of a chat's thread.
func ChatThread(chatID string) string { return "c-" + chatID }

type Thread struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Kind      ThreadKind `json:"kind"`
	ProjectID string     `json:"project_id,omitempty"`
	ChatID    string     `json:"chat_id,omitempty"`
	Color     string     `json:"color,omitempty"`
	Archived  bool       `json:"archived,omitempty"`
	// Position: the owner's urgency order (1 = most urgent, left on the board); 0 = not placed.
	Position  int   `json:"position,omitempty"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`

	// The Matrix (matrix.go): who owns it, how far it is, the owner's own words and their source,
	// the user story it serves, the thread it sits under and where its card lies on the canvas.
	ParentID     string  `json:"parent_id,omitempty"`
	Status       string  `json:"status,omitempty"`    // "" = active
	Lifecycle    string  `json:"lifecycle,omitempty"` // task | ongoing; "" = task
	Owner        string  `json:"owner,omitempty"`     // agent ID, usually a lead
	Story        string  `json:"story,omitempty"`
	IntentText   string  `json:"intent_text,omitempty"`
	IntentSource string  `json:"intent_source,omitempty"`
	CanvasX      float64 `json:"canvas_x,omitempty"`
	CanvasY      float64 `json:"canvas_y,omitempty"`
}

// BoardCard is one head turn, summarised. ID is the seq of its first entry.
type BoardCard struct {
	ID       int64  `json:"id"`
	ThreadID string `json:"thread_id"`
	To       int64  `json:"to"` // seq of its last entry
	At       int64  `json:"at"`
	Updated  int64  `json:"updated"`
	// Kind: owner (the owner wrote), mail (an agent's message), chat (a message from a chat with
	// other people; Sender is who wrote it there, "owner" for the owner), start (the head's first prompt).
	Kind     string   `json:"kind"`
	Sender   string   `json:"sender"` // "owner" or the agent's name
	Prompt   string   `json:"prompt"`
	Reply    string   `json:"reply,omitempty"`    // the head's last text in the turn
	Notes    []string `json:"notes,omitempty"`    // what it sent the owner
	Asked    []string `json:"asked,omitempty"`    // questions put to the owner (ask_owner)
	Launched []string `json:"launched,omitempty"` // agents it launched
	Projects []string `json:"projects,omitempty"` // projects it created
	SentTo   []string `json:"sent_to,omitempty"`  // agents it messaged
	Tools    int      `json:"tools"`
	Done     bool     `json:"done"` // the turn ended (a result came)
	Error    bool     `json:"error,omitempty"`
}

// Board is GET /api/board. Threads come in board order: the owner's urgency order when
// Ordered, else General first and then the most recently active.
type Board struct {
	YourTurn []Approval    `json:"your_turn"` // typed owner cards, independent of conversation turns
	HeadID   string        `json:"head_id"`
	Threads  []Thread      `json:"threads"`
	Ordered  bool          `json:"ordered"`
	Cards    []BoardCard   `json:"cards"`
	Entries  []ThreadEntry `json:"entries"`
}

// A thread's log (DESIGN.md "Board"): short lines worth keeping, filed by the owner, the head
// or the project's agents. Append-only: a correction is a new entry that names the one it
// corrects. Kept forever (cards follow the head's timeline, which is trimmed).
const (
	LogDecision = "decision"
	LogAction   = "action"
	LogInfo     = "info"
	LogLink     = "link"
)

// LogKinds are the kinds of log entries.
var LogKinds = []string{LogDecision, LogAction, LogInfo, LogLink}

// Event kinds (the Matrix, DESIGN.md "Matrix"): what an agent appends to a thread on purpose,
// so that a thread's history is its events, not its chatter. Threads take these and LogKinds;
// chat topics, shared with other installs, keep LogKinds only.
const (
	EventResult  = "result"  // something finished, with its evidence
	EventBlocker = "blocker" // work can't go on until something outside changes
	EventHandoff = "handoff" // the work passes to someone else
	EventAsk     = "ask"     // a question to someone (the owner's cards stay in Your turn)
	EventStart   = "start"   // work on the thread begins
	EventPause   = "pause"   // work stops for now
	EventDone    = "done"    // the thread's goal is met
)

// EventKinds are the kinds a thread's log takes.
var EventKinds = append(append([]string{}, LogKinds...), EventResult, EventBlocker, EventHandoff, EventAsk,
	EventStart, EventPause, EventDone)

// Chatter is the event kind the Matrix's event view leaves out unless asked.
const Chatter = LogInfo

type ThreadEntry struct {
	// ID is a hash of thread, kind, normalised text and day: the same line filed twice the same
	// day is one entry.
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Link     string `json:"link,omitempty"`  // a URL or path it points to
	Card     int64  `json:"card,omitempty"`  // the head turn (card) it came from
	By       string `json:"by"`              // "owner" or the agent's name
	ByID     string `json:"by_id,omitempty"` // the agent's ID
	At       int64  `json:"at"`
	Corrects string `json:"corrects,omitempty"` // the entry this one replaces
}

// ProjectSection is the owner's daily project report. Needs are checklist items,
// never approvals or board cards. It is independent of the project's lifecycle status.
type ProjectSection struct {
	PreviewDir  string          `json:"preview_dir,omitempty"` // imported metadata, not permission to serve files
	Origin      *ImportOrigin   `json:"origin,omitempty"`
	FinalOutput []ProjectOutput `json:"final_output"`
	ProjectID   string          `json:"project_id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Needs       []ProjectNeed   `json:"needs"`
	Links       []ProjectLink   `json:"links"`
	Lead        string          `json:"lead,omitempty"`
	UpdatedAt   int64           `json:"updated_at"`
	UpdatedBy   string          `json:"updated_by"`
}
type ProjectNeed struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}
type ProjectLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}
type FeedbackRoute struct {
	To    string `json:"to,omitempty"`
	Named string `json:"named,omitempty"`
	How   string `json:"how"`
	Why   string `json:"why,omitempty"`
}
type ProjectFeedback struct {
	ID        string        `json:"id"`
	ProjectID string        `json:"project_id"`
	Text      string        `json:"text"`
	Done      []string      `json:"done"`
	Route     FeedbackRoute `json:"route"`
	Messages  []Message     `json:"messages"`
	CreatedAt int64         `json:"created_at"`
}
