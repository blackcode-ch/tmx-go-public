package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Outbox: every outgoing envelope is stored first (chat_outbox) and sent by one worker per
// peer, oldest first, so a peer receives our envelopes in order even across offline spells
// and daemon restarts. A failed Send leaves the envelope at the head of the queue and the
// worker retries with backoff, and at once when PeerEvents reports the peer online.

type kickReq struct {
	force bool          // retry now even if the backoff has not passed
	done  chan struct{} // closed after the next pass
}

type worker struct {
	peer string
	kick chan kickReq
}

// enqueueLocked stores an envelope for a peer. messageID links it to one of our messages
// (its state follows the outbox); "" for receipts and group state.
func (s *Service) enqueueLocked(peer string, env p2p.Envelope, messageID string) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = s.st.AddOutbox(store.OutboxItem{PeerID: peer, ChatID: env.ChatID, MessageID: messageID,
		Envelope: b, CreatedAt: model.Now()})
	return err
}

// sendLocked queues env for each peer; their workers start when mu is released (unlock).
// It returns how many peers it was queued for.
func (s *Service) sendLocked(peers []string, env p2p.Envelope, messageID string) int {
	n := 0
	for _, p := range peers {
		if p == "" || p == s.selfLocked() {
			continue
		}
		if err := s.enqueueLocked(p, env, messageID); err != nil {
			s.log.Error("chat: queue envelope", "peer", p, "err", err)
			continue
		}
		s.pendingPeers = append(s.pendingPeers, p)
		n++
	}
	return n
}

// kick wakes a peer's worker (starting it if needed); the channel closes after its pass.
func (s *Service) kick(peer string, force bool) <-chan struct{} {
	done := make(chan struct{})
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		close(done)
		return done
	}
	w := s.workers[peer]
	if w == nil {
		w = &worker{peer: peer, kick: make(chan kickReq, 64)}
		s.workers[peer] = w
		s.goTracked(func() { s.runWorker(w) })
	}
	s.mu.Unlock()
	select {
	case w.kick <- kickReq{force: force, done: done}:
	default:
		close(done) // a pass is already pending
	}
	return done
}

// kickAll wakes several workers and waits (up to SendWait) for their first pass.
func (s *Service) kickAll(peers []string) {
	if len(peers) == 0 {
		return
	}
	var dones []<-chan struct{}
	for _, p := range peers {
		dones = append(dones, s.kick(p, false))
	}
	deadline := time.NewTimer(s.o.SendWait)
	defer deadline.Stop()
	for _, d := range dones {
		select {
		case <-d:
		case <-deadline.C:
			return
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Service) runWorker(w *worker) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		var reqs []kickReq
		select {
		case <-s.ctx.Done():
			return
		case r := <-w.kick:
			reqs = append(reqs, r)
		case <-timer.C:
		}
	drain:
		for {
			select {
			case r := <-w.kick:
				reqs = append(reqs, r)
			default:
				break drain
			}
		}
		force := false
		for _, r := range reqs {
			force = force || r.force
		}
		next := s.drain(w.peer, force)
		for _, r := range reqs {
			close(r.done)
		}
		timer.Stop()
		if next > 0 {
			timer.Reset(time.Duration(max(next-model.Now(), 1)) * time.Millisecond)
		}
	}
}

// drain sends a peer's queue in order until it is empty (returns 0) or a send fails
// (returns when to retry, unix ms).
func (s *Service) drain(peer string, force bool) int64 {
	for s.ctx.Err() == nil {
		it, err := s.st.NextOutbox(peer)
		if errors.Is(err, store.ErrNotFound) {
			return 0
		}
		if err != nil {
			s.log.Error("chat: outbox", "err", err)
			return model.Now() + s.o.RetryMax.Milliseconds()
		}
		now := model.Now()
		if now-it.CreatedAt > s.o.OutboxTTL.Milliseconds() {
			s.failItems([]store.OutboxItem{it})
			continue
		}
		if !force && it.NextAt > now {
			return it.NextAt
		}
		var env p2p.Envelope
		if err := json.Unmarshal(it.Envelope, &env); err != nil {
			s.log.Error("chat: bad outbox item", "seq", it.Seq, "err", err)
			_ = s.st.DeleteOutbox(it.Seq)
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, s.o.SendTimeout)
		err = s.node.Send(ctx, peer, env)
		cancel()
		if err != nil {
			if s.ctx.Err() != nil {
				return 0
			}
			attempts := it.Attempts + 1
			delay := s.o.RetryMin << min(attempts-1, 20)
			if delay > s.o.RetryMax || delay <= 0 {
				delay = s.o.RetryMax
			}
			next := model.Now() + delay.Milliseconds()
			if !errors.Is(err, p2p.ErrUnreachable) {
				s.log.Info("chat: send failed", "peer", peer, "type", env.Type, "err", err)
			}
			_ = s.st.RetryOutboxLater(it.Seq, attempts, next)
			return next
		}
		force = false
		if err := s.st.DeleteOutbox(it.Seq); err != nil {
			s.log.Error("chat: outbox", "err", err)
		}
		if it.MessageID != "" {
			s.afterSent(it.ChatID, it.MessageID)
		}
	}
	return 0
}

// afterSent moves one of our messages from queued to sent once no copy is left queued.
func (s *Service) afterSent(chatID, msgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshStateLocked(chatID, msgID)
	s.offerSentLocked(chatID, msgID) // admin.go: an admin offer is tagged with its ID
}

// refreshStateLocked recomputes an outgoing message's state from the outbox and receipts.
// It never moves a message backwards (delivered stays delivered) and leaves failed alone.
func (s *Service) refreshStateLocked(chatID, msgID string) {
	m, err := s.st.GetChatMessage(chatID, msgID)
	if err != nil || m.Sender != model.ChatMe || m.State == model.ChatFailed {
		return
	}
	queued, err := s.st.CountOutboxFor(chatID, msgID)
	if err != nil {
		return
	}
	st := m.State
	switch {
	case m.Recipients > 0 && m.Delivered >= m.Recipients:
		st = model.ChatDelivered
	case queued == 0 && m.State == model.ChatQueued:
		st = model.ChatSent
	}
	if st != m.State {
		m.State = st
		_ = s.st.SetChatMessageState(chatID, msgID, st)
		s.publish(model.EvChatMessage, m)
	}
}

// failItems marks the messages carried by dropped outbox items as failed.
func (s *Service) failItems(items []store.OutboxItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range items {
		_ = s.st.DeleteOutbox(it.Seq)
		if it.MessageID == "" {
			continue
		}
		m, err := s.st.GetChatMessage(it.ChatID, it.MessageID)
		if err != nil || m.State == model.ChatDelivered || m.State == model.ChatFailed {
			continue
		}
		m.State = model.ChatFailed
		_ = s.st.SetChatMessageState(it.ChatID, it.MessageID, model.ChatFailed)
		s.publish(model.EvChatMessage, m)
	}
}
