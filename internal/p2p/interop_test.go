package p2p

// Common Core interop (tmx-lead, 3 Nov): our build of internal/p2p against
// blackcode-ch/tmx-go's `main` branch at a pinned commit (Mustneer's line, the source of the
// public releases). Two genuinely separate OS processes, each running its own compiled code -
// internal/p2p/p2ptest/interopnode is the harness; this test builds it twice (once from this
// checkout, once from a second checkout of the pinned commit, provided by the CI workflow via
// TMX_P2P_PINNED_CHECKOUT) and runs both over a local relay.
//
// Gated behind TMX_P2P_INTEROP=1: it compiles a second binary and (for
// TestInteropLateJoinAdmissionWindow) waits a real 2 minutes, neither of which belongs in the
// default `go test ./...` run. The dedicated CI job (.github/workflows/p2p-interop.yml) sets
// both env vars and runs only on PRs touching internal/p2p, pairing or the Core messages.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// pinnedCommit is blackcode-ch/tmx-go's `main` branch (Mustneer's line): the source of the
// public releases (tag v0.1.10 at the time this was pinned, 29 Oct 2026). Bump it along with
// re-running the Common Core diff when his line moves (tmx-lead's rule: any change to the
// Common Core surface needs this test green).
const pinnedCommit = "0c36197f03a09ef1fa3899c42de0a8e8d8541831"

func skipUnlessInterop(t *testing.T) {
	t.Helper()
	if os.Getenv("TMX_P2P_INTEROP") != "1" {
		t.Skip("set TMX_P2P_INTEROP=1 to run the cross-build Common Core interop check")
	}
}

// buildOurHarness builds interopnode from this checkout.
func buildOurHarness(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "interopnode-ours")
	cmd := hiddenexec.Command("go", "build", "-o", out, "./p2ptest/interopnode")
	cmd.Dir = "."
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build our harness: %v\n%s", err, b)
	}
	return out
}

// buildPinnedHarness builds interopnode from a checkout of blackcode-ch/tmx-go at
// pinnedCommit, copying this checkout's interopnode source into it first (the harness itself
// isn't part of his line; it only calls his internal/p2p's stable public API, which the
// Common Core diff covers), so the binary is compiled entirely from his module.
//
// The checkout itself is the CI workflow's job, not this test's: a second actions/checkout@v4
// step with ref: pinnedCommit handles auth itself (blackcode-ch/tmx-go is private) and never
// prints a token anywhere, unlike a git fetch this test ran itself. TMX_P2P_PINNED_CHECKOUT
// names that directory; a developer running this by hand checks out pinnedCommit there too.
func buildPinnedHarness(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("TMX_P2P_PINNED_CHECKOUT")
	if dir == "" {
		t.Skip("set TMX_P2P_PINNED_CHECKOUT to a checkout of blackcode-ch/tmx-go at pinnedCommit")
	}
	if got := pinnedHeadSHA(t, dir); got != pinnedCommit {
		t.Fatalf("TMX_P2P_PINNED_CHECKOUT is at %s, want %s (pinnedCommit)", got, pinnedCommit)
	}

	harnessDir := filepath.Join(dir, "internal", "p2p", "p2ptest", "interopnode")
	if err := os.MkdirAll(harnessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("p2ptest", "interopnode", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harnessDir, "main.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "interopnode-pinned")
	cmd := hiddenexec.Command("go", "build", "-o", out, "./internal/p2p/p2ptest/interopnode")
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build pinned harness: %v\n%s", err, b)
	}
	return out
}

func pinnedHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	cmd := hiddenexec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in %s: %v\n%s", dir, err, b)
	}
	return strings.TrimSpace(string(b))
}

// interopRelay is a plain loopback circuit-v2 relay (no tmx gater: it serves anyone, like a
// public IPFS relay would) that both harness binaries dial into.
func interopRelay(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	if _, err := relay.New(h); err != nil {
		t.Fatal(err)
	}
	return h
}

func relayMultiaddr(h host.Host) string {
	return h.Addrs()[0].String() + "/p2p/" + h.ID().String()
}

// runHarness starts one harness process and returns a scanner over its stdout plus a way to
// wait for it to exit. The caller reads lines until it sees what it needs, then waits.
type harnessProc struct {
	cmd    *exec.Cmd
	lines  *bufio.Scanner
	done   chan error
	stderr *strings.Builder
	stdout *strings.Builder // every stdout line seen so far, not just the one waitForLine matched on
	stdin  io.WriteCloser
}

// startHarness starts one role. budget is this process's own run-for timeout, from ITS start
// (not the invite's creation time - see joinAt for that); 0 defaults to the harness's own
// 2-minute default. joinAt, joiner-only, is an absolute RFC3339Nano instant to wait for before
// Join, measured against the inviter's own CREATED_AT line - never a duration computed from
// process-start timing, which the admission window itself does not use.
func startHarness(t *testing.T, bin, role, relayAddr, link string, budget time.Duration, joinAt time.Time) *harnessProc {
	t.Helper()
	args := []string{"-role", role, "-relay", relayAddr, "-state-dir", t.TempDir()}
	if link != "" {
		args = append(args, "-link", link)
	}
	if budget > 0 {
		args = append(args, "-budget", budget.String())
	}
	if !joinAt.IsZero() {
		args = append(args, "-join-at", joinAt.Format(time.RFC3339Nano))
	}
	cmd := hiddenexec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &harnessProc{cmd: cmd, lines: bufio.NewScanner(stdout), done: make(chan error, 1), stderr: &stderr, stdout: &strings.Builder{}, stdin: stdin}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return p
}

// exitStatus is a one-line, non-blocking read of whether the process has already exited and
// how - "still running" if waitForLine's failure fired for some other reason (the line it
// wanted never showed up, not that the process died). Puts the result straight back: nothing
// else reads p.done, and a second call (or Cmd.Wait's own guarantee against double-collection)
// must still see it.
func (p *harnessProc) exitStatus() string {
	select {
	case err := <-p.done:
		p.done <- err
		if err == nil {
			return "exited 0"
		}
		return err.Error()
	default:
		return "still running"
	}
}

// close tells the harness it may exit now (both sides are done: see waitForClose in
// p2ptest/interopnode/main.go - a process must never close its host while the peer's Fetch of
// its own file might still be in flight, the actual cause of the first interop CI failure).
func (p *harnessProc) close() {
	_, _ = p.stdin.Write([]byte("CLOSE\n"))
	_ = p.stdin.Close()
}

// waitForLine reads lines until one has the given prefix (returning the remainder) or ctx
// expires.
func (p *harnessProc) waitForLine(ctx context.Context, prefix string) (string, error) {
	type result struct {
		line string
		ok   bool
	}
	ch := make(chan result, 1)
	go func() {
		for p.lines.Scan() {
			line := p.lines.Text()
			p.stdout.WriteString(line)
			p.stdout.WriteByte('\n')
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				ch <- result{rest, true}
				return
			}
			if strings.HasPrefix(line, "ERROR ") {
				ch <- result{line, false}
				return
			}
		}
		ch <- result{"", false}
	}()
	select {
	case r := <-ch:
		if !r.ok {
			return "", fmt.Errorf("harness did not report %q: %s\nexit: %s\nstdout:\n%s\nstderr:\n%s",
				prefix, r.line, p.exitStatus(), p.stdout.String(), p.stderr.String())
		}
		return r.line, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestInteropOursToPinned and TestInteropPinnedToOurs each do a fresh invite, pair, one
// message and one small file fetch each way - no wire break expected (the Common Core diff,
// 3 Nov, found zero diff on the envelope, invite-link and pairing-proof formats).

func TestInteropOursToPinned(t *testing.T) {
	skipUnlessInterop(t)
	runInteropPair(t, "ours-inviter", "pinned-joiner")
}

func TestInteropPinnedToOurs(t *testing.T) {
	skipUnlessInterop(t)
	runInteropPair(t, "pinned-inviter", "ours-joiner")
}

func runInteropPair(t *testing.T, inviterKind, joinerKind string) {
	t.Helper()
	ours := buildOurHarness(t)
	pinned := buildPinnedHarness(t)
	r := interopRelay(t)
	relayAddr := relayMultiaddr(r)

	inviterBin, joinerBin := ours, pinned
	if inviterKind == "pinned-inviter" {
		inviterBin, joinerBin = pinned, ours
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	inviter := startHarness(t, inviterBin, "inviter", relayAddr, "", 0, time.Time{})
	link, err := inviter.waitForLine(ctx, "INVITE ")
	if err != nil {
		t.Fatalf("%s: %v", inviterKind, err)
	}

	joiner := startHarness(t, joinerBin, "joiner", relayAddr, link, 0, time.Time{})
	// Both DONE before either close()s: a harness stays up (p2ptest/interopnode's
	// waitForClose) exactly so neither side's h.Close() can race the other's still-in-flight
	// Fetch of its file - the real cause of the first interop CI failure.
	_, joinErr := joiner.waitForLine(ctx, "DONE")
	_, inviteErr := inviter.waitForLine(ctx, "DONE")
	joiner.close()
	inviter.close()
	if joinErr != nil {
		t.Fatalf("%s: %v", joinerKind, joinErr)
	}
	if inviteErr != nil {
		t.Fatalf("%s: %v", inviterKind, inviteErr)
	}
}

// TestInteropLateJoinAdmissionWindow documents the one behavioral divergence that affects
// who gets paired at all (the Common Core diff, 3 Nov, corrected after a first wrong read):
// OUR gater (internal/p2p/gater.go's inviteOpen) admits an unrecorded joiner only within 2
// minutes of the invite's OWN creation time; the pinned build's inviteOpen has no such cap -
// an invite stays open for its full stated lifetime (24h DM) exactly as a joiner would expect
// from a fresh install. So the divergence is one-directional: an invite from one of OUR
// builds dies early for a late joiner; an invite from a pinned build does not. The joiner's
// wait is measured from the inviter's own CREATED_AT (an absolute instant, via -join-at), not
// from when the joiner process happens to start - a security-property boundary test must
// measure against the invite's own clock. This takes a real several minutes to run.
func TestInteropLateJoinAdmissionWindow(t *testing.T) {
	skipUnlessInterop(t)
	ours := buildOurHarness(t)
	pinned := buildPinnedHarness(t)
	r := interopRelay(t)
	relayAddr := relayMultiaddr(r)

	// createInvite starts bin as an inviter and returns its link and creation instant.
	createInvite := func(t *testing.T, ctx context.Context, bin string) (*harnessProc, string, time.Time) {
		t.Helper()
		inviter := startHarness(t, bin, "inviter", relayAddr, "", 6*time.Minute, time.Time{})
		createdLine, err := inviter.waitForLine(ctx, "CREATED_AT ")
		if err != nil {
			t.Fatalf("inviter: %v", err)
		}
		created, err := time.Parse(time.RFC3339Nano, createdLine)
		if err != nil {
			t.Fatalf("CREATED_AT %q: %v", createdLine, err)
		}
		link, err := inviter.waitForLine(ctx, "INVITE ")
		if err != nil {
			t.Fatalf("inviter: %v", err)
		}
		return inviter, link, created
	}

	t.Run("our invite dies for a joiner 5 minutes late", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
		defer cancel()
		_, link, created := createInvite(t, ctx, ours)
		joiner := startHarness(t, pinned, "joiner", relayAddr, link, 6*time.Minute, created.Add(5*time.Minute))
		if _, err := joiner.waitForLine(ctx, "DONE"); err == nil {
			t.Fatal("a joiner 5 minutes late paired with one of our invites past its 2-minute window")
		}
		// The inviter never reaches DONE here (pairing never completes): t.Cleanup kills it.
	})

	t.Run("our invite still admits a joiner 30 seconds late", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		inviter, link, created := createInvite(t, ctx, ours)
		joiner := startHarness(t, pinned, "joiner", relayAddr, link, time.Minute, created.Add(30*time.Second))
		joinErr := func() error { _, err := joiner.waitForLine(ctx, "DONE"); return err }()
		inviteErr := func() error { _, err := inviter.waitForLine(ctx, "DONE"); return err }()
		joiner.close()
		inviter.close()
		if joinErr != nil {
			t.Fatalf("a joiner 30s late failed to pair within our 2-minute window (expected to succeed): %v", joinErr)
		}
		if inviteErr != nil {
			t.Fatalf("our inviter: %v", inviteErr)
		}
	})

	t.Run("a pinned invite still admits a joiner 5 minutes late", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
		defer cancel()
		inviter, link, created := createInvite(t, ctx, pinned)
		joiner := startHarness(t, ours, "joiner", relayAddr, link, 6*time.Minute, created.Add(5*time.Minute))
		joinErr := func() error { _, err := joiner.waitForLine(ctx, "DONE"); return err }()
		inviteErr := func() error { _, err := inviter.waitForLine(ctx, "DONE"); return err }()
		joiner.close()
		inviter.close()
		if joinErr != nil {
			t.Fatalf("a joiner 5 minutes late failed to pair with a pinned invite (expected to succeed - his gater has no 2-minute cap): %v", joinErr)
		}
		if inviteErr != nil {
			t.Fatalf("pinned inviter: %v", inviteErr)
		}
	})
}
