// Package host is an agent's session host (`tmx host`): a process of its own that runs the
// agent's program, so the agent outlives the daemon. A daemon restart or update detaches from
// the host; the next daemon reattaches (DESIGN.md "Restarts").
//
// Two kinds of program: a terminal one in a pseudo-terminal of the host's (terminal-mode
// agents; ConPTY on Windows), and a piped one, stdin/stdout/stderr (chat-mode agents: claude's
// stream-json). Either way the host numbers the program's output by byte offset and keeps the
// last part of it, so a reattaching daemon gets exactly what it has not handled yet (a piped
// program) or the recent screen (a terminal).
//
// The host listens on 127.0.0.1 and serves one daemon at a time over frames: a type byte, a
// 4-byte big-endian length, the payload. The daemon's first frame carries the offset it wants
// output from and the host's secret (host.json, a secret file: private to the user).
package host

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// Frames.
const (
	// The hello proves both sides hold the host's secret without either sending it: the daemon
	// sends a nonce, the host answers HMAC(secret, "host"‖its nonce) and a nonce of its own, the
	// daemon answers HMAC(secret, "daemon"‖that nonce). Nothing else passes before that.
	frameHello      = 'H' // daemon → host: uint64 offset to send output from, the daemon's nonce
	frameProof      = 'P' // host → daemon: HMAC(secret, "host"‖daemon's nonce), the host's nonce
	frameAuth       = 'U' // daemon → host: HMAC(secret, "daemon"‖host's nonce)
	frameWrite      = 'W' // daemon → host: input bytes
	frameCloseInput = 'C' // daemon → host: close the program's stdin (piped)
	frameResize     = 'R' // daemon → host: cols, rows (2 × uint16; terminal)
	frameTerminate  = 'T' // daemon → host: ask the program (and what it started) to end
	frameKill       = 'K' // daemon → host: kill it
	frameAck        = 'A' // daemon → host: the exit was seen; the host may end
	frameOutput     = 'O' // host → daemon: uint64 offset of the first byte, then output (the first, empty: where it starts)
	frameExit       = 'X' // host → daemon: int32 exit code, then the tail of stderr

	maxFrame = 8 << 20
)

// Files in an agent's host folder (fleet: <agent dir>/host, private like its secrets).
const (
	specFile  = "spec.json"  // what to run; the host deletes it once read (it holds the env)
	infoFile  = "host.json"  // where the running host listens, and its secret
	stateFile = "state.json" // what a detached daemon handed to the next one (Conn.SaveState)
)

// Spec is what a host runs.
type Spec struct {
	Path  string   `json:"path"`
	Args  []string `json:"args"`
	Env   []string `json:"env"`
	Cwd   string   `json:"cwd"`
	Piped bool     `json:"piped"` // stdin/stdout/stderr pipes, not a terminal
	Cols  int      `json:"cols"`
	Rows  int      `json:"rows"`
	// Token is the agent's tmx credential; the host hands it back to a reattaching daemon,
	// since the program keeps using it.
	Token string `json:"token"`
	// OrphanHours: with no daemon attached this long, the host ends the program and exits
	// (default 12): a host outlives a daemon restart, not a daemon that is gone for good.
	OrphanHours float64 `json:"orphan_hours"`
}

// info is a running host.
type info struct {
	PID      int    `json:"pid"`       // the host
	Start    int64  `json:"start"`     // the host's start time (sysinfo.Proc.Start): its identity
	Image    string `json:"image"`     // the host's executable name as the OS lists it (sysinfo.Proc.Name)
	ChildPID int    `json:"child_pid"` // the program
	Port     int    `json:"port"`
	Secret   string `json:"secret"`
	Token    string `json:"token"`
	Piped    bool   `json:"piped"`
	Protocol int    `json:"protocol"`
}

const protocol = 3

const nonceSize = 32

// proof is one side's answer to the other's nonce (role "host" or "daemon").
func proof(secret, role string, nonce []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(role))
	m.Write(nonce)
	return m.Sum(nil)
}

// ErrNoHost: there is no live session host to reattach to (proven gone).
var ErrNoHost = runner.ErrNoHost

// ErrHostUnknown: whether the host runs could not be told; do not start the agent again.
var ErrHostUnknown = runner.ErrHostUnknown

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > maxFrame {
		return fmt.Errorf("frame too large: %d", len(payload))
	}
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], uint32(len(payload)))
	copy(b[5:], payload)
	_, err := w.Write(b)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:5])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, nil, err
	}
	return h[0], b, nil
}

// writeJSON writes v to dir/name as a secret file (private to the user; an ACL on Windows).
func writeJSON(dir, name string, v any) error {
	if err := config.MkdirSecret(dir); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return config.WriteSecretFile(filepath.Join(dir, name), b)
}

func readJSON(dir, name string, v any) error {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
