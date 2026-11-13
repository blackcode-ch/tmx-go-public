package model

import "strings"

// Teammates (config.json "teammates"): people the owner marked as teammates of a project.
// Their chat and WhatsApp messages reach agents under a label naming the relationship and its
// scope, "teammate <name>, project <Y>" (internal/fleet/authority.go), and within that project
// their requests count as the owner's standing order. Who they are comes from tmx, never from
// what they write: a chat contact is matched by its peer ID (the key its install proved when
// pairing), a WhatsApp sender by the JID the owner's own linked account saw. The name is only
// how the label reads.
type Teammate struct {
	Peer     string `json:"peer,omitempty"`     // chats between installs: the contact's peer ID
	WhatsApp string `json:"whatsapp,omitempty"` // WhatsApp: the sender's JID, e.g. "41791234567@s.whatsapp.net"
	Name     string `json:"name,omitempty"`     // how the label names them; "" = the name the message carries
	Project  string `json:"project"`            // the shared project: its slug, name or id on this install
}

// TeammateMark is what tmx attaches to a marked sender's message: the name to show ("" = the
// message's own sender name) and the shared projects, as they exist on this install. Both are
// already safe for a one-line label (peertext.Field).
type TeammateMark struct {
	Name     string
	Projects []string
}

// Phrase reads "teammate <name>, project Y" (or "..., projects Y and Z"); name is the sender's
// name as the label shows it, used when the owner gave none.
func (m TeammateMark) Phrase(name string) string {
	if m.Name != "" {
		name = m.Name
	}
	return "teammate " + name + ", " + m.Scope()
}

// Scope reads "project Y", "projects Y and Z" or "projects X, Y and Z".
func (m TeammateMark) Scope() string {
	switch len(m.Projects) {
	case 0:
		return "no project"
	case 1:
		return "project " + m.Projects[0]
	}
	return "projects " + strings.Join(m.Projects[:len(m.Projects)-1], ", ") + " and " + m.Projects[len(m.Projects)-1]
}
