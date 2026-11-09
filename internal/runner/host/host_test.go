package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/runner/host/hosttest"
)

// The test binary is also `tmx host [--launch] DIR` and the program a host runs (TMXG_FAKE_PROG):
// "ready", then for each line "got:<line>"; "later" answers 300 ms later; "bye" exits 3 after
// writing "last words" to stderr.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "host" {
		var err error
		switch args := os.Args[2:]; {
		case len(args) == 2 && args[0] == "--launch":
			exe, _ := os.Executable()
			err = Launch(exe, args[1])
		case len(args) == 1:
			err = Run(args[0])
		default:
			err = errors.New("usage")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("TMXG_FAKE_PROG") == "1" {
		fakeProg()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeProg() {
	fmt.Print("ready\n")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch line {
		case "later":
			time.Sleep(300 * time.Millisecond)
			fmt.Print("got:later\n")
		case "bye":
			fmt.Fprint(os.Stderr, "last words")
			os.Exit(3)
		default:
			fmt.Printf("got:%s\n", line)
		}
	}
}

func spawn(t *testing.T, orphanHours float64) (string, *Conn) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "host")
	env := append(os.Environ(), "TMXG_FAKE_PROG=1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Spawn(ctx, hosttest.Executable(t), dir, Spec{Path: os.Args[0], Env: env, Cwd: t.TempDir(), Piped: true,
		Token: "cred-1", OrphanHours: orphanHours})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c2, err := Dial(dir, End); err == nil {
			c2.Kill()
			_, _ = c2.Wait()
		}
	})
	return dir, c
}

// readUntil reads c until the output read so far contains want.
func readUntil(t *testing.T, c *Conn, got *strings.Builder, want string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for !strings.Contains(got.String(), want) {
			n, err := c.Read(buf)
			got.Write(buf[:n])
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil && !strings.Contains(got.String(), want) {
			t.Fatalf("no %q (%v); got %q", want, err, got.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no %q within 20 s; got %q", want, got.String())
	}
}

// A daemon detaches at a line's end; the next one asks for the output from there: what the
// program wrote meanwhile arrives, what the first one handled does not come again.
func TestPipedHostResumesAtTheOffset(t *testing.T) {
	dir, c1 := spawn(t, 0)
	if c1.Token() != "cred-1" || c1.PID() <= 0 || c1.HostPID() <= 0 {
		t.Fatalf("info: %q %d %d", c1.Token(), c1.PID(), c1.HostPID())
	}
	var got1 strings.Builder
	readUntil(t, c1, &got1, "ready\n")
	handled := uint64(len("ready\n"))
	if _, err := c1.Write([]byte("later\n")); err != nil {
		t.Fatal(err)
	}
	if err := c1.SaveState(map[string]uint64{"offset": handled}); err != nil {
		t.Fatal(err)
	}
	c1.Close() // the daemon stops before the answer comes
	if _, err := c1.Wait(); !errors.Is(err, ErrDetached) {
		t.Fatalf("a detach is not an exit: %v", err)
	}
	time.Sleep(600 * time.Millisecond) // the program answers while no daemon is attached

	var st map[string]uint64
	if !LoadState(dir, &st) || st["offset"] != handled {
		t.Fatalf("state %v", st)
	}
	c2, err := Dial(dir, st["offset"])
	if err != nil {
		t.Fatal(err)
	}
	if c2.Start() != handled {
		t.Fatalf("starts at %d, want %d", c2.Start(), handled)
	}
	var got2 strings.Builder
	readUntil(t, c2, &got2, "got:later\n")
	if strings.Contains(got2.String(), "ready") {
		t.Fatalf("handled output came again: %q", got2.String())
	}
	if _, err := c2.Write([]byte("again\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, c2, &got2, "got:again\n")
	ClearState(dir)
	if LoadState(dir, &st) {
		t.Fatal("state not cleared")
	}
}

// End: only what comes next (a daemon that died without a handover never gets output twice).
func TestPipedHostFromTheEnd(t *testing.T) {
	dir, c1 := spawn(t, 0)
	var got strings.Builder
	readUntil(t, c1, &got, "ready\n")
	c1.Close()
	c2, err := Dial(dir, End)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Start() != uint64(len("ready\n")) {
		t.Fatalf("start %d", c2.Start())
	}
	c2.Write([]byte("x\n"))
	var got2 strings.Builder
	readUntil(t, c2, &got2, "got:x\n")
	if strings.Contains(got2.String(), "ready") {
		t.Fatalf("old output: %q", got2.String())
	}
}

// The program's exit reaches the daemon with its code and the tail of its stderr; the host
// then ends and leaves no host.json.
func TestPipedHostExit(t *testing.T) {
	dir, c := spawn(t, 0)
	var got strings.Builder
	readUntil(t, c, &got, "ready\n")
	c.Write([]byte("bye\n"))
	go io.Copy(io.Discard, c)
	code, err := c.Wait()
	if err != nil || code != 3 || c.Stderr() != "last words" {
		t.Fatalf("exit %d %v %q", code, err, c.Stderr())
	}
	waitGone(t, dir)
	if _, err := Dial(dir, 0); !errors.Is(err, ErrNoHost) {
		t.Fatalf("dial after the end: %v", err)
	}
}

// Closing stdin ends a program that reads until EOF (claude's graceful stop).
func TestPipedHostCloseInput(t *testing.T) {
	dir, c := spawn(t, 0)
	var got strings.Builder
	readUntil(t, c, &got, "ready\n")
	if err := c.CloseInput(); err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, c)
	if code, err := c.Wait(); err != nil || code != 0 {
		t.Fatalf("exit %d %v", code, err)
	}
	waitGone(t, dir)
}

// A connection without the host's secret gets nothing.
func TestHostNeedsItsSecret(t *testing.T) {
	dir, _ := spawn(t, 0)
	var in info
	if err := readJSON(dir, infoFile, &in); err != nil {
		t.Fatal(err)
	}
	in.Secret = "wrong"
	other := t.TempDir()
	if err := writeJSON(other, infoFile, in); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(other, 0); !errors.Is(err, ErrHostUnknown) {
		t.Fatalf("a wrong secret was served: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, specFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the spec (it holds the environment) was left behind: %v", err)
	}
}

// With no daemon attached for its orphan time, a host ends its program and exits: it outlives a
// restart, not a daemon that is gone for good.
func TestOrphanHostEnds(t *testing.T) {
	dir, c := spawn(t, 1.0/3600) // one second
	var got strings.Builder
	readUntil(t, c, &got, "ready\n")
	c.Close()
	waitGone(t, dir)
}

func waitGone(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, infoFile)); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the host did not end")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The host.json of a host that is gone (killed, crashed, a reboot) while something else now
// listens on its port: Dial says there is no host, writes nothing to that listener, and
// removes the stale files (o4, #247).
func TestStaleHostJSONIsNotTrusted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan int, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- -1
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := io.Copy(io.Discard, c)
		got <- int(n)
	}()
	dir := t.TempDir()
	stale := info{PID: os.Getpid(), Start: 1, Image: "tmx-host-x", ChildPID: 1, Port: ln.Addr().(*net.TCPAddr).Port,
		Secret: "s", Token: "cred", Protocol: protocol}
	if err := writeJSON(dir, infoFile, stale); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(dir, stateFile, map[string]int{"offset": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(dir, 0); !errors.Is(err, ErrNoHost) {
		t.Fatalf("dial: %v", err)
	}
	ln.Close()
	select {
	case n := <-got:
		if n > 0 {
			t.Fatalf("%d bytes went to a stranger", n)
		}
	case <-time.After(3 * time.Second):
	}
	for _, f := range []string{infoFile, stateFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale %s kept: %v", f, err)
		}
	}
}

// An impostor on the host's port (it does not hold the secret, but speaks the protocol): it
// sees no byte of the secret and is refused; the agent is not taken for gone either (o5, #247).
func TestImpostorGetsNoSecretAndIsRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	const secret = "the-hosts-secret-0123456789abcdef"
	seen := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			seen <- nil
			return
		}
		defer c.Close()
		var all []byte
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if typ, b, err := readFrame(c); err == nil {
			all = append(all, typ)
			all = append(all, b...)
			// the best it can do without the secret: an answer over the daemon's nonce
			_ = writeFrame(c, frameProof, append(proof("guess", "host", b[8:]), make([]byte, nonceSize)...))
		}
		rest, _ := io.ReadAll(c)
		seen <- append(all, rest...)
	}()
	old := alive
	alive = func(info) liveness { return hostAlive } // as if the recorded host still ran
	defer func() { alive = old }()
	dir := t.TempDir()
	if err := writeJSON(dir, infoFile, info{PID: 1, Start: 1, Image: "tmx-host-x", Port: ln.Addr().(*net.TCPAddr).Port, Secret: secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(dir, 0); !errors.Is(err, ErrHostUnknown) {
		t.Fatalf("an impostor was accepted, or taken for proof the host is gone: %v", err)
	}
	got := <-seen
	if strings.Contains(string(got), secret) || strings.Contains(string(got), secret[:8]) {
		t.Fatalf("the secret went to the impostor: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, infoFile)); err != nil {
		t.Fatalf("an unsure check removed host.json: %v", err)
	}
}

// An unreadable process list proves nothing: Dial says unknown and removes nothing (the fleet
// then checks again instead of starting a second copy of the agent).
func TestUnsureCheckKeepsTheHost(t *testing.T) {
	old := alive
	alive = func(info) liveness { return hostUnknown }
	defer func() { alive = old }()
	dir := t.TempDir()
	if err := writeJSON(dir, infoFile, info{PID: 1, Start: 1, Image: "tmx-host-x", Port: 1, Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(dir, 0); !errors.Is(err, ErrHostUnknown) {
		t.Fatalf("dial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, infoFile)); err != nil {
		t.Fatalf("host.json removed on an unsure check: %v", err)
	}
}

// Stop ends a host that runs (its program with it) by the identity it recorded; afterwards
// there is nothing to stop. A host.json whose process is gone is only cleaned up.
func TestStopEndsTheRecordedHost(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("stopping a recorded host is unsupported on macOS")
	}
	dir, c := spawn(t, 0)
	var got strings.Builder
	readUntil(t, c, &got, "ready\n")
	go io.Copy(io.Discard, c)
	gone, err := Stop(dir)
	if err != nil || gone {
		t.Fatalf("stop: gone=%v %v", gone, err)
	}
	if _, err := c.Wait(); err == nil {
		t.Log("the program's exit reached the daemon")
	}
	if _, err := Dial(dir, 0); !errors.Is(err, ErrNoHost) {
		t.Fatalf("dial after a stop: %v", err)
	}
	if gone, err := Stop(dir); err != nil || !gone {
		t.Fatalf("a second stop: gone=%v %v", gone, err)
	}
}

func TestStopNeverKillsAnotherProcess(t *testing.T) {
	old := killHost
	killed := false
	killHost = func(info) error { killed = true; return nil }
	defer func() { killHost = old }()
	dir := t.TempDir()
	// this test's own PID, with another start time: a reused PID, not the host
	if err := writeJSON(dir, infoFile, info{PID: os.Getpid(), Start: 1, Image: "tmx-host-x", Port: 1, Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	if gone, err := Stop(dir); err != nil || !gone || killed {
		t.Fatalf("gone=%v err=%v killed=%v", gone, err, killed)
	}
	// unsure: nothing is done
	oldAlive := alive
	alive = func(info) liveness { return hostUnknown }
	defer func() { alive = oldAlive }()
	if err := writeJSON(dir, infoFile, info{PID: 1, Start: 1, Image: "tmx-host-x", Port: 1, Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Stop(dir); !errors.Is(err, ErrHostUnknown) || killed {
		t.Fatalf("unsure: %v killed=%v", err, killed)
	}
}

// stubStop replaces Stop's steps for one test: liveness answers in order (the last repeats).
func stubStop(t *testing.T, answers []liveness, program, hostErr error, child bool) (hostKilled *bool) {
	t.Helper()
	oa, op, oh, oc := alive, killProgram, killHost, childOfHost
	t.Cleanup(func() { alive, killProgram, killHost, childOfHost = oa, op, oh, oc })
	n := 0
	alive = func(info) liveness {
		a := answers[min(n, len(answers)-1)]
		n++
		return a
	}
	killProgram = func(info) error { return program }
	killed := false
	killHost = func(info) error { killed = true; return hostErr }
	childOfHost = func(info) bool { return child }
	return &killed
}

func writeInfo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := writeJSON(dir, infoFile, info{PID: 1, ChildPID: 2, Start: 1, Image: "tmx-host-x", Port: 1, Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The host kill failed and the re-check is unsure: not reported ended, host.json kept (else a
// second copy could start while the host runs). o5, #249.
func TestStopFailedKillUnsureKeepsTheHost(t *testing.T) {
	dir := writeInfo(t)
	stubStop(t, []liveness{hostAlive, hostUnknown}, nil, errors.New("access denied"), false)
	if _, err := Stop(dir); err == nil {
		t.Fatal("reported stopped")
	}
	if _, err := os.Stat(filepath.Join(dir, infoFile)); err != nil {
		t.Fatalf("host.json removed: %v", err)
	}
}

// The program's tree could not be ended and it still runs under its host: the host is not
// killed (that would orphan the program), and the stop says so. o5, #249.
func TestStopNeverOrphansTheProgram(t *testing.T) {
	dir := writeInfo(t)
	killed := stubStop(t, []liveness{hostAlive}, errors.New("not registered"), nil, true)
	if _, err := Stop(dir); err == nil || !strings.Contains(err.Error(), "program") {
		t.Fatalf("stop: %v", err)
	}
	if *killed {
		t.Fatal("the host was killed while its program still ran")
	}
	if _, err := os.Stat(filepath.Join(dir, infoFile)); err != nil {
		t.Fatalf("host.json removed: %v", err)
	}
}

// The program already ended (not under the host any more): the host is ended as usual.
func TestStopEndsTheHostWhenTheProgramIsGone(t *testing.T) {
	dir := writeInfo(t)
	killed := stubStop(t, []liveness{hostAlive, hostGone}, errors.New("no such process"), nil, false)
	if gone, err := Stop(dir); err != nil || gone || !*killed {
		t.Fatalf("gone=%v err=%v killed=%v", gone, err, *killed)
	}
}

func TestWaitHostGoneBounded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []liveness
		want    bool
	}{
		{"delayed exit", []liveness{hostAlive, hostAlive, hostGone}, true},
		{"persistent failure", []liveness{hostAlive}, false},
		{"unknown liveness", []liveness{hostUnknown}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := alive
			t.Cleanup(func() { alive = old })
			n := 0
			alive = func(info) liveness { v := tc.answers[min(n, len(tc.answers)-1)]; n++; return v }
			clock := time.Unix(0, 0)
			now := func() time.Time { return clock }
			sleep := func(d time.Duration) { clock = clock.Add(d) }
			if got := waitHostGone(info{}, 2*time.Second, 25*time.Millisecond, now, sleep); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if elapsed := clock.Sub(time.Unix(0, 0)); elapsed > 2*time.Second {
				t.Fatalf("exceeded deadline: %v", elapsed)
			}
		})
	}
}
