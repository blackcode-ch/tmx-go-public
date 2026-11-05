package host

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// ErrDetached: the connection to the host ended without the program's exit (Conn.Close, or the
// host went away): the program may run on.
var ErrDetached = errors.New("detached from the session host")

// Conn is a daemon's connection to a session host.
type Conn struct {
	conn net.Conn
	in   info
	dir  string

	out  *io.PipeReader
	outW *io.PipeWriter
	wmu  sync.Mutex

	mu     sync.Mutex
	start  uint64 // the offset of the first output byte this connection delivers
	next   uint64 // the offset of the next output byte this connection delivers
	done   chan struct{}
	code   int
	stderr string
	err    error
}

// Dial connects to the host in dir (host.json), asking for its output from offset from (0: all
// it kept: a terminal's recent screen; End: only what comes next). Start is where it really
// starts (not before what the host kept, not after its end). ErrNoHost when none runs there.
func Dial(dir string, from uint64) (*Conn, error) {
	var in info
	if err := readJSON(dir, infoFile, &in); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoHost
		}
		return nil, fmt.Errorf("%w: %v", ErrNoHost, err)
	}
	if in.Port <= 0 || in.Secret == "" {
		return nil, ErrNoHost
	}
	// The host.json of a host that was killed, crashed or lost to a reboot stays behind: only
	// a live process with the recorded identity is that host. Never write to whatever else may
	// listen on its port now. Unsure (the process list unreadable): not gone either.
	switch alive(in) {
	case hostGone:
		_ = os.Remove(filepath.Join(dir, infoFile))
		_ = os.Remove(filepath.Join(dir, stateFile))
		return nil, fmt.Errorf("%w: its host process is gone", ErrNoHost)
	case hostUnknown:
		return nil, fmt.Errorf("%w: the process list could not be read", ErrHostUnknown)
	}
	// From here the host process runs: a failure is not proof it is gone.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(in.Port)), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHostUnknown, err)
	}
	fail := func(why string) (*Conn, error) {
		conn.Close()
		return nil, fmt.Errorf("%w: %s", ErrHostUnknown, why)
	}
	ours := make([]byte, nonceSize)
	if _, err := rand.Read(ours); err != nil {
		return fail(err.Error())
	}
	hello := make([]byte, 8+nonceSize)
	binary.BigEndian.PutUint64(hello[:8], from)
	copy(hello[8:], ours)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeFrame(conn, frameHello, hello); err != nil {
		return fail(err.Error())
	}
	typ, b, err := readFrame(conn)
	if err != nil || typ != frameProof || len(b) != 32+nonceSize || !hmac.Equal(b[:32], proof(in.Secret, "host", ours)) {
		return fail("the host did not prove it holds the secret") // nothing secret was sent
	}
	if err := writeFrame(conn, frameAuth, proof(in.Secret, "daemon", b[32:])); err != nil {
		return fail(err.Error())
	}
	typ, b, err = readFrame(conn)
	_ = conn.SetDeadline(time.Time{})
	if err != nil || typ != frameOutput || len(b) != 8 {
		return fail("the host did not answer")
	}
	start := binary.BigEndian.Uint64(b)
	pr, pw := io.Pipe()
	c := &Conn{conn: conn, in: in, dir: dir, out: pr, outW: pw, start: start, next: start, done: make(chan struct{}), code: -1}
	go c.readLoop()
	return c, nil
}

type liveness int

const (
	hostAlive liveness = iota
	hostGone
	hostUnknown
)

// alive tells whether the host in recorded runs: its PID, with the start time and executable
// name it recorded (a PID reused by another process has another start time), a session host's
// tmx copy (tmx-host-*, like sysinfo.RegisterHosted). A host.json without that identity was
// never a host's (hosts refuse to run without it). An unreadable process list proves nothing.
var alive = func(in info) liveness {
	if in.PID <= 0 || in.Start <= 0 || !sysinfo.IsHostImage(in.Image) {
		return hostGone
	}
	rows, err := sysinfo.Procs()
	if err != nil || len(rows) == 0 {
		return hostUnknown
	}
	for _, p := range rows {
		if p.PID == in.PID {
			if p.Start == in.Start && p.Name == in.Image {
				return hostAlive
			}
			return hostGone
		}
	}
	return hostGone
}

// Stop ends the agent in dir's host without talking to the host: for a host that does not
// answer (ErrHostUnknown) when the owner stops its agent. It acts only on the identity host.json
// recorded: the program's tree, anchored to the host (sysinfo), then the host, each only if it
// is still that process. gone: there was nothing left to end. ErrHostUnknown: whether it runs
// could not be told (the process list unreadable): nothing was done.
func Stop(dir string) (gone bool, err error) {
	var in info
	if err := readJSON(dir, infoFile, &in); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	switch alive(in) {
	case hostGone:
		_ = os.Remove(filepath.Join(dir, infoFile))
		_ = os.Remove(filepath.Join(dir, stateFile))
		return true, nil
	case hostUnknown:
		return false, fmt.Errorf("%w: not stopped, it could not be verified", ErrHostUnknown)
	}
	// The program first, through its verified tree: ending the host alone would orphan it.
	if err := killProgram(in); err != nil && childOfHost(in) {
		return false, fmt.Errorf("the agent's program could not be ended: %w", err)
	}
	if err := killHost(in); err != nil && alive(in) != hostGone {
		// Not proven ended (still there, or unsure): keep host.json, so nothing starts a copy.
		return false, err
	}
	_ = os.Remove(filepath.Join(dir, infoFile))
	_ = os.Remove(filepath.Join(dir, stateFile))
	return false, nil
}

// Stop's steps, variables for tests.
var (
	killHost    = func(in info) error { return sysinfo.KillHost(in.PID, in.Start, in.Image) }
	killProgram = func(in info) error {
		sysinfo.RegisterHosted(in.ChildPID, in.PID, in.Start, false)
		return sysinfo.KillTree(in.ChildPID) // only a verified child of that host
	}
	// childOfHost: the program may still run under its host (unsure counts as yes).
	childOfHost = func(in info) bool {
		rows, err := sysinfo.Procs()
		if err != nil {
			return true
		}
		for _, p := range rows {
			if p.PID == in.ChildPID {
				return p.PPID == in.PID
			}
		}
		return false
	}
)

// End asks Dial for only the output that comes next.
const End = ^uint64(0)

// Start is the offset of the first output byte this connection delivers.
func (c *Conn) Start() uint64 { return c.start }

// ClearState removes what SaveState left in dir (it was taken over).
func ClearState(dir string) { _ = os.Remove(filepath.Join(dir, stateFile)) }

func (c *Conn) readLoop() {
	defer close(c.done)
	for {
		typ, b, err := readFrame(c.conn)
		if err != nil {
			c.mu.Lock()
			c.err = ErrDetached
			c.mu.Unlock()
			c.outW.CloseWithError(io.EOF)
			return
		}
		switch typ {
		case frameOutput:
			if len(b) < 8 {
				continue
			}
			off, data := binary.BigEndian.Uint64(b[:8]), b[8:]
			c.mu.Lock()
			if off+uint64(len(data)) <= c.next { // already delivered
				c.mu.Unlock()
				continue
			}
			if off < c.next {
				data = data[c.next-off:]
			}
			c.next = off + uint64(len(b)-8)
			c.mu.Unlock()
			if len(data) > 0 {
				if _, err := c.outW.Write(data); err != nil {
					return
				}
			}
		case frameExit:
			c.mu.Lock()
			if len(b) >= 4 {
				c.code = int(int32(binary.BigEndian.Uint32(b[:4])))
				c.stderr = string(b[4:])
			}
			c.err = nil
			c.mu.Unlock()
			c.outW.CloseWithError(io.EOF)
			_ = c.send(frameAck, nil) // the host may end now
			c.conn.Close()
			return
		}
	}
}

func (c *Conn) send(typ byte, b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeFrame(c.conn, typ, b)
}

// Read is the program's output (stdout, or the terminal), from the offset asked for.
func (c *Conn) Read(b []byte) (int, error) { return c.out.Read(b) }

func (c *Conn) Write(b []byte) (int, error) {
	if err := c.send(frameWrite, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// CloseInput closes the program's stdin (a piped program).
func (c *Conn) CloseInput() error { return c.send(frameCloseInput, nil) }

func (c *Conn) Resize(cols, rows int) error {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:2], uint16(cols))
	binary.BigEndian.PutUint16(b[2:4], uint16(rows))
	return c.send(frameResize, b)
}

func (c *Conn) Terminate() { _ = c.send(frameTerminate, nil) }
func (c *Conn) Kill()      { _ = c.send(frameKill, nil) }

// PID is the program's (not the host's).
func (c *Conn) PID() int { return c.in.ChildPID }

// HostPID and HostStart identify the host process (sysinfo.RegisterHosted).
func (c *Conn) HostPID() int     { return c.in.PID }
func (c *Conn) HostStart() int64 { return c.in.Start }

// Token is the agent's tmx credential the program was started with.
func (c *Conn) Token() string { return c.in.Token }

// Wait blocks until the program ended (its exit code) or the connection ended without its
// exit (ErrDetached).
func (c *Conn) Wait() (int, error) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code, c.err
}

// Stderr is the tail of a piped program's stderr at its exit.
func (c *Conn) Stderr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr
}

// Close detaches: the connection ends, the program runs on in its host.
func (c *Conn) Close() error { return c.conn.Close() }

// SaveState hands the next daemon what it needs to go on where this one stopped (a runner's
// own state, e.g. the offset of the last line it handled), as a secret file in the host's
// folder; LoadState reads it back (false: none).
func (c *Conn) SaveState(v any) error { return writeJSON(c.dir, stateFile, v) }

// LoadState reads what SaveState left in dir.
func LoadState(dir string, v any) bool { return readJSON(dir, stateFile, v) == nil }

// Spawn starts a session host for spec in dir and connects to it. The host is started through
// `exe host --launch DIR`, which starts the real host detached and exits at once (Launch).
func Spawn(ctx context.Context, exe, dir string, spec Spec) (*Conn, error) {
	_ = os.Remove(filepath.Join(dir, infoFile))
	_ = os.Remove(filepath.Join(dir, stateFile))
	if err := writeJSON(dir, specFile, spec); err != nil {
		return nil, err
	}
	cmd := hiddenexec.Command(exe, "host", "--launch", dir)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(filepath.Join(dir, specFile))
		return nil, fmt.Errorf("start the session host: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, infoFile)); err == nil {
			return Dial(dir, 0)
		}
		if time.Now().After(deadline) {
			_ = os.Remove(filepath.Join(dir, specFile))
			return nil, errors.New("the session host did not come up")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
