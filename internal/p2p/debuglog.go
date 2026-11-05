package p2p

import (
	"context"
	"sync"
	"time"
)

// LogEntry is one thing a Drain saw: a pairing, an envelope or a peer event.
type LogEntry struct {
	At       int64      `json:"at"`
	Kind     string     `json:"kind"` // paired | envelope | peer
	Paired   *Paired    `json:"paired,omitempty"`
	Envelope *Envelope  `json:"envelope,omitempty"`
	Peer     *PeerEvent `json:"peer,omitempty"`
}

// Drain consumes a Node's Paired, Incoming and PeerEvents channels into a ring buffer.
// It exists to test the network layer on its own (config "p2p": {"debug_drain": true});
// with the chat layer running it must stay off, or the two would split the channels.
type Drain struct {
	mu      sync.Mutex
	entries []LogEntry
	keep    int
}

// StartDrain runs until ctx ends.
func StartDrain(ctx context.Context, n Node, keep int) *Drain {
	d := &Drain{keep: keep}
	go func() {
		for {
			var e LogEntry
			select {
			case <-ctx.Done():
				return
			case p := <-n.Paired():
				e = LogEntry{Kind: "paired", Paired: &p}
			case env := <-n.Incoming():
				e = LogEntry{Kind: "envelope", Envelope: &env}
			case ev := <-n.PeerEvents():
				e = LogEntry{Kind: "peer", Peer: &ev}
			}
			e.At = time.Now().UnixMilli()
			d.mu.Lock()
			d.entries = append(d.entries, e)
			if len(d.entries) > d.keep {
				d.entries = d.entries[len(d.entries)-d.keep:]
			}
			d.mu.Unlock()
		}
	}()
	return d
}

// Entries returns what was seen, oldest first.
func (d *Drain) Entries() []LogEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]LogEntry{}, d.entries...)
}
