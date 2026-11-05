package fleet

import (
	"errors"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func (f *Fleet) MessagePins(c Caller) ([]model.MessagePin, error) {
	if !c.Owner && c.AgentID == "" {
		return nil, errf(KindForbidden, "pins require the owner or an addressed agent")
	}
	return f.st.MessagePins(c.AgentID, c.Owner)
}

// PinMessage saves the owner's selection. Agents may read their own pins but cannot
// create, complete or dismiss the owner's reminders. Pinning never dispatches mail.
func (f *Fleet) PinMessage(c Caller, p model.MessagePin) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner can pin messages")
	}
	if p.Source == "" || len(p.Source) > 512 || strings.TrimSpace(p.Text) == "" || len(p.Text) > 256*1024 || len(p.Label) > 256 || p.Label == "" || len(p.Href) > 1024 || !strings.HasPrefix(p.Href, "#/") {
		return errf(KindInvalid, "invalid message selection")
	}
	if p.AgentID != "" {
		a, err := f.Agent(p.AgentID)
		if err != nil {
			return err
		}
		p.AgentID = a.ID
	}
	p.ID, p.PinnedAt = model.NewID(), model.Now()
	if err := f.st.SaveMessagePin(p); err != nil {
		return err
	}
	f.Publish(model.BusEvent{Type: "message.pins.changed"}) // invalidation only: no private content on the bus
	return nil
}

func (f *Fleet) SetMessagePinState(c Caller, id, state string) error {
	if !c.Owner {
		return errf(KindForbidden, "only the owner can change pins")
	}
	if state != "active" && state != "done" && state != "unpinned" {
		return errf(KindInvalid, "invalid pin state")
	}
	if err := f.st.SetMessagePinState(id, state); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errf(KindNotFound, "pin not found")
		}
		return err
	}
	f.Publish(model.BusEvent{Type: "message.pins.changed"})
	return nil
}
