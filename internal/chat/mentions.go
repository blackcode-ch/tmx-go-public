package chat

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// @mentions (DESIGN.md "Chats"). A message names people and agents with "@<label>": a
// person by the display name this install knows them by, another install's chat agent as
// "@<person>/<agent>", this install's own chat agent as "@<agent>". The author's install
// resolves the labels and sends who is meant (peer ID, agent); receivers flag messages that
// mention their owner and tell their chat agent when it is mentioned.
const (
	maxMentions     = 20
	maxMentionLabel = 80
)

type wireMention struct {
	PeerID string `json:"peer_id"`
	Agent  string `json:"agent,omitempty"`
	Label  string `json:"label"`
}

// Mentionable is someone a message in a chat can @mention.
type Mentionable struct {
	Label  string `json:"label"`
	PeerID string `json:"peer_id"`
	Agent  string `json:"agent,omitempty"`
	Kind   string `json:"kind"` // "person" | "agent"
	Mine   bool   `json:"mine,omitempty"`
}

// Mentionable lists who a message in chatID can @mention.
func (s *Service) Mentionable(c fleet.Caller, chatID string) ([]Mentionable, error) {
	if !s.Enabled() {
		return nil, ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, err := s.chatForLocked(c, chatID)
	if err != nil {
		return nil, err
	}
	return s.mentionablesLocked(ch), nil
}

func (s *Service) mentionablesLocked(ch model.Chat) []Mentionable {
	self := s.selfLocked()
	names := map[string]string{} // peer → display name
	switch ch.Kind {
	case model.ChatDM:
		if ct, err := s.st.GetContact(ch.PeerID); err == nil {
			names[ch.PeerID] = ct.Name
		}
	case model.ChatGroup:
		for _, m := range s.currentMembersLocked(ch.ID) {
			if m.PeerID != self {
				names[m.PeerID] = m.Name
			}
		}
	}
	var out []Mentionable
	for peer, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, Mentionable{Label: name, PeerID: peer, Kind: "person"})
		}
	}
	agents, _ := s.st.ChatAgents(ch.ID)
	for peer, agent := range agents {
		if name := strings.TrimSpace(names[peer]); name != "" && agent != "" {
			out = append(out, Mentionable{Label: name + "/" + agent, PeerID: peer, Agent: agent, Kind: "agent"})
		}
	}
	if mine := s.chatAgentNameLocked(ch); mine != "" {
		out = append(out, Mentionable{Label: mine, PeerID: self, Agent: mine, Kind: "agent", Mine: true})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "person"
		}
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out
}

// chatAgentNameLocked is the name of this install's agent in ch ("" = none).
func (s *Service) chatAgentNameLocked(ch model.Chat) string {
	if ch.AgentID == "" {
		return ""
	}
	if a, err := s.ag.Agent(ch.AgentID); err == nil {
		return a.Name
	}
	return ""
}

// parseMentions finds "@label" in body for the given candidates (longest label first, case
// insensitive, ending at a non-word character) and returns each one mentioned once.
func parseMentions(body string, cands []Mentionable) []model.ChatMention {
	if !strings.Contains(body, "@") || len(cands) == 0 {
		return nil
	}
	sorted := append([]Mentionable(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].Label) > len(sorted[j].Label) })
	var out []model.ChatMention
	seen := map[string]bool{}
	for i := 0; i < len(body) && len(out) < maxMentions; i++ {
		if body[i] != '@' {
			continue
		}
		if i > 0 {
			if r, _ := utf8.DecodeLastRuneInString(body[:i]); isWord(r) {
				continue // an e-mail address, not a mention
			}
		}
		rest := body[i+1:]
		for _, c := range sorted {
			l := len(c.Label)
			if l == 0 || l > len(rest) || !strings.EqualFold(rest[:l], c.Label) {
				continue
			}
			if r, _ := utf8.DecodeRuneInString(rest[l:]); l < len(rest) && isWord(r) {
				continue
			}
			key := c.PeerID + "/" + c.Agent
			if !seen[key] {
				seen[key] = true
				out = append(out, model.ChatMention{PeerID: c.PeerID, Agent: c.Agent, Label: rest[:l]})
			}
			i += l
			break
		}
	}
	return out
}

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func wireMentions(ms []model.ChatMention) []wireMention {
	var out []wireMention
	for _, m := range ms {
		out = append(out, wireMention{PeerID: m.PeerID, Agent: m.Agent, Label: m.Label})
	}
	return out
}

// acceptMentions validates the mentions of an incoming message and reports whether they name
// this install's owner and its chat agent (agentName, "" = none).
func acceptMentions(list []wireMention, self, agentName string) (ms []model.ChatMention, me, agent bool) {
	for i, w := range list {
		if i >= maxMentions {
			break
		}
		if w.PeerID == "" || len(w.PeerID) > 128 || len(w.Agent) > 40 || w.Label == "" || len(w.Label) > maxMentionLabel ||
			!utf8.ValidString(w.Label) {
			continue
		}
		ms = append(ms, model.ChatMention{PeerID: w.PeerID, Agent: cleanName(w.Agent), Label: w.Label})
		if w.PeerID == self {
			if w.Agent == "" {
				me = true
			} else if agentName != "" && strings.EqualFold(w.Agent, agentName) {
				agent = true
			}
		}
	}
	return ms, me, agent
}
