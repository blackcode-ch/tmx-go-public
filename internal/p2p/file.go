package p2p

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// /tmx/file/1.0.0: the receiver of a chat message pulls each attached file from a peer
// that has it, one stream per file (envelopes stay small; files never share the msg streams).
//
// Wire format (varint-length-prefixed JSON, like /tmx/pair and /tmx/msg):
//
//	requester → server: fileRequest {chat_id, file_id}   (ids ≤ 128 bytes, message ≤ 1 KiB),
//	                    then the requester closes its write side
//	server → requester: fileHeader {ok, size, error?, retry?}   (≤ 4 KiB)
//	                    then, when ok, exactly size raw bytes, then the stream closes
//
// Only paired or trusted peers are served (the same rule as envelopes). The server asks the
// FileSource with the connection's authenticated peer ID; any error from it → ok:false (the
// requester gets ErrNoFile). retry:true means "not now" (busy, rate limited, or a relayed
// connection for a file that is too large for it) → the requester gets ErrUnreachable.
//
// Relays: circuit-relay connections are "limited" (public relays cap a circuit at ~128 KiB
// and 2 minutes), so Fetch first tries for a direct connection (a direct dial of the peer's
// known addresses, then a short wait while hole punching on a fresh relayed connection may
// still finish). Over a connection that is still limited only files of at most
// maxRelayedFile pass; larger ones → ErrUnreachable, so the caller retries later, when a
// direct connection may exist. Both sides enforce this (the server answers retry before
// sending a body over a limited connection).
const (
	maxFileRequest    = 1 << 10
	maxFileHeader     = 4 << 10
	maxRelayedFile    = 64 << 10
	fileHeaderTimeout = 20 * time.Second // open the stream, send the request, get the header
	fileIdleTimeout   = 30 * time.Second // no byte for this long → the transfer is dead
	fileMaxDuration   = time.Hour        // server backstop for one stream (a trickling reader)
	directWait        = 5 * time.Second  // how long Fetch tries to turn a relayed connection direct
	freshRelayed      = 10 * time.Second // a relayed connection this young may still be hole punched
	filesPerPeer      = 2                // concurrent file streams served to one peer
	filesOverall      = 8                // and to everyone
	fileRatePerMinute = 120              // file requests per peer per minute
	fileChunk         = 64 << 10
)

type fileRequest struct {
	ChatID string `json:"chat_id"`
	FileID string `json:"file_id"`
}

type fileHeader struct {
	OK    bool   `json:"ok"`
	Size  int64  `json:"size,omitempty"`
	Error string `json:"error,omitempty"`
	Retry bool   `json:"retry,omitempty"` // busy, rate limited or needs a direct connection
}

func checkFileIDs(chatID, fileID string) error {
	switch {
	case fileID == "" || len(fileID) > maxIDLen:
		return errors.New("file request needs a file ID (at most 128 bytes)")
	case len(chatID) > maxIDLen:
		return errors.New("chat ID too long")
	}
	return nil
}

// relayedOK reports whether a file of size may cross a connection that is (or is not) limited.
func relayedOK(limited bool, size int64) bool { return !limited || size <= maxRelayedFile }

// ServeFiles sets what answers file requests from paired or trusted peers (nil refuses all).
func (n *Host) ServeFiles(src FileSource) {
	n.mu.Lock()
	n.files = src
	n.mu.Unlock()
}

// takeFileSlot reserves one of the concurrent file streams for p (filesPerPeer, filesOverall).
func (n *Host) takeFileSlot(p peer.ID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.filesActive[p] >= filesPerPeer || n.filesTotal >= filesOverall {
		return false
	}
	n.filesActive[p]++
	n.filesTotal++
	return true
}

func (n *Host) releaseFileSlot(p peer.ID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.filesActive[p]--; n.filesActive[p] <= 0 {
		delete(n.filesActive, p)
	}
	n.filesTotal--
}

func (n *Host) handleFile(s network.Stream) {
	defer s.Close()
	remote := s.Conn().RemotePeer()
	_ = s.SetDeadline(time.Now().Add(streamTimeout))
	reply := func(h fileHeader) { _ = writeMsg(s, h) }
	if !n.isPeer(remote) {
		reply(fileHeader{Error: "not paired with this tmx"})
		return
	}
	if !n.limiter.allow("file:"+remote.String(), fileRatePerMinute, time.Minute) {
		reply(fileHeader{Error: "too many file requests; slow down", Retry: true})
		return
	}
	if !n.takeFileSlot(remote) {
		reply(fileHeader{Error: "busy sending other files", Retry: true})
		return
	}
	defer n.releaseFileSlot(remote)
	var req fileRequest
	if err := readMsg(s, maxFileRequest, &req); err != nil {
		s.Reset()
		return
	}
	if err := checkFileIDs(req.ChatID, req.FileID); err != nil {
		reply(fileHeader{Error: err.Error()})
		return
	}
	n.mu.Lock()
	src := n.files
	n.mu.Unlock()
	if src == nil {
		reply(fileHeader{Error: "this tmx shares no files"})
		return
	}
	r, size, err := src(FileRequest{PeerID: remote.String(), ChatID: req.ChatID, FileID: req.FileID})
	if err != nil {
		// The source's reason may name local paths: log it, answer generically.
		n.log.Debug("p2p: file refused", "peer", remote.String(), "chat", req.ChatID, "file", req.FileID, "err", err)
		reply(fileHeader{Error: "file not available"})
		return
	}
	defer r.Close()
	if size < 0 {
		reply(fileHeader{Error: "file not available"})
		return
	}
	if !relayedOK(s.Conn().Stat().Limited, size) {
		reply(fileHeader{Error: "file needs a direct connection", Retry: true})
		return
	}
	if err := writeMsg(s, fileHeader{OK: true, Size: size}); err != nil {
		s.Reset()
		return
	}
	end := time.Now().Add(fileMaxDuration)
	buf := make([]byte, fileChunk)
	var sent int64
	for sent < size {
		k, rerr := r.Read(buf[:min(int64(len(buf)), size-sent)])
		if k > 0 {
			dl := time.Now().Add(fileIdleTimeout)
			if end.Before(dl) {
				dl = end
			}
			_ = s.SetWriteDeadline(dl)
			if _, err := s.Write(buf[:k]); err != nil {
				s.Reset()
				return
			}
			sent += int64(k)
		}
		if rerr != nil && sent < size {
			// The source ended early or failed: reset, so the requester sees a broken transfer.
			n.log.Warn("p2p: file source ended early", "peer", remote.String(), "file", req.FileID, "sent", sent, "size", size, "err", rerr)
			s.Reset()
			return
		}
	}
	_ = s.SetWriteDeadline(time.Now().Add(fileIdleTimeout))
	if err := s.CloseWrite(); err != nil {
		s.Reset()
		return
	}
	n.log.Debug("p2p: file served", "peer", remote.String(), "file", req.FileID, "size", size)
}

// Fetch downloads file fileID of chatID from peerID into w (at most maxBytes) over
// /tmx/file/1.0.0 and returns the bytes written. Errors: ErrUnreachable (not connected,
// busy, rate limited, only a relayed connection for a large file, or the transfer broke
// off: retry later), ErrNoFile (not paired, or the peer does not have it or refuses),
// ErrTooBig (the header announced more than maxBytes; nothing is read), ctx's error, or
// w's write error.
func (n *Host) Fetch(ctx context.Context, peerID, chatID, fileID string, w io.Writer, maxBytes int64) (int64, error) {
	p, err := peer.Decode(peerID)
	if err != nil {
		return 0, fmt.Errorf("not a peer ID: %q", peerID)
	}
	if p == n.self {
		return 0, errors.New("cannot fetch from this node itself")
	}
	if err := checkFileIDs(chatID, fileID); err != nil {
		return 0, err
	}
	if !n.isPeer(p) {
		return 0, fmt.Errorf("%w: %s is not a paired peer", ErrNoFile, peerID)
	}
	if err := n.connect(ctx, p); err != nil {
		return 0, fmt.Errorf("%w: %s", ErrUnreachable, shortErr(err))
	}
	n.preferDirect(ctx, p)

	// One retry when the connection or stream setup closes during setup (*network.ConnError
	// or *network.StreamError - e.g. a small UDP buffer failing a fresh QUIC connection; a
	// real race on real networks, confirmed in the p2p Common Core interop CI, 3 Nov). Never
	// retried for anything else: a header the peer actually answered (refused, busy, too big)
	// is final. ClosePeer before the retry's connect: the failed connection may still be the
	// one n.h.Network() reports as Connected (torn down asynchronously), in which case
	// connect's own Connectedness check would be a no-op and NewStream would open on the same
	// dead connection again; closing it here forces a genuinely fresh dial.
	s, h, err := n.openFileStream(ctx, p, chatID, fileID)
	if err != nil && isConnSetupError(err) {
		_ = n.h.Network().ClosePeer(p)
		if cerr := n.connect(ctx, p); cerr == nil {
			s, h, err = n.openFileStream(ctx, p, chatID, fileID)
		}
	}
	if err != nil {
		return 0, err
	}
	defer s.Close()

	switch {
	case !h.OK && h.Retry:
		s.Reset()
		return 0, fmt.Errorf("%w: %s", ErrUnreachable, h.Error)
	case !h.OK:
		s.Reset()
		return 0, fmt.Errorf("%w: %s", ErrNoFile, h.Error)
	case h.Size < 0:
		s.Reset()
		return 0, fmt.Errorf("p2p: bad file header (size %d)", h.Size)
	case h.Size > maxBytes:
		s.Reset()
		return 0, fmt.Errorf("%w: %d bytes, limit %d", ErrTooBig, h.Size, maxBytes)
	case !relayedOK(s.Conn().Stat().Limited, h.Size):
		s.Reset()
		return 0, fmt.Errorf("%w: a %d-byte file needs a direct connection (only relayed now)", ErrUnreachable, h.Size)
	}

	// Body: exactly h.Size bytes. No total deadline (a slow transfer that keeps moving is
	// fine); each read must arrive within fileIdleTimeout. ctx can still bound the total.
	var got int64
	buf := make([]byte, fileChunk)
	for got < h.Size {
		dl := time.Now().Add(fileIdleTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(dl) {
			dl = d
		}
		_ = s.SetReadDeadline(dl)
		k, rerr := s.Read(buf[:min(int64(len(buf)), h.Size-got)])
		if k > 0 {
			wk, werr := w.Write(buf[:k])
			got += int64(wk)
			if werr == nil && wk < k {
				werr = io.ErrShortWrite
			}
			if werr != nil {
				s.Reset()
				return got, werr
			}
		}
		if rerr != nil && got < h.Size {
			s.Reset()
			if ctx.Err() != nil {
				return got, ctx.Err()
			}
			if rerr == io.EOF {
				rerr = io.ErrUnexpectedEOF
			}
			return got, fmt.Errorf("%w: transfer broke off after %d of %d bytes: %s", ErrUnreachable, got, h.Size, shortErr(rerr))
		}
	}
	return got, nil
}

// openFileStreamFn is overridden in tests to inject a *network.ConnError/*network.StreamError
// on the opener's first call, without racing a real network connection in-process; nil (the
// production default) calls the real implementation.
var openFileStreamFn func(n *Host, ctx context.Context, p peer.ID, chatID, fileID string) (network.Stream, fileHeader, error)

func (n *Host) openFileStream(ctx context.Context, p peer.ID, chatID, fileID string) (network.Stream, fileHeader, error) {
	if openFileStreamFn != nil {
		return openFileStreamFn(n, ctx, p, chatID, fileID)
	}
	return n.openFileStreamReal(ctx, p, chatID, fileID)
}

// openFileStreamReal opens the /tmx/file/1.0.0 stream to p, sends the request and reads the
// header: Fetch's one retryable step (see Fetch's ConnError check). On any error the stream
// is already reset/closed; the caller never needs to clean it up.
func (n *Host) openFileStreamReal(ctx context.Context, p peer.ID, chatID, fileID string) (network.Stream, fileHeader, error) {
	hctx, cancel := context.WithTimeout(network.WithAllowLimitedConn(ctx, "tmx-file"), fileHeaderTimeout)
	defer cancel()
	s, err := n.h.NewStream(hctx, p, protoID(ProtoFile))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fileHeader{}, ctx.Err()
		}
		// %w on err (not shortErr(err), a string): Fetch's retry needs the underlying
		// *network.ConnError out of errors.As, same as a caller checking ErrUnreachable.
		return nil, fileHeader{}, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	// Cancelling ctx aborts the transfer at once (reads would otherwise wait for a deadline).
	stop := context.AfterFunc(ctx, func() { s.Reset() })
	defer stop()
	dl := time.Now().Add(fileHeaderTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = s.SetDeadline(dl)
	broken := func(what string, err error) (network.Stream, fileHeader, error) {
		s.Reset()
		if ctx.Err() != nil {
			return nil, fileHeader{}, ctx.Err()
		}
		// %w on err (not shortErr(err), a string): the retry needs the underlying
		// *network.ConnError out of errors.As, not just the ErrUnreachable sentinel.
		return nil, fileHeader{}, fmt.Errorf("%w: %s: %w", ErrUnreachable, what, err)
	}
	if err := writeMsg(s, fileRequest{ChatID: chatID, FileID: fileID}); err != nil {
		return broken("file request", err)
	}
	_ = s.CloseWrite()
	var h fileHeader
	if err := readMsg(s, maxFileHeader, &h); err != nil {
		return broken("no file header", err)
	}
	return s, h, nil
}

// isConnSetupError reports whether err is a transport-level connection or stream closure
// (*network.ConnError, *network.StreamError) rather than an application-level answer: the
// class Fetch's one retry covers.
func isConnSetupError(err error) bool {
	var ce *network.ConnError
	var se *network.StreamError
	return errors.As(err, &ce) || errors.As(err, &se)
}

// preferDirect tries to turn a relay-only connection to p into a direct one before a file
// transfer: a direct dial of p's known addresses, then, while the relayed connection is
// fresh (hole punching starts when it opens and takes a few seconds), a short wait for the
// direct connection to appear. It gives up after directWait; the caller then uses whatever
// connection there is.
func (n *Host) preferDirect(ctx context.Context, p peer.ID) {
	if n.peerReach(p) != Relayed {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, directWait)
	defer cancel()
	_ = n.h.Connect(network.WithForceDirectDial(wctx, "tmx-file"), peer.AddrInfo{ID: p, Addrs: n.h.Peerstore().Addrs(p)})
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for n.peerReach(p) == Relayed && n.freshRelayed(p) {
		select {
		case <-wctx.Done():
			return
		case <-t.C:
		}
	}
}

// freshRelayed: some relayed connection to p opened less than freshRelayed ago.
func (n *Host) freshRelayed(p peer.ID) bool {
	for _, c := range n.h.Network().ConnsToPeer(p) {
		if c.Stat().Limited && time.Since(c.Stat().Opened) < freshRelayed {
			return true
		}
	}
	return false
}
