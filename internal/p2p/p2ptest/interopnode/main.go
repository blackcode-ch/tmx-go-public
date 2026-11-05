// Command interopnode drives one side of a cross-build peer-to-peer interop check (the
// Common Core CI test, tmx-lead 3 Nov): built once from this repo's internal/p2p and once
// from blackcode-ch/tmx-go's `main` at a pinned SHA, run as two separate OS processes so each
// side genuinely runs its own compiled code, with no package-level sharing between them.
//
// Protocol with the driving test: one line of status on stdout per step, then exit 0 on
// success or exit 1 after printing "ERROR <message>". Flags select the role; the inviter
// prints its invite link as "INVITE <link>" as soon as it is ready.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

const (
	msgChatID = "interop-chat"
	fileID    = "interop-file"
	fileBody  = "tmx peer-to-peer interop check\n"
)

func main() {
	role := flag.String("role", "", "inviter or joiner")
	relay := flag.String("relay", "", "relay multiaddr (with /p2p/<id>)")
	link := flag.String("link", "", "invite link (joiner only)")
	joinAt := flag.String("join-at", "", "joiner: RFC3339Nano timestamp to wait for before Join (tests an admission window; measured from the inviter's own CREATED_AT, not process-start timing)")
	budget := flag.Duration("budget", 2*time.Minute, "how long this process waits overall, from its own start")
	stateDir := flag.String("state-dir", "", "directory for the node's identity/state files")
	flag.Parse()

	var at time.Time
	if *joinAt != "" {
		var err error
		at, err = time.Parse(time.RFC3339Nano, *joinAt)
		if err != nil {
			fmt.Println("ERROR -join-at: " + err.Error())
			os.Exit(1)
		}
	}
	if err := run(*role, *relay, *link, at, *budget, *stateDir); err != nil {
		fmt.Println("ERROR " + err.Error())
		os.Exit(1)
	}
}

func run(role, relayAddr, link string, joinAt time.Time, budget time.Duration, stateDir string) error {
	if stateDir == "" {
		return fmt.Errorf("-state-dir is required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	key, err := p2p.LoadOrCreateKey(filepath.Join(stateDir, "p2p-key"))
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	var relays []string
	if relayAddr != "" {
		relays = []string{relayAddr}
	}
	h, err := p2p.New(p2p.Options{
		Key:           key,
		StateFile:     filepath.Join(stateDir, "p2p-state.json"),
		Name:          role,
		Version:       "interopnode",
		ListenAddrs:   []string{"/ip4/127.0.0.1/udp/0/quic-v1", "/ip4/127.0.0.1/tcp/0"},
		AllowLoopback: true,
		DHT:           false,
		Relays:        relays,
	})
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	defer h.Close()

	h.ServeFiles(func(req p2p.FileRequest) (io.ReadCloser, int64, error) {
		if req.ChatID != msgChatID || req.FileID != fileID {
			return nil, 0, fmt.Errorf("unknown file %s/%s", req.ChatID, req.FileID)
		}
		return io.NopCloser(stringsReader(fileBody)), int64(len(fileBody)), nil
	})

	// budget is from THIS process's own start: the driving test passes a generous one on
	// both sides of a deliberately-late join (an inviter waiting for a late joiner needs the
	// same headroom as the joiner itself, even though only the joiner has a -join-at).
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	switch role {
	case "inviter":
		return runInviter(ctx, h)
	case "joiner":
		return runJoiner(ctx, h, link, joinAt)
	default:
		return fmt.Errorf("-role must be inviter or joiner")
	}
}

func runInviter(ctx context.Context, h *p2p.Host) error {
	created := time.Now()
	inv, err := h.CreateInvite(p2p.InviteOptions{Kind: p2p.InviteDM})
	if err != nil {
		return fmt.Errorf("create invite: %w", err)
	}
	// Printed so the driving test can compute the joiner's -join-at relative to the REAL
	// invite creation moment, not an estimate from process-start timing (tmx-lead, 3 Nov: a
	// security-property boundary test must measure from the invite's own clock, not ours).
	fmt.Println("CREATED_AT " + created.Format(time.RFC3339Nano))
	fmt.Println("INVITE " + inv.Link)

	var peerID string
	select {
	case p := <-h.Paired():
		peerID = p.PeerID
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting to pair: %w", ctx.Err())
	}
	return exchange(ctx, h, peerID)
}

func runJoiner(ctx context.Context, h *p2p.Host, link string, joinAt time.Time) error {
	if link == "" {
		return fmt.Errorf("-link is required for the joiner")
	}
	if !joinAt.IsZero() {
		if wait := time.Until(joinAt); wait > 0 {
			fmt.Printf("WAITING %s\n", wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	p, err := h.Join(ctx, link)
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	return exchange(ctx, h, p.PeerID)
}

// exchange sends one message and fetches one file from peerID, then waits for the same from
// the other side: a minimal, symmetric exercise of the message and file protocols.
func exchange(ctx context.Context, h *p2p.Host, peerID string) error {
	env := p2p.Envelope{ID: randomHex(), Type: "interop", ChatID: msgChatID, Body: []byte("hello from interop")}
	if err := h.Send(ctx, peerID, env); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	fmt.Println("SENT_MSG")

	var buf writeBuf
	if _, err := h.Fetch(ctx, peerID, msgChatID, fileID, &buf, 1<<20); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if buf.String() != fileBody {
		return fmt.Errorf("fetched file content mismatch: %q", buf.String())
	}
	fmt.Println("FETCHED_FILE")

	select {
	case got := <-h.Incoming():
		if got.ChatID != msgChatID {
			return fmt.Errorf("unexpected incoming chat_id %q", got.ChatID)
		}
		fmt.Println("RECEIVED_MSG")
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for the peer's message: %w", ctx.Err())
	}

	fmt.Println("DONE")
	// Stay up until the driving test confirms BOTH sides are done (a "CLOSE" line on stdin),
	// so this process's h.Close() can never race the peer's still-in-flight Fetch of OUR file
	// (go-shells-s / tmx-lead, 3 Nov: the real cause of the first interop CI failure -
	// "Application error 0x0 (remote)" was this process's own clean close, not a degraded UDP
	// buffer).
	waitForClose(ctx)
	return nil
}

// waitForClose blocks until a line arrives on stdin or ctx expires, whichever first: the
// driving test writes one line once it has seen "DONE" from every harness in this run.
func waitForClose(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

type writeBuf struct{ b []byte }

func (w *writeBuf) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *writeBuf) String() string              { return string(w.b) }

func stringsReader(s string) io.Reader { return &stringReader{s: s} }

type stringReader struct {
	s   string
	pos int
}

func (r *stringReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}

func randomHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	const hex = "0123456789abcdef"
	out := make([]byte, 16)
	for i, c := range b {
		out[i*2] = hex[c>>4]
		out[i*2+1] = hex[c&0xf]
	}
	return string(out)
}
