package p2p

import (
	"context"
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type pairProgress struct {
	active   int
	outgoing int
	waiters  int
	done     chan struct{}
}

const pairReadyTimeout = 5 * time.Second
const maxPairReadyWaiters = 4

// Both directions register before exposing the peer to application traffic:
// Join before dialing, and handlePair after proof verification but before Paired.
// No trust is granted here. Message/file operations still check isPeer after
// waiting, so a failed or cancelled Join cannot authorize an incoming envelope.
func (n *Host) beginPairReadyLocked(p peer.ID) {
	if n.pairReady == nil {
		n.pairReady = map[peer.ID]*pairProgress{}
	}
	progress := n.pairReady[p]
	if progress == nil {
		progress = &pairProgress{done: make(chan struct{})}
		n.pairReady[p] = progress
	}
	progress.active++
}

func (n *Host) endPairReadyLocked(p peer.ID) {
	progress := n.pairReady[p]
	progress.active--
	if progress.active == 0 {
		delete(n.pairReady, p)
		close(progress.done)
	}
}

// The pair stream's EOF follows Join's recordPeer. Waiting prevents a Paired
// consumer (including an older peer's consumer) from racing the other endpoint's
// peer record. It never holds the state lock while waiting, and honours both
// operation cancellation and host shutdown. Pair streams have their own deadline.
func (n *Host) waitPairReady(ctx context.Context, p peer.ID) error {
	ctx, cancel := context.WithTimeout(ctx, pairReadyTimeout)
	defer cancel()
	for {
		n.mu.Lock()
		progress := n.pairReady[p]
		n.mu.Unlock()
		if progress == nil {
			return nil
		}
		select {
		case <-progress.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-n.ctx.Done():
			return n.ctx.Err()
		}
	}
}

// Only the exact inviter of an outgoing Join may wait before becoming trusted.
// Bound retained inbound streams independently of the resource manager's limits.
func (n *Host) waitIncomingPairReady(ctx context.Context, p peer.ID) error {
	n.mu.Lock()
	progress := n.pairReady[p]
	if progress == nil || progress.outgoing == 0 {
		n.mu.Unlock()
		return nil
	}
	if progress.waiters >= maxPairReadyWaiters {
		n.mu.Unlock()
		return errors.New("too many streams waiting for pairing")
	}
	progress.waiters++
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		progress.waiters--
		n.mu.Unlock()
	}()
	return n.waitPairReady(ctx, p)
}
