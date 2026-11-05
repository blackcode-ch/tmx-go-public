package p2p

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// A joiner can be descheduled after the inviter answers, before it records the
// peer. Publishing Paired during that window lets the inviter's chat layer Send
// to a host that still rejects it as unpaired. Join closes the pair stream only
// after recordPeer, so its EOF is the existing protocol's readiness signal.
func TestTrafficAfterPairEventWaitsForJoinerReady(t *testing.T) {
	inviter := startNode(t, "inviter", nil, "")
	joiner := startNode(t, "joiner", nil, "")
	inv, err := inviter.CreateInvite(InviteOptions{Kind: InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	p, err := parseLink(inv.Link)
	if err != nil {
		t.Fatal(err)
	}
	if err := joiner.h.Connect(ctxT(t), peer.AddrInfo{ID: p.PeerID, Addrs: p.Addrs}); err != nil {
		t.Fatal(err)
	}
	s, err := joiner.h.NewStream(network.WithAllowLimitedConn(ctxT(t), "pair-test"), p.PeerID, protoID(ProtoPair))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeMsg(s, pairRequest{V: 1, Proof: pairProof(p.Secret, p.PeerID, joiner.self), Name: "joiner"}); err != nil {
		t.Fatal(err)
	}
	var response pairResponse
	if err := readMsg(s, maxPairMsg, &response); err != nil || !response.OK {
		t.Fatalf("pair answer: %+v %v", response, err)
	}
	recv(t, inviter.Paired(), "inviter Paired")
	joiner.ServeFiles(func(FileRequest) (io.ReadCloser, int64, error) {
		return io.NopCloser(strings.NewReader("ready")), 5, nil
	})
	fetched := make(chan error, 1)
	var data bytes.Buffer
	go func() { _, err := inviter.Fetch(ctxT(t), joiner.PeerID(), "chat", "file", &data, 1024); fetched <- err }()
	sent := make(chan error, 1)
	go func() { sent <- inviter.Send(ctxT(t), joiner.PeerID(), Envelope{ID: "early", Type: "msg"}) }()
	select {
	case err := <-sent:
		t.Fatalf("Send returned before joiner recorded the inviter: %v", err)
	case err := <-fetched:
		t.Fatalf("Fetch returned before joiner recorded the inviter: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	joiner.recordPeer(p.PeerID, response.Name, p.Kind, "")
	_ = s.Close()
	if err := recv(t, sent, "deferred Send"); err != nil {
		t.Fatal(err)
	}
	if err := recv(t, fetched, "deferred Fetch"); err != nil || data.String() != "ready" {
		t.Fatalf("Fetch: %v data=%q", err, data.String())
	}
	recv(t, joiner.Incoming(), "early message delivered")
	send(t, inviter, joiner, "dm", "ready")
}

// Older inviters send as soon as they emit Paired, before answering the Join.
// Delay that answer explicitly: inbound traffic must wait, then recheck trust.
func TestIncomingTrafficWaitsForJoinResult(t *testing.T) {
	for _, accept := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted", false: "refused"}[accept], func(t *testing.T) {
			inviter := startNode(t, "old-inviter", nil, "")
			joiner := startNode(t, "joiner", nil, "")
			inv, err := inviter.CreateInvite(InviteOptions{Kind: InviteDM})
			if err != nil {
				t.Fatal(err)
			}
			requested := make(chan struct{})
			answer := make(chan struct{})
			inviter.h.SetStreamHandler(protoID(ProtoPair), func(s network.Stream) {
				defer s.Close()
				_ = s.SetDeadline(time.Now().Add(5 * time.Second))
				var req pairRequest
				if err := readMsg(s, maxPairMsg, &req); err != nil {
					return
				}
				inviter.recordPeer(s.Conn().RemotePeer(), req.Name, InviteDM, "")
				close(requested)
				select {
				case <-answer:
				case <-inviter.ctx.Done():
					return
				}
				_ = writeMsg(s, pairResponse{OK: accept, Name: "inviter", Error: "refused for test"})
				_ = s.CloseWrite()
				_, _ = io.Copy(io.Discard, io.LimitReader(s, 1))
			})
			joiner.ServeFiles(func(FileRequest) (io.ReadCloser, int64, error) {
				return io.NopCloser(strings.NewReader("ready")), 5, nil
			})
			joined := make(chan error, 1)
			go func() { _, err := joiner.Join(ctxT(t), inv.Link); joined <- err }()
			recv(t, requested, "join request")
			sent := make(chan error, 1)
			fetched := make(chan error, 1)
			go func() { sent <- inviter.Send(ctxT(t), joiner.PeerID(), Envelope{ID: "early", Type: "msg"}) }()
			var data bytes.Buffer
			go func() { _, err := inviter.Fetch(ctxT(t), joiner.PeerID(), "chat", "file", &data, 1024); fetched <- err }()
			select {
			case err := <-sent:
				t.Fatalf("Send answered before the Join result: %v", err)
			case err := <-fetched:
				t.Fatalf("Fetch answered before the Join result: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(answer)
			joinErr := recv(t, joined, "Join result")
			sendErr := recv(t, sent, "Send result")
			fetchErr := recv(t, fetched, "Fetch result")
			if accept {
				if joinErr != nil || sendErr != nil || fetchErr != nil || data.String() != "ready" {
					t.Fatalf("accepted: join=%v send=%v fetch=%v data=%q", joinErr, sendErr, fetchErr, data.String())
				}
				recv(t, joiner.Incoming(), "message")
			} else {
				if joinErr == nil || (!errors.Is(sendErr, ErrRejected) && !errors.Is(sendErr, ErrUnreachable)) || (!errors.Is(fetchErr, ErrNoFile) && !errors.Is(fetchErr, ErrUnreachable)) {
					t.Fatalf("refused: join=%v send=%v fetch=%v", joinErr, sendErr, fetchErr)
				}
				if joiner.isPeer(inviter.self) {
					t.Fatal("failed Join granted peer trust")
				}
				select {
				case <-joiner.Incoming():
					t.Fatal("failed Join accepted an envelope")
				default:
				}
			}
		})
	}
}

func TestPairReadinessCancellationAndOverlappingStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &Host{ctx: ctx}
	p := newPeerID(t)
	n.beginPairReadyLocked(p)
	n.beginPairReadyLocked(p)
	n.endPairReadyLocked(p)
	operation, stop := context.WithCancel(context.Background())
	stop()
	if err := n.waitPairReady(operation, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("operation cancellation: %v", err)
	}
	cancel()
	if err := n.waitPairReady(context.Background(), p); !errors.Is(err, context.Canceled) {
		t.Fatalf("host shutdown: %v", err)
	}
	n.endPairReadyLocked(p)
	if len(n.pairReady) != 0 {
		t.Fatal("completed pairing retained")
	}
	if err := n.waitPairReady(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func TestPairReadinessInboundScopeAndLimit(t *testing.T) {
	n := &Host{ctx: context.Background()}
	p, stranger := newPeerID(t), newPeerID(t)
	n.beginPairReadyLocked(p)
	n.pairReady[p].outgoing = 1
	// An unrelated peer never waits on the inviter's Join.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.waitIncomingPairReady(ctx, stranger); err != nil {
		t.Fatalf("unrelated peer waited: %v", err)
	}
	// Proof-checked incoming pairing alone does not enable inbound waiting.
	n.beginPairReadyLocked(stranger)
	if err := n.waitIncomingPairReady(ctx, stranger); err != nil {
		t.Fatalf("incoming-only peer waited: %v", err)
	}
	n.endPairReadyLocked(stranger)
	results := make(chan error, maxPairReadyWaiters)
	for i := 0; i < maxPairReadyWaiters; i++ {
		go func() { results <- n.waitIncomingPairReady(context.Background(), p) }()
	}
	deadline := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		count := n.pairReady[p].waiters
		n.mu.Unlock()
		if count == maxPairReadyWaiters {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiters did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := n.waitIncomingPairReady(context.Background(), p); err == nil {
		t.Fatal("excess inbound waiter admitted")
	}
	n.mu.Lock()
	progress := n.pairReady[p]
	n.endPairReadyLocked(p)
	n.mu.Unlock()
	for i := 0; i < maxPairReadyWaiters; i++ {
		if err := recv(t, results, "released waiter"); err != nil {
			t.Fatal(err)
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if progress.waiters != 0 {
		t.Fatal("waiter slots leaked")
	}
}
