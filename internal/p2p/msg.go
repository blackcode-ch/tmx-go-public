package p2p

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// MaxBody is the largest Envelope.Body Send accepts (a 32 KB chat message plus metadata).
	MaxBody = 48 << 10
	// maxEnvelopeMsg bounds one envelope on the wire (base64 body + fields).
	maxEnvelopeMsg = 96 << 10
	maxIDLen       = 128
	maxTypeLen     = 64
	incomingWait   = 5 * time.Second
	// Per-peer inbound rate: generous for chat, low enough that one peer cannot flood the agents.
	ratePerMinute = 120
)

// ErrRejected: the peer answered but refused the envelope (not paired there, bad signature,
// too large). Retrying the same envelope will not help.
var ErrRejected = errors.New("peer refused the envelope")

type ack struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Retry bool   `json:"retry,omitempty"` // busy or rate limited: try again later
}

// signedBytes is the canonical form an envelope's signature covers: a domain tag, then each
// field length-prefixed (so no field can bleed into the next), Sig excluded.
func signedBytes(e Envelope) []byte {
	b := []byte("tmx-envelope-v1\x00")
	for _, f := range []string{e.ID, e.Type, e.ChatID, e.From} {
		b = binary.AppendUvarint(b, uint64(len(f)))
		b = append(b, f...)
	}
	b = binary.BigEndian.AppendUint64(b, uint64(e.At))
	b = binary.AppendUvarint(b, uint64(len(e.Body)))
	return append(b, e.Body...)
}

// SignEnvelope fills From/At (if zero) and Sig with key.
func SignEnvelope(key crypto.PrivKey, e Envelope) (Envelope, error) {
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return e, err
	}
	e.From = id.String()
	if e.At == 0 {
		e.At = nowMs()
	}
	e.Sig, err = key.Sign(signedBytes(e))
	return e, err
}

// VerifyEnvelope checks that e.Sig is From's signature over the envelope.
func VerifyEnvelope(e Envelope) error {
	from, err := peer.Decode(e.From)
	if err != nil {
		return fmt.Errorf("bad sender %q", e.From)
	}
	pub, err := from.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("sender key: %w", err)
	}
	ok, err := pub.Verify(signedBytes(e), e.Sig)
	if err != nil || !ok {
		return errors.New("bad signature")
	}
	return nil
}

func checkEnvelope(e Envelope) error {
	switch {
	case e.ID == "" || len(e.ID) > maxIDLen:
		return errors.New("envelope needs an ID (at most 128 bytes)")
	case e.Type == "" || len(e.Type) > maxTypeLen:
		return errors.New("envelope needs a type (at most 64 bytes)")
	case len(e.ChatID) > maxIDLen:
		return errors.New("chat ID too long")
	case len(e.Body) > MaxBody:
		return fmt.Errorf("envelope body larger than %d KB", MaxBody>>10)
	}
	return nil
}

// Sign signs env with this node's key without sending it (see Node.Sign).
func (n *Host) Sign(env Envelope) (Envelope, error) {
	env.From, env.Sig = "", nil
	if err := checkEnvelope(env); err != nil {
		return env, err
	}
	return SignEnvelope(n.key, env)
}

// VerifyEnvelope checks an envelope signed by any peer (see Node.VerifyEnvelope).
func (n *Host) VerifyEnvelope(env Envelope) error { return VerifyEnvelope(env) }

// Send signs env and delivers it to peerID over one /tmx/msg/1.0.0 stream, waiting for the
// peer's ack. Unreachable, busy or rate-limited → an error wrapping ErrUnreachable (queue
// and retry); refused → ErrRejected. Relayed connections are allowed (envelopes are small).
func (n *Host) Send(ctx context.Context, peerID string, env Envelope) error {
	p, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("not a peer ID: %q", peerID)
	}
	if p == n.self {
		return errors.New("cannot send to this node itself")
	}
	if !n.isPeer(p) {
		return fmt.Errorf("%w: %s is not a paired peer", ErrRejected, peerID)
	}
	env.From, env.Sig = "", nil
	if err := checkEnvelope(env); err != nil {
		return err
	}
	env, err = SignEnvelope(n.key, env)
	if err != nil {
		return err
	}
	if err := n.connect(ctx, p); err != nil {
		return fmt.Errorf("%w: %s", ErrUnreachable, shortErr(err))
	}
	sctx, cancel := context.WithTimeout(network.WithAllowLimitedConn(ctx, "tmx-msg"), streamTimeout)
	defer cancel()
	s, err := n.h.NewStream(sctx, p, protoID(ProtoMsg))
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUnreachable, shortErr(err))
	}
	defer s.Close()
	dl := time.Now().Add(streamTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = s.SetDeadline(dl)
	if err := writeMsg(s, env); err != nil {
		s.Reset()
		return fmt.Errorf("%w: %s", ErrUnreachable, shortErr(err))
	}
	var a ack
	if err := readMsg(s, maxPairMsg, &a); err != nil {
		s.Reset()
		return fmt.Errorf("%w: no ack: %s", ErrUnreachable, shortErr(err))
	}
	switch {
	case a.OK:
		return nil
	case a.Retry:
		return fmt.Errorf("%w: %s", ErrUnreachable, a.Error)
	default:
		return fmt.Errorf("%w: %s", ErrRejected, a.Error)
	}
}

func (n *Host) handleMsg(s network.Stream) {
	defer s.Close()
	remote := s.Conn().RemotePeer()
	_ = s.SetDeadline(time.Now().Add(streamTimeout))
	reply := func(a ack) { _ = writeMsg(s, a) }
	if !n.isPeer(remote) {
		reply(ack{Error: "not paired with this tmx"})
		return
	}
	var env Envelope
	if err := readMsg(s, maxEnvelopeMsg, &env); err != nil {
		s.Reset()
		return
	}
	if err := checkEnvelope(env); err != nil {
		reply(ack{Error: err.Error()})
		return
	}
	// The stream's remote peer is authenticated by Noise/TLS; the signature must be its own.
	if env.From != remote.String() {
		reply(ack{Error: "sender does not match the connection"})
		return
	}
	if err := VerifyEnvelope(env); err != nil {
		reply(ack{Error: err.Error()})
		return
	}
	if !n.limiter.allow("msg:"+remote.String(), ratePerMinute, time.Minute) {
		reply(ack{Error: "too many messages; slow down", Retry: true})
		return
	}
	t := time.NewTimer(incomingWait)
	defer t.Stop()
	select {
	case n.incoming <- env:
		reply(ack{OK: true})
	case <-t.C:
		reply(ack{Error: "busy", Retry: true})
	case <-n.ctx.Done():
		reply(ack{Error: "shutting down", Retry: true})
	}
}
