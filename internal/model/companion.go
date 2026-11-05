package model

// Companion is the owner's desktop agent app (DESIGN.md "Companion"). tmx hands it tasks, and
// the head's and the owner's conversation messages, over its local A2A interface; these types
// are the tmx side of a task or message.

// CompanionSpec is what a task asks of Companion. tmx renders it into the exact text sent.
type CompanionSpec struct {
	Goal       string   `json:"goal"`
	Context    string   `json:"context,omitempty"`
	Steps      []string `json:"steps,omitempty"`
	Success    string   `json:"success,omitempty"`      // done when ...
	DoNotTouch []string `json:"do_not_touch,omitempty"` // what Companion must leave alone
	Where      string   `json:"where,omitempty"`        // the site, app or account it is about
	// Effects is what the asker declares: none | account | outward | money. tmx classifies the
	// text itself and keeps the stronger of the two (CompanionTask.Effects).
	Effects string `json:"effects,omitempty"`
}

// Effects of a task, weakest first. Anything but none passes the owner gate (when it is on).
const (
	EffectsNone    = "none"
	EffectsAccount = "account"
	EffectsOutward = "outward"
	EffectsMoney   = "money"
)

type CompanionTaskState string

const (
	CompanionAwaitingApproval CompanionTaskState = "awaiting_approval" // an agent asked; the owner decides
	CompanionDenied           CompanionTaskState = "denied"            // the owner declined the hand-off
	CompanionQueued           CompanionTaskState = "queued"            // approved; waits its turn (one at a time) or for Companion
	CompanionSending          CompanionTaskState = "sending"           // the request is on its way
	CompanionWorking          CompanionTaskState = "working"           // Companion took it
	CompanionInputRequired    CompanionTaskState = "input_required"    // Companion asks a question (Needs you)
	CompanionCompleted        CompanionTaskState = "completed"
	CompanionFailed           CompanionTaskState = "failed"
	CompanionCanceled         CompanionTaskState = "canceled"
	CompanionRefused          CompanionTaskState = "refused"     // Companion refused it before running (nothing was sent)
	CompanionUnconfirmed      CompanionTaskState = "unconfirmed" // maybe sent (timeout); never resent blindly
)

// Done reports whether the task reached a final state.
func (s CompanionTaskState) Done() bool {
	switch s {
	case CompanionDenied, CompanionCompleted, CompanionFailed, CompanionCanceled, CompanionRefused, CompanionUnconfirmed:
		return true
	}
	return false
}

// Kinds of CompanionTask: a task (Spec; "" in rows from before messages) or a conversation
// message (Body).
const (
	CompanionKindTask    = "task"
	CompanionKindMessage = "message"
)

// CompanionTask is one task or message for Companion. Both kinds share one queue (Companion's
// conversation with tmx takes one message at a time). Text never changes after creation: it is
// what the owner saw (in the preview or the approval) and exactly what is sent.
type CompanionTask struct {
	ID string `json:"id"`
	// Kind: CompanionKindTask (or "") or CompanionKindMessage.
	Kind string `json:"kind,omitempty"`
	// AgentID gets the outcome as mail: a task's agent, the head for its own messages; "" for
	// the owner's messages (the owner reads the outcome in the conversation).
	AgentID string `json:"agent_id"`
	Origin  string `json:"origin"` // owner | agent (a message: the head)
	// ApprovalID: the owner's approval of an agent's hand-off (or of the head's message).
	// SourceApprovalID: the Needs-you item the owner handed over. QuestionApprovalID:
	// Companion's pending question (tasks only).
	ApprovalID         string             `json:"approval_id,omitempty"`
	SourceApprovalID   string             `json:"source_approval_id,omitempty"`
	QuestionApprovalID string             `json:"question_approval_id,omitempty"`
	Spec               CompanionSpec      `json:"spec"`
	Body               string             `json:"body,omitempty"` // a message: what its sender wrote (Text frames it)
	Text               string             `json:"text"`
	Effects            string             `json:"effects"`
	State              CompanionTaskState `json:"state"`
	RemoteID           string             `json:"remote_id,omitempty"`  // Companion's task id
	ContextID          string             `json:"context_id,omitempty"` // Companion's conversation id
	Question           string             `json:"question,omitempty"`
	// Exchanges: Companion's questions about it and their answers, oldest first.
	Exchanges  []CompanionExchange `json:"exchanges,omitempty"`
	Result     string              `json:"result,omitempty"`
	Error      string              `json:"error,omitempty"`
	Attempts   int                 `json:"attempts"`
	NextAt     int64               `json:"next_at,omitempty"`
	CreatedAt  int64               `json:"created_at"`
	UpdatedAt  int64               `json:"updated_at"`
	SentAt     int64               `json:"sent_at,omitempty"`
	FinishedAt int64               `json:"finished_at,omitempty"`
}

// IsMessage reports whether t is a conversation message (not a task).
func (t CompanionTask) IsMessage() bool { return t.Kind == CompanionKindMessage }

// CompanionExchange is one question Companion asked about a task or message, and its answer.
type CompanionExchange struct {
	Question   string `json:"question"`
	AskedAt    int64  `json:"asked_at"`
	Answer     string `json:"answer,omitempty"`      // what the answerer wrote
	AnsweredBy string `json:"answered_by,omitempty"` // "owner", or the head's agent ID
	AnsweredAt int64  `json:"answered_at,omitempty"`
}

// CompanionApprovalInput is the Input of an Approval of kind companion.
type CompanionApprovalInput struct {
	TaskID   string        `json:"task_id"`
	Text     string        `json:"text,omitempty"`     // companion_task, companion_message: exactly what will be sent
	Effects  string        `json:"effects,omitempty"`  // companion_task, companion_message
	Spec     CompanionSpec `json:"spec,omitzero"`      // companion_task
	Question string        `json:"question,omitempty"` // companion_question
}
