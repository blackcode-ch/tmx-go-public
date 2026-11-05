package term

import (
	"io"

	pty "github.com/aymanbagabas/go-pty"
)

// conduit is how a process reaches its program: a pty of the daemon's own (localConduit) or a
// session host (*host.Conn).
type conduit interface {
	io.ReadWriter
	Resize(cols, rows int) error
	Terminate()
	Kill()
	// Wait blocks until the program ended (its exit code) or the conduit was detached
	// (host.ErrDetached): the program then runs on in its host.
	Wait() (int, error)
	Close() error
	PID() int
}

// localConduit is a program in a pty of the daemon's own: it ends with the daemon (no session
// host: tests, and a daemon without the tmx executable to start hosts with).
type localConduit struct {
	pt  pty.Pty
	cmd *pty.Cmd
}

func startLocal(path string, args []string, dir string, env []string, cols, rows int) (*localConduit, error) {
	pt, err := pty.New()
	if err != nil {
		return nil, err
	}
	if err := pt.Resize(cols, rows); err != nil {
		pt.Close()
		return nil, err
	}
	cmd := pt.Command(path, args...)
	cmd.Dir = dir
	prepareCommand(cmd)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		pt.Close()
		return nil, err
	}
	afterStart(pt)
	return &localConduit{pt: pt, cmd: cmd}, nil
}

func (l *localConduit) Read(b []byte) (int, error)  { return l.pt.Read(b) }
func (l *localConduit) Write(b []byte) (int, error) { return l.pt.Write(b) }
func (l *localConduit) Resize(cols, rows int) error { return l.pt.Resize(cols, rows) }
func (l *localConduit) Terminate()                  { terminate(l.cmd) }
func (l *localConduit) Kill()                       { kill(l.cmd) }
func (l *localConduit) Close() error                { return l.pt.Close() }
func (l *localConduit) PID() int                    { return l.cmd.Process.Pid }

func (l *localConduit) Wait() (int, error) {
	err := l.cmd.Wait()
	code := -1
	if st := l.cmd.ProcessState; st != nil {
		code = st.ExitCode()
	}
	return code, err
}
