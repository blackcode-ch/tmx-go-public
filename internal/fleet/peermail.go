package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Peer mail delivery (peer.go has the contract). Outbound mail is stored queued with its wire
// ID, sent at once, and on ErrPeerUnreachable retried by peerLoop with the same wire ID (the
// peer deduplicates) until it is accepted, refused, or peerGiveUp has passed.

// peerWireRx is Python's message ID format.
var peerWireRx = regexp.MustCompile(`^[0-9a-f]{12,32}$`)

// PeerOperator is the sender name of the owner's mail on the Python daemons (their "operator").
const PeerOperator = "operator"

// reservedSenders are the sender names the Python daemons treat as the owner or the system
// (their CONTACT_RESERVED_SENDERS): "operator" is the owner there. No agent may carry one, and
// only the owner's own mail goes out as "operator" (security-review #105).
var reservedSenders = map[string]bool{PeerOperator: true, model.CompanionParty: true, "tmx": true, model.Owner: true, "system": true}

// ReservedAgentName reports whether name may not be an agent's: this daemon's own parties
// and every name the fleet's Python daemons reserve for the owner or the system.
func ReservedAgentName(name string) bool {
	return reservedSenders[name] || name == model.WhatsAppParty
}

// Retry timing; variables so tests can shorten them.
var (
	peerBackoffBase = 15 * time.Second
	peerBackoffMax  = 5 * time.Minute
	peerGiveUp      = 24 * time.Hour
	peerPollMin     = time.Second // the loop never spins faster than this
	peerPollMax     = time.Minute
)

// peerState is the peer mail side of a Fleet. Guarded by Fleet.mu.
type peerState struct {
	sender   PeerSender
	self     string
	running  bool
	wake     chan struct{}
	inFlight map[string]bool // local IDs being sent
}

// SetPeerSender enables peer mail: s posts to the fleet's other machines; machine is this
// daemon's name (the SenderHost of outbound mail; "name@<machine>" is a local agent). It
// starts the retry loop once; the loop ends with the fleet (Close).
func (f *Fleet) SetPeerSender(s PeerSender, machine string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if machine == "" {
		machine = f.opts.Machine
	}
	f.peer.sender, f.peer.self = s, strings.ToLower(machine)
	if f.peer.wake == nil {
		f.peer.wake = make(chan struct{}, 1)
		f.peer.inFlight = map[string]bool{}
	}
	if s != nil && !f.peer.running && !f.closed {
		f.peer.running = true
		f.goTracked(f.peerLoop)
	}
}

func bodySHA(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func newWireID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (f *Fleet) selfLocked() string {
	if f.peer.self != "" {
		return f.peer.self
	}
	return strings.ToLower(f.opts.Machine)
}

// peerTargetLocked maps a "name@host" address: host is this machine → the local name; a
// known peer → its peer party; otherwise KindNotFound. Addresses without "@" are returned as is.
func (f *Fleet) peerTargetLocked(to string) (string, error) {
	if IsPeer(to) {
		name, host, ok := SplitPeer(to)
		if !ok {
			return "", errf(KindInvalid, "bad peer address %q", to)
		}
		to = name + "@" + host
	}
	name, host, ok := strings.Cut(to, "@")
	if !ok {
		return to, nil
	}
	host = strings.ToLower(host)
	if host == f.selfLocked() {
		return name, nil
	}
	if !model.ValidName(name) {
		return "", errf(KindInvalid, "%q is not an agent name", name)
	}
	if f.peer.sender == nil || !f.peer.sender.Known(host) {
		return "", errf(KindNotFound, "no machine %q in this fleet (agents on other machines are name@host)", host)
	}
	return PeerParty(name, host), nil
}

// DeliverPeer queues mail an authenticated peer machine handed over for a local agent and
// delivers it per the mail rules. The same wire ID again returns the stored message
// (duplicate); with another body it is KindConflict.
func (f *Fleet) DeliverPeer(in PeerInbound) (m model.Message, duplicate bool, err error) {
	peer := strings.ToLower(in.Peer)
	if peer == "" {
		return model.Message{}, false, errf(KindForbidden, "unknown peer machine")
	}
	if !peerWireRx.MatchString(in.ID) || in.ReplyTo != "" && !peerWireRx.MatchString(in.ReplyTo) {
		return model.Message{}, false, errf(KindInvalid, "message ids are 12-32 lowercase hex characters")
	}
	if !model.ValidName(in.Sender) {
		return model.Message{}, false, errf(KindInvalid, "sender must be an agent name of that machine")
	}
	if strings.ToLower(in.SenderHost) != peer {
		return model.Message{}, false, errf(KindForbidden, "sender_host must be the authenticated peer machine")
	}
	if strings.TrimSpace(in.Body) == "" {
		return model.Message{}, false, errf(KindInvalid, "the message is empty")
	}
	if len(in.Body) > MaxBodyBytes+1024 {
		return model.Message{}, false, errf(KindInvalid, "the message is too long")
	}
	// Urgent has no equivalent here: mail is delivered at the agent's next idle turn either way.
	sum := bodySHA(in.Body)
	f.mu.Lock()
	if prior, err := f.st.PeerByWire(peer, in.ID); err == nil {
		f.mu.Unlock()
		if prior.Direction != store.PeerIn || prior.SHA256 != sum {
			return model.Message{}, false, errf(KindConflict, "message id %s was already used for a different message", in.ID)
		}
		m, err := f.message(prior.LocalID)
		return m, true, err
	} else if !errors.Is(err, store.ErrNotFound) {
		f.mu.Unlock()
		return model.Message{}, false, err
	}
	to := in.To
	if name, host, ok := strings.Cut(to, "@"); ok {
		if strings.ToLower(host) != f.selfLocked() {
			f.mu.Unlock()
			return model.Message{}, false, errf(KindNotFound, "no agent %q on this machine", to)
		}
		to = name
	}
	r, err := f.resolveLocked(to)
	if err != nil {
		f.mu.Unlock()
		return model.Message{}, false, err
	}
	n, err := f.st.CountQueued(r.a.ID)
	if err != nil {
		f.mu.Unlock()
		return model.Message{}, false, err
	}
	if n >= MaxQueued {
		f.mu.Unlock()
		return model.Message{}, false, errf(KindTooMany, "%s already has %d messages waiting", r.a.Name, n)
	}
	m = model.Message{ID: model.NewID(), From: PeerParty(in.Sender, peer), To: r.a.ID, Body: in.Body,
		State: model.MsgQueued, CreatedAt: model.Now()}
	m.Receipt, m.ReceiptAt = "queued", m.CreatedAt
	if in.ReplyTo != "" {
		if orig, err := f.st.PeerByWire(peer, in.ReplyTo); err == nil && orig.Direction == store.PeerOut {
			m.ReplyTo = orig.LocalID
			if om, err := f.st.GetMessage(orig.LocalID); err == nil && isHead(r) {
				m.ThreadID = om.ThreadID // the answer comes back to the head's topic it was asked in
			}
		}
	}
	if err := f.st.PutPeerMessage(m, store.PeerMessage{LocalID: m.ID, Peer: peer, WireID: in.ID,
		Direction: store.PeerIn, SHA256: sum}); err != nil {
		f.mu.Unlock()
		return model.Message{}, false, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: r.a.ID, Data: m})
	f.refreshQueuedLocked(r)
	f.mu.Unlock()
	f.tryDeliver(r.a.ID)
	m, err = f.message(m.ID)
	return m, false, err
}

// sendPeerLocked stores mail from c to a peer party and returns it; the caller sends it
// (sendPeer) after unlocking.
func (f *Fleet) sendPeerLocked(c Caller, party string, req SendRequest, retryHash string, orig *model.Message) (model.Message, error) {
	if c.Companion {
		return model.Message{}, errf(KindForbidden, "Companion cannot mail other machines")
	}
	if !c.Owner && reservedSenders[f.nameOfLocked(c.ID())] {
		return model.Message{}, errf(KindForbidden, "an agent named %q cannot mail other machines: that name is reserved there for the owner or the system", f.nameOfLocked(c.ID()))
	}
	_, host, _ := SplitPeer(party)
	if f.peer.sender == nil || !f.peer.sender.Known(host) {
		return model.Message{}, errf(KindNotFound, "no machine %q in this fleet", host)
	}
	thread, err := f.mailThreadLocked(c, party, req, orig)
	if err != nil {
		return model.Message{}, err
	}
	m := model.Message{ID: model.NewID(), From: c.ID(), To: party, Body: req.Body,
		State: model.MsgQueued, ReplyTo: req.ReplyTo, CreatedAt: model.Now(), ThreadID: thread}
	m.RetryID, m.RetryHash, m.Receipt, m.ReceiptAt = req.RetryID, retryHash, "queued", m.CreatedAt
	if !c.Owner {
		if err := f.checkLimitsLocked(m); err != nil {
			return model.Message{}, err
		}
	}
	if err := f.st.PutPeerMessage(m, store.PeerMessage{LocalID: m.ID, Peer: host, WireID: newWireID(),
		Direction: store.PeerOut, SHA256: bodySHA(m.Body), NextAttemptAt: m.CreatedAt}); err != nil {
		return model.Message{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: agentParty(m), Data: m})
	return m, nil
}

// sendPeer makes one send attempt of outbound peer mail that is due (or new).
func (f *Fleet) sendPeer(id string) {
	f.mu.Lock()
	s := f.peer.sender
	if s == nil || f.peer.inFlight[id] {
		f.mu.Unlock()
		return
	}
	pm, err := f.st.PeerByLocal(id)
	if err != nil || pm.Direction != store.PeerOut || pm.NextAttemptAt == 0 {
		f.mu.Unlock()
		return
	}
	m, err := f.st.GetMessage(id)
	if err != nil {
		f.mu.Unlock()
		return
	}
	name, host, _ := SplitPeer(m.To)
	out := PeerOutbound{ID: pm.WireID, Sender: f.nameOfLocked(m.From), SenderHost: f.selfLocked(), To: name,
		Body: m.Body, Sent: float64(m.CreatedAt) / 1000, SHA256: pm.SHA256}
	if m.From == model.Owner {
		out.Sender = PeerOperator
	} else if reservedSenders[out.Sender] {
		// Never on the wire as the owner or the system, whatever the stored mail says.
		f.failPeerMailLocked(m, pm.Attempts, fmt.Errorf("the sender name %q is reserved on the other machines", out.Sender))
		f.mu.Unlock()
		return
	}
	if m.ReplyTo != "" {
		if op, err := f.st.PeerByLocal(m.ReplyTo); err == nil && op.Peer == host {
			w := op.WireID
			out.ReplyTo = &w
		}
	}
	f.peer.inFlight[id] = true
	f.mu.Unlock()

	err = s.Send(host, out)

	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.peer.inFlight, id)
	pm.Attempts++
	now := model.Now()
	switch {
	case err == nil:
		_ = f.st.SetPeerAttempt(id, pm.Attempts, 0, "")
		m.State, m.DeliveredAt = model.MsgDelivered, now
		m.Receipt, m.ReceiptAt = "accepted", now
		if err := f.st.PutMessage(m); err != nil {
			f.log.Error("peer mail", "err", err)
		}
		f.bus.Publish(model.BusEvent{Type: model.EvMessageUpdated, AgentID: agentParty(m), Data: m})
	case errors.Is(err, ErrPeerUnreachable) && time.Duration(now-m.CreatedAt)*time.Millisecond < peerGiveUp:
		_ = f.st.SetPeerAttempt(id, pm.Attempts, now+peerBackoff(pm.Attempts).Milliseconds(), err.Error())
		select {
		case f.peer.wake <- struct{}{}:
		default:
		}
	default:
		f.failPeerMailLocked(m, pm.Attempts, err)
	}
}

// failPeerMailLocked marks outbound peer mail failed for good and tells its sender.
func (f *Fleet) failPeerMailLocked(m model.Message, attempts int, err error) {
	f.log.Warn("peer mail failed", "to", m.To, "err", err)
	_ = f.st.SetPeerAttempt(m.ID, attempts, 0, err.Error())
	m.State = model.MsgFailed
	if err := f.st.SetMessageState(m.ID, model.MsgFailed, 0); err != nil {
		f.log.Error("peer mail", "err", err)
	}
	f.bus.Publish(model.BusEvent{Type: model.EvMessageUpdated, AgentID: agentParty(m), Data: m})
	if r, ok := f.agents[m.From]; ok {
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, IsError: true,
			Text: fmt.Sprintf("Message %s to %s was not delivered: %v", m.ID, f.nameOfLocked(m.To), err)})
	}
}

func peerBackoff(attempts int) time.Duration {
	d := peerBackoffBase
	for i := 1; i < attempts && d < peerBackoffMax; i++ {
		d *= 2
	}
	return min(d, peerBackoffMax)
}

// peerLoop sends due outbound peer mail until the fleet closes.
func (f *Fleet) peerLoop() {
	f.mu.Lock()
	wake := f.peer.wake
	f.mu.Unlock()
	for {
		wait := peerPollMax
		if next, err := f.st.NextPeerAttempt(); err == nil && next > 0 {
			wait = min(wait, time.Duration(next-model.Now())*time.Millisecond)
		}
		timer := time.NewTimer(max(wait, peerPollMin))
		select {
		case <-f.ctx.Done():
			timer.Stop()
			return
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
		due, err := f.st.DuePeerMessages(model.Now(), 50)
		if err != nil {
			f.log.Error("peer mail", "err", err)
			continue
		}
		for _, p := range due {
			if f.ctx.Err() != nil {
				return
			}
			f.sendPeer(p.LocalID)
		}
	}
}

// ownerPeerMapping prepares an outbox entry for an atomic owner operation.
// The operation persists it with its message and receipt, then deliverOwner uses
// the ordinary peer transport/retry loop with this stable wire identity.
func ownerPeerMapping(m *model.Message) []store.PeerMessage {
	if m == nil || !IsPeer(m.To) {
		return nil
	}
	_, host, _ := SplitPeer(m.To)
	return []store.PeerMessage{{LocalID: m.ID, Peer: host, WireID: newWireID(), Direction: store.PeerOut,
		SHA256: bodySHA(m.Body), NextAttemptAt: m.CreatedAt}}
}
