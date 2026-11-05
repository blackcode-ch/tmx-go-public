package model

// The Matrix (DESIGN.md "Matrix"): the Board's threads, each with links to everything that
// works on it (agents, shells, documents, secret names, ...). A link points at something that
// lives elsewhere; the Matrix never copies it, and never holds a secret's value.

// Thread statuses: an activity's lifecycle. "" in old rows = active.
const (
	ThreadActive  = "active"           // someone is on it
	ThreadPaused  = "paused"           // deliberately on hold; nothing runs
	ThreadWaiting = "waiting_on_owner" // a card is open for the owner
	ThreadBlocked = "blocked"          // waiting on something outside (a person, a delivery)
	ThreadDone    = "done"             // finished (reopen = active)
)

// ThreadStatuses are the statuses a thread may have.
var ThreadStatuses = []string{ThreadActive, ThreadPaused, ThreadWaiting, ThreadBlocked, ThreadDone}

// Thread lifecycles. "" in old rows = task.
const (
	LifecycleTask    = "task"    // ends when its goal is met
	LifecycleOngoing = "ongoing" // never ends (a client, the books); flagged when silent, never "stuck"
)

// Link axes: what a link's Ref points at.
const (
	AxisAgent   = "agent"   // agent ID
	AxisShell   = "shell"   // process registry ID
	AxisTool    = "tool"    // agent tool or system tool ID
	AxisMemory  = "memory"  // memory file path
	AxisDoc     = "doc"     // document path or URL
	AxisFile    = "file"    // machine:path
	AxisPath    = "path"    // machine:folder
	AxisKey     = "key"     // key name (never the key)
	AxisSecret  = "secret"  // vault name (never the value)
	AxisRule    = "rule"    // rules registry ID
	AxisPerson  = "person"  // People entry
	AxisStory   = "story"   // a user story (Ref = its text's ID; Label = the text)
	AxisProject = "project" // project ID
	AxisMachine = "machine" // host name
)

// LinkAxes are the axes a link may have.
var LinkAxes = []string{AxisAgent, AxisShell, AxisTool, AxisMemory, AxisDoc, AxisFile, AxisPath, AxisKey,
	AxisSecret, AxisRule, AxisPerson, AxisStory, AxisProject, AxisMachine}

// How a link was made.
const (
	LinkAuto = "auto" // by tmx, as it happened (a launch, a shell, a grant)
	LinkHand = "hand" // by the owner or an agent
)

// ThreadLink attaches one thing to a thread. Detaching sets EndedAt; links are never deleted,
// so a thread's history stays readable.
type ThreadLink struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	Axis     string `json:"axis"`
	Ref      string `json:"ref"`
	Role     string `json:"role,omitempty"`  // e.g. lead | worker for agents
	Label    string `json:"label,omitempty"` // what to show
	Why      string `json:"why,omitempty"`
	By       string `json:"by"`  // who attached it: an agent's name or "owner"
	How      string `json:"how"` // auto | hand
	At       int64  `json:"at"`
	EndedAt  int64  `json:"ended_at,omitempty"`
}
