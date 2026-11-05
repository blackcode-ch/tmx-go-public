package claude

import (
	"io"
	"os/exec"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/host"
)

// pipes is how a process reaches claude: pipes of the daemon's own (localPipes), or a session
// host that outlives the daemon (*host.Conn, Spec.HostDir).
type pipes interface {
	io.Reader // stdout
	io.Writer // stdin
	CloseInput() error
	Terminate()
	Kill()
	// Wait blocks until claude ended (its exit code and an error) or the pipes were detached
	// from its host (host.ErrDetached: it runs on).
	Wait() (int, error)
	Stderr() string
	PID() int
}

// localPipes is claude as the daemon's own child: it ends with the daemon.
type localPipes struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr *tailBuffer
}

func (l *localPipes) Read(b []byte) (int, error)  { return l.stdout.Read(b) }
func (l *localPipes) Write(b []byte) (int, error) { return l.stdin.Write(b) }
func (l *localPipes) CloseInput() error           { return l.stdin.Close() }
func (l *localPipes) Terminate()                  { terminate(l.cmd) }
func (l *localPipes) Kill()                       { kill(l.cmd) }
func (l *localPipes) Stderr() string              { return strings.TrimSpace(l.stderr.String()) }
func (l *localPipes) PID() int                    { return l.cmd.Process.Pid }

func (l *localPipes) Wait() (int, error) {
	err := l.cmd.Wait()
	reapGroup(l.cmd) // whatever the agent left running in its process group
	code := -1
	if st := l.cmd.ProcessState; st != nil {
		code = st.ExitCode()
	}
	return code, err
}

// hostPipes adapts a session host to pipes.
type hostPipes struct{ *host.Conn }

func (h hostPipes) Stderr() string { return strings.TrimSpace(h.Conn.Stderr()) }

// handover is what a daemon that detached from a hosted claude leaves the next one (in the
// host's folder, a secret file): where in claude's output it stopped, at a line's end, and
// what it knew then.
type handover struct {
	Offset    uint64           `json:"offset"`
	SessionID string           `json:"session_id"`
	State     model.AgentState `json:"state"`
	Busy      bool             `json:"busy"`
	// Asked are claude's permission requests not answered yet, as they were asked.
	Asked  map[string]runner.ApprovalRequest `json:"asked"`
	InitID string                            `json:"init_id"`
}
