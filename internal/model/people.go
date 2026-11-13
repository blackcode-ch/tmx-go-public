package model

// ReminderParty is the From of mail the reminder scheduler delivers.
const ReminderParty = "reminder"

// Person is someone without their own tmx (a teammate, an external partner contact or a
// client), see docs/PEOPLE_PROMISES.md. Channels hold personal data: only the owner sees them;
// agents get PublicPerson.
type Person struct {
	Key       string            `json:"key"`
	Name      string            `json:"name,omitempty"`
	Role      string            `json:"role,omitempty"`
	Lang      string            `json:"lang,omitempty"`
	Kind      string            `json:"kind"` // team | external | client
	Preferred string            `json:"preferred,omitempty"`
	Aliases   []string          `json:"aliases,omitempty"`
	Channels  map[string]string `json:"channels,omitempty"` // whatsapp, email, github, tmx (their install's peer ID)
	Notes     string            `json:"notes,omitempty"`
	// Projects a team member shares with the owner (slug, name or id): within them their requests
	// count as the owner's standing order (internal/fleet/authority.go, Teammate). Team members only.
	Projects  []string `json:"projects,omitempty"`
	UpdatedAt int64    `json:"updated_at"`
}

// PublicPerson is what an agent sees: whether a channel exists, never the number or address.
type PublicPerson struct {
	Key          string   `json:"key"`
	Name         string   `json:"name,omitempty"`
	Role         string   `json:"role,omitempty"`
	Lang         string   `json:"lang,omitempty"`
	Kind         string   `json:"kind"`
	Preferred    string   `json:"preferred,omitempty"`
	Aliases      []string `json:"aliases,omitempty"`
	GitHub       string   `json:"github,omitempty"`
	HasWhatsApp  bool     `json:"has_whatsapp"`
	HasEmail     bool     `json:"has_email"`
	HasTmx       bool     `json:"has_tmx"` // their own tmx install is known (never the peer ID itself)
	Projects     []string `json:"projects,omitempty"`
	OpenPromises int      `json:"open_promises"`
}

func (p Person) Public() PublicPerson {
	return PublicPerson{Key: p.Key, Name: p.Name, Role: p.Role, Lang: p.Lang, Kind: p.Kind, Preferred: p.Preferred,
		Aliases: p.Aliases, GitHub: p.Channels["github"], HasWhatsApp: p.Channels["whatsapp"] != "", HasEmail: p.Channels["email"] != "",
		HasTmx: p.Channels["tmx"] != "", Projects: p.Projects}
}

// Promise is a tracked commitment to or from a person (Python's People "ask").
type Promise struct {
	ID        string `json:"id"`
	PersonKey string `json:"person_key"`
	Title     string `json:"title"`
	Text      string `json:"text,omitempty"`
	Owner     string `json:"owner,omitempty"` // the agent (name or ID) accountable for following it up
	Due       string `json:"due,omitempty"`   // ISO date
	Status    string `json:"status"`          // open | done | dropped
	Source    string `json:"source,omitempty"`
	CreatedBy string `json:"created_by,omitempty"` // agent ID that opened it ("" = the owner or an import)
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Reminder is a durable timed message to an agent; the scheduler fires it once, even across restarts.
type Reminder struct {
	ID        string `json:"id"`
	FireAt    int64  `json:"fire_at"` // unix ms
	Target    string `json:"target"`  // agent ID
	Text      string `json:"text"`
	PromiseID string `json:"promise_id,omitempty"`
	State     string `json:"state"` // pending | fired | cancelled
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error,omitempty"`
	FiredAt   int64  `json:"fired_at,omitempty"`
	CreatedAt int64  `json:"created_at"`
}
