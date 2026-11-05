package host

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/runner/ptyproc"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

const (
	// linger is how long a host whose program ended waits for a daemon to collect the exit.
	linger = 10 * time.Minute
	// Output kept for a reattaching daemon: a terminal's recent screen; a piped program's
	// whole unhandled stream (a daemon restart takes about a minute).
	keepTerminal = 1 << 20
	keepPiped    = 64 << 20
	stderrTail   = 8 << 10

	defaultCols, defaultRows = 120, 32
	defaultOrphanHours       = 12
	// firstAttach: a host no daemon attached to yet is not an orphan before this.
	firstAttach = time.Minute
)

// Run is `tmx host DIR`: it starts DIR's spec.json and serves it to daemons until the program
// has ended and a daemon saw the exit (or linger passed). A daemon going away never stops the
// program; no daemon for Spec.OrphanHours does.
func Run(dir string) error {
	var spec Spec
	if err := readJSON(dir, specFile, &spec); err != nil {
		return fmt.Errorf("session host: %w", err)
	}
	_ = os.Remove(filepath.Join(dir, specFile)) // it holds the agent's environment
	_ = os.Remove(filepath.Join(dir, stateFile))
	h := &host{exited: make(chan struct{}), lastSeen: time.Now()}
	if spec.Piped {
		h.keep = keepPiped
	} else {
		h.keep = keepTerminal
	}
	if err := h.start(spec); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.kill()
		return err
	}
	defer ln.Close()
	in := info{PID: os.Getpid(), ChildPID: h.pid, Port: ln.Addr().(*net.TCPAddr).Port, Secret: config.NewToken(),
		Token: spec.Token, Piped: spec.Piped, Protocol: protocol}
	if rows, err := sysinfo.Procs(); err == nil {
		for _, p := range rows {
			if p.PID == in.PID {
				in.Start, in.Image = p.Start, p.Name
			}
		}
	}
	if in.Start <= 0 || in.Image == "" {
		// Without its identity no daemon could ever trust this host: refuse to run, rather
		// than leave an agent nobody can reattach to.
		h.kill()
		return errors.New("session host: cannot record its own identity")
	}
	if err := writeJSON(dir, infoFile, in); err != nil {
		h.kill()
		return err
	}
	defer os.Remove(filepath.Join(dir, infoFile))

	readDone := make(chan struct{})
	go h.read(readDone)
	go h.wait(readDone)
	acked := make(chan struct{})
	var ackOnce sync.Once
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serve(conn, in.Secret, func() { ackOnce.Do(func() { close(acked) }) })
		}
	}()
	orphan := time.Duration(defaultOrphanHours * float64(time.Hour))
	if spec.OrphanHours > 0 {
		orphan = time.Duration(spec.OrphanHours * float64(time.Hour))
	}
	go h.watchOrphan(orphan)
	<-h.exited
	h.mu.Lock()
	orphaned := h.orphaned
	h.mu.Unlock()
	if orphaned {
		return nil // nobody is coming for the exit
	}
	select {
	case <-acked:
	case <-time.After(linger):
	}
	return nil
}

type host struct {
	// the program: a terminal (pt, pcmd) or pipes (cmd, stdin, stderr)
	pt     pty.Pty
	pcmd   *pty.Cmd
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr *tail
	pid    int

	wmu sync.Mutex // serialises writes to the program

	mu       sync.Mutex // guards out, client, lastSeen and the exit
	keep     int
	base     uint64 // the offset of out[0]
	out      []byte
	client   *client
	lastSeen time.Time // when a daemon was last attached
	attached bool      // a daemon was attached at least once
	code     int
	stderrAt string // the stderr tail at the exit
	orphaned bool   // watchOrphan ended the program
	exited   chan struct{}
}

func (h *host) start(spec Spec) error {
	if !spec.Piped {
		cols, rows := spec.Cols, spec.Rows
		if cols <= 0 || rows <= 0 {
			cols, rows = defaultCols, defaultRows
		}
		pt, err := pty.New()
		if err != nil {
			return fmt.Errorf("open pty: %w", err)
		}
		if err := pt.Resize(cols, rows); err != nil {
			pt.Close()
			return fmt.Errorf("size pty: %w", err)
		}
		cmd := pt.Command(spec.Path, spec.Args...)
		cmd.Dir, cmd.Env = spec.Cwd, spec.Env
		ptyproc.Prepare(cmd)
		if err := cmd.Start(); err != nil {
			pt.Close()
			return fmt.Errorf("start %s: %w", spec.Path, err)
		}
		ptyproc.AfterStart(pt)
		h.pt, h.pcmd, h.stdout, h.pid = pt, cmd, pt, cmd.Process.Pid
		return nil
	}
	cmd := hiddenexec.Command(spec.Path, spec.Args...)
	cmd.Dir, cmd.Env = spec.Cwd, spec.Env
	cmd.WaitDelay = 2 * time.Second // grandchildren holding stderr must not block Wait
	setGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	h.stderr = &tail{max: stderrTail}
	cmd.Stderr = h.stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", spec.Path, err)
	}
	h.cmd, h.stdin, h.stdout, h.pid = cmd, stdin, stdout, cmd.Process.Pid
	return nil
}

func (h *host) terminate() {
	if h.pcmd != nil {
		ptyproc.Terminate(h.pcmd)
		return
	}
	terminateGroup(h.cmd)
}

func (h *host) kill() {
	if h.pcmd != nil {
		ptyproc.Kill(h.pcmd)
		return
	}
	killGroup(h.cmd)
}

func (h *host) write(b []byte) error {
	h.wmu.Lock()
	defer h.wmu.Unlock()
	if h.pt != nil {
		_, err := h.pt.Write(b)
		return err
	}
	if h.stdin == nil {
		return errors.New("input closed")
	}
	_, err := h.stdin.Write(b)
	return err
}

func (h *host) closeInput() {
	h.wmu.Lock()
	defer h.wmu.Unlock()
	if h.stdin != nil {
		_ = h.stdin.Close()
		h.stdin = nil
	}
}

func (h *host) read(done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 32<<10)
	for {
		n, err := h.stdout.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			h.mu.Lock()
			off := h.base + uint64(len(h.out))
			h.out = append(h.out, chunk...)
			if over := len(h.out) - h.keep; over > 0 {
				h.out = append([]byte(nil), h.out[over:]...)
				h.base += uint64(over)
			}
			if h.client != nil {
				h.client.send(frameOutput, outputPayload(off, chunk))
			}
			h.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (h *host) wait(readDone <-chan struct{}) {
	var code int
	if h.pcmd != nil {
		_ = h.pcmd.Wait()
		code = -1
		if st := h.pcmd.ProcessState; st != nil {
			code = st.ExitCode()
		}
		select {
		case <-readDone:
		case <-time.After(500 * time.Millisecond): // a background job may still hold the tty
		}
		h.pt.Close()
	} else {
		<-readDone // stdout first: Wait closes the pipes
		_ = h.cmd.Wait()
		reapGroup(h.cmd) // whatever the agent left running in its process group
		code = -1
		if st := h.cmd.ProcessState; st != nil {
			code = st.ExitCode()
		}
	}
	<-readDone
	h.mu.Lock()
	h.code = code
	if h.stderr != nil {
		h.stderrAt = h.stderr.String()
	}
	close(h.exited)
	if h.client != nil {
		h.client.send(frameExit, exitPayload(code, h.stderrAt))
	}
	h.mu.Unlock()
}

// watchOrphan ends the program once no daemon was attached for orphan (a daemon gone for
// good, not restarting); the host then exits.
func (h *host) watchOrphan(orphan time.Duration) {
	every := min(time.Minute, max(orphan/4, 50*time.Millisecond))
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-h.exited:
			return
		case <-t.C:
			h.mu.Lock()
			// Before any daemon attached (the one that started it is still connecting) a host
			// waits at least firstAttach.
			limit := orphan
			if !h.attached {
				limit = max(orphan, firstAttach)
			}
			gone := h.client == nil && time.Since(h.lastSeen) > limit
			h.orphaned = gone
			h.mu.Unlock()
			if gone {
				h.closeInput()
				h.terminate()
				select {
				case <-h.exited:
				case <-time.After(10 * time.Second):
					h.kill()
				}
				return
			}
		}
	}
}

// client is the attached daemon. Output goes through a buffered queue: a daemon that stops
// reading is dropped, never the program blocked.
type client struct {
	conn net.Conn
	q    chan []byte
	done chan struct{} // closed with the connection; q is never closed
	once sync.Once
}

func (c *client) send(typ byte, payload []byte) {
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], uint32(len(payload)))
	copy(b[5:], payload)
	select {
	case <-c.done:
	case c.q <- b:
	default:
		c.close()
	}
}

func (c *client) close() { c.once.Do(func() { close(c.done); c.conn.Close() }) }

func (c *client) pump() {
	for {
		select {
		case <-c.done:
			return
		case b := <-c.q:
			_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := c.conn.Write(b); err != nil {
				c.close()
				return
			}
		}
	}
}

// serve is one daemon's connection: offset and secret first, then the kept output from that
// offset and live output; a new daemon replaces the old one.
func (h *host) serve(conn net.Conn, secret string, ack func()) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	typ, b, err := readFrame(conn)
	if err != nil || typ != frameHello || len(b) != 8+nonceSize {
		conn.Close()
		return
	}
	from, theirs := binary.BigEndian.Uint64(b[:8]), b[8:]
	ours := make([]byte, nonceSize)
	if _, err := rand.Read(ours); err != nil {
		conn.Close()
		return
	}
	if err := writeFrame(conn, frameProof, append(proof(secret, "host", theirs), ours...)); err != nil {
		conn.Close()
		return
	}
	typ, b, err = readFrame(conn)
	if err != nil || typ != frameAuth || !hmac.Equal(b, proof(secret, "daemon", ours)) {
		conn.Close() // not a daemon that holds the secret: it gets nothing more
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	c := &client{conn: conn, q: make(chan []byte, 4096), done: make(chan struct{})}
	go c.pump()
	h.mu.Lock()
	if old := h.client; old != nil {
		old.close()
	}
	h.client = c
	h.lastSeen = time.Now()
	h.attached = true
	if from < h.base {
		from = h.base // older than what is kept: the daemon learns it from the offset
	}
	if end := h.base + uint64(len(h.out)); from > end {
		from = end
	}
	c.send(frameOutput, outputPayload(from, nil)) // where this connection's output starts
	for rest, off := h.out[from-h.base:], from; len(rest) > 0; {
		n := min(len(rest), maxFrame-8)
		c.send(frameOutput, outputPayload(off, rest[:n]))
		rest, off = rest[n:], off+uint64(n)
	}
	select {
	case <-h.exited:
		c.send(frameExit, exitPayload(h.code, h.stderrAt))
	default:
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.client == c {
			h.client = nil
			h.lastSeen = time.Now()
		}
		h.mu.Unlock()
		c.close()
	}()
	for {
		typ, b, err := readFrame(conn)
		if err != nil {
			return
		}
		switch typ {
		case frameWrite:
			_ = h.write(b) // a write after the program ended is not the daemon's fault
		case frameCloseInput:
			h.closeInput()
		case frameResize:
			if len(b) == 4 && h.pt != nil {
				_ = h.pt.Resize(int(binary.BigEndian.Uint16(b[0:2])), int(binary.BigEndian.Uint16(b[2:4])))
			}
		case frameTerminate:
			h.terminate()
		case frameKill:
			h.kill()
		case frameAck:
			ack()
		default:
			return
		}
	}
}

func outputPayload(off uint64, data []byte) []byte {
	b := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(b[:8], off)
	copy(b[8:], data)
	return b
}

func exitPayload(code int, stderr string) []byte {
	b := make([]byte, 4+len(stderr))
	binary.BigEndian.PutUint32(b[:4], uint32(int32(code)))
	copy(b[4:], stderr)
	return b
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// Launch is `tmx host --launch DIR`: it starts `exe host DIR` detached from this process (its
// own session or process group, no console window, out of any job object that allows it) and
// returns without waiting. The host is then nobody's child: a tree kill of the daemon misses it.
func Launch(exe, dir string) error {
	cmd := hiddenexec.Command(exe, "host", dir)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		if retry := withoutBreakaway(cmd); retry != nil {
			if err2 := retry.Start(); err2 == nil {
				return retry.Process.Release()
			}
		}
		return err
	}
	return cmd.Process.Release()
}
