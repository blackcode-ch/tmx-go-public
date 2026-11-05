package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Reading the account's chats. Names come from the owner's address book, else from the name a
// person chose, else their number; the owner is "You".

// phoneOf turns a phone JID into "+41791234567" ("" for groups and hidden numbers).
func phoneOf(jid string) string {
	user, server, ok := strings.Cut(jid, "@")
	if !ok || server != "s.whatsapp.net" {
		return ""
	}
	user, _, _ = strings.Cut(user, ":") // a device suffix
	user, _, _ = strings.Cut(user, ".")
	return "+" + user
}

func (s *Service) chatView(c model.WaChat) model.WaChat {
	if c.Name == "" && c.Kind == model.WaDM {
		c.Name = s.o.Store.WaContactName(c.JID)
	}
	if c.Name == "" {
		c.Name = phoneOf(c.JID)
	}
	if c.Name == "" {
		c.Name = "Unnamed chat"
		if c.Kind == model.WaGroup {
			c.Name = "Unnamed group"
		}
	}
	if c.Last != nil {
		m := s.messageView(store.WaMessageRow{WaMessage: *c.Last})
		c.Last = &m
	}
	return c
}

func (s *Service) messageView(row store.WaMessageRow) model.WaMessage {
	m := row.WaMessage
	switch {
	case m.FromMe:
		m.SenderName = "You"
	case m.SenderName == "":
		m.SenderName = firstNonEmpty(phoneOf(m.SenderJID), "Someone")
	}
	if m.Media != nil {
		md := *m.Media
		md.Downloaded = row.MediaFile != "" && s.fileExists(row.MediaFile)
		m.Media = &md
	}
	for i, r := range m.Reactions {
		if self := s.selfJID(); r.ByJID == self && self != "" {
			m.Reactions[i].By = "You"
		} else if r.By == "" {
			m.Reactions[i].By = firstNonEmpty(phoneOf(r.ByJID), "Someone")
		}
	}
	return m
}

// Chats lists chats, newest activity first; q matches names (every word) and numbers. The
// plain list leaves out the chats the phone hides (only system notices, see store.waShown); a
// search looks through every chat, so anyone WhatsApp knows can still be found.
func (s *Service) Chats(q string, limit int) ([]model.WaChat, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q = strings.TrimSpace(q)
	return s.listChats(q, limit, q == "")
}

func (s *Service) listChats(q string, limit int, shownOnly bool) ([]model.WaChat, error) {
	if err := s.ready(false); err != nil {
		return nil, err
	}
	n := limit
	if q != "" {
		n = 5000 // names come from contacts too: match after resolving them
	}
	list, err := s.o.Store.WaListChats("", n, shownOnly)
	if err != nil {
		return nil, err
	}
	out := make([]model.WaChat, 0, len(list))
	words := strings.Fields(strings.ToLower(q))
	for _, c := range list {
		c = s.chatView(c)
		if q != "" && !matches(c, words) {
			continue
		}
		out = append(out, c)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// matches: every word is in the chat's name or number (names resolved from contacts, so the
// search runs after chatView).
func matches(c model.WaChat, words []string) bool {
	hay := strings.ToLower(c.Name + " " + c.JID + " " + phoneOf(c.JID))
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// Chat returns one chat.
func (s *Service) Chat(jid string) (model.WaChat, error) {
	if err := s.ready(false); err != nil {
		return model.WaChat{}, err
	}
	c, err := s.o.Store.WaGetChat(jid)
	if errors.Is(err, store.ErrNotFound) {
		return c, errorf(http.StatusNotFound, "no WhatsApp chat %q", jid)
	}
	if err != nil {
		return c, err
	}
	return s.chatView(c), nil
}

// FindChat resolves a chat for an agent or the CLI: its JID, a number, or a name (exact, else
// the only one containing it).
func (s *Service) FindChat(ref string) (model.WaChat, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.WaChat{}, errorf(http.StatusBadRequest, "name the chat")
	}
	if c, err := s.Chat(ref); err == nil || !isNotFound(err) {
		return c, err
	}
	all, err := s.listChats("", 5000, false) // every chat, hidden ones too
	if err != nil {
		return model.WaChat{}, err
	}
	digits := strings.Map(keepDigits, ref)
	var exact, part []model.WaChat
	for _, c := range all {
		switch {
		case strings.EqualFold(c.Name, ref) || (len(digits) >= 7 && strings.Map(keepDigits, phoneOf(c.JID)) == digits):
			exact = append(exact, c)
		case strings.Contains(strings.ToLower(c.Name), strings.ToLower(ref)):
			part = append(part, c)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) == 0 && len(part) == 1:
		return part[0], nil
	case len(exact)+len(part) == 0:
		return model.WaChat{}, errorf(http.StatusNotFound, "no WhatsApp chat called %q", ref)
	}
	names := []string{}
	for _, c := range append(exact, part...) {
		names = append(names, c.Name+" ("+c.JID+")")
	}
	slices.Sort(names)
	if len(names) > 8 {
		names = append(names[:8], "…")
	}
	return model.WaChat{}, errorf(http.StatusConflict, "%q matches several chats: %s; use the JID", ref, strings.Join(names, ", "))
}

func isNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Messages returns up to limit messages before the message before (empty: the newest), oldest
// first.
func (s *Service) Messages(jid, before string, limit int) ([]model.WaMessage, error) {
	if _, err := s.Chat(jid); err != nil {
		return nil, err
	}
	rows, err := s.o.Store.WaMessages(jid, before, limit)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errorf(http.StatusNotFound, "no message %q in this chat", before)
	}
	if err != nil {
		return nil, err
	}
	out := make([]model.WaMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.messageView(r))
	}
	return out, nil
}

// MarkRead: the owner read the chat in tmx. Its unread count goes to 0; read receipts (blue
// ticks) go to the senders only when the owner turned them on.
func (s *Service) MarkRead(ctx context.Context, jid string) error {
	c, err := s.Chat(jid)
	if err != nil {
		return err
	}
	rows, err := s.o.Store.WaMessages(jid, "", 100)
	if err != nil {
		return err
	}
	bySender := map[string][]string{}
	var ids []string
	for _, r := range rows {
		if !r.FromMe && r.Status != model.WaRead && !r.Deleted {
			bySender[r.SenderJID] = append(bySender[r.SenderJID], r.ID)
			ids = append(ids, r.ID)
		}
	}
	if s.Settings().ReadReceipts && len(ids) > 0 && s.ready(true) == nil {
		for sender, list := range bySender {
			if err := s.o.Client.MarkRead(ctx, jid, sender, list); err != nil {
				s.log.Warn("whatsapp read receipt", "err", err)
			}
		}
	}
	if len(ids) > 0 {
		if _, err := s.o.Store.WaSetStatus(jid, ids, model.WaRead); err != nil {
			return err
		}
	}
	if c.Unread != 0 {
		s.evMu.Lock()
		cur, err := s.o.Store.WaGetChat(jid)
		if err == nil {
			cur.Unread = 0
			err = s.o.Store.WaPutChat(cur)
		}
		s.evMu.Unlock()
		if err != nil {
			return err
		}
		c.Unread = 0
		s.publishChat(c)
	}
	return nil
}

// ---- agents

// CanRead reports whether an agent may read WhatsApp and ask to send: its profile is in
// agent_profiles (default: only the head).
func (s *Service) CanRead(agentID string) error {
	if s.o.Fleet == nil {
		return errorf(http.StatusForbidden, "agents cannot use WhatsApp in this daemon")
	}
	p, err := s.o.Fleet.AgentProfile(agentID)
	if err != nil {
		return errorf(http.StatusForbidden, "unknown agent")
	}
	if !slices.Contains(s.Settings().AgentProfiles, p.Slug) {
		return errorf(http.StatusForbidden, "the owner lets only these agent profiles use WhatsApp: %s (Settings → WhatsApp)",
			strings.Join(s.Settings().AgentProfiles, ", "))
	}
	return nil
}
