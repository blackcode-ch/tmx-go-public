package processes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/host"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// Agents' tools (preview servers, watchers) run in session hosts of their own, like the agents
// (runner/host): a daemon restart detaches from them (Detach) and the next daemon adopts them
// (Adopt), so a restart or an update never stops them. Each has a folder <dir>/<id> with its
// meta.json (what tmx knows of it) and the host's folder. The daemon's own system tools stay
// its children: it restarts them itself.

// EnableHosts makes agents' tools run in session hosts started from exe, kept under dir.
func (r *Registry) EnableHosts(exe, dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostExe, r.dir = exe, dir
}

func (r *Registry) hosted(e *entry) bool { return r.hostExe != "" && r.dir != "" && !e.spec.System }

func (r *Registry) toolDir(id string) string { return filepath.Join(r.dir, id) }

// meta is what a daemon leaves of a hosted tool for the next one.
type meta struct {
	Info model.ProcessInfo `json:"info"`
	Spec Spec              `json:"spec"`
}

func (r *Registry) saveMeta(e *entry) {
	b, err := json.Marshal(meta{Info: e.info, Spec: e.spec})
	if err == nil {
		_ = config.WriteFileAtomic(filepath.Join(r.toolDir(e.info.ID), "meta.json"), b, 0o600)
	}
}

// spawnHosted starts the tool in a session host. Called with r.mu held.
func (r *Registry) spawnHosted(e *entry) error {
	dir := r.toolDir(e.info.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.dir = filepath.Join(dir, "host")
	c, err := host.Spawn(ctx, r.hostExe, e.dir, host.Spec{Path: e.spec.Command, Args: e.spec.Args, Cwd: e.spec.Cwd,
		// Do not delegate the daemon's bearer credentials or harness identity to tools.
		Env: toolEnv(os.Environ()), Piped: true})
	if err != nil {
		_ = os.RemoveAll(dir)
		e.info.PID = 0
		e.info.State = "retrying"
		return err
	}
	e.info.StartedAt = model.Now()
	r.attach(e, c)
	r.saveMeta(e)
	return nil
}

// attach makes c the entry's program: registered for the resource brakes (anchored to its
// host), its output drained (a tool's output is not shown), its end watched.
func (r *Registry) attach(e *entry, c *host.Conn) {
	sysinfo.RegisterHosted(c.PID(), c.HostPID(), c.HostStart(), false)
	e.conn = c
	e.exited = make(chan struct{})
	e.info.PID = c.PID()
	e.info.State = "running"
	go func() { _, _ = io.Copy(io.Discard, c) }()
	go func(done chan struct{}) { _, _ = c.Wait(); close(done) }(e.exited)
}

// terminateHosted asks the host to end the tool's tree, kills it after a grace, and, should the
// host not answer, ends it by the identity it recorded (host.Stop; never by name).
func (r *Registry) terminateHosted(e *entry, c *host.Conn, exited chan struct{}) {
	c.Terminate()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		c.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_, _ = host.Stop(e.dir)
		}
	}
	r.mu.Lock()
	r.forgetHosted(e)
	r.mu.Unlock()
}

// forgetHosted removes a hosted tool's folder once it ended. Called with r.mu held.
func (r *Registry) forgetHosted(e *entry) {
	if r.dir != "" && validID(e.info.ID) {
		_ = os.RemoveAll(r.toolDir(e.info.ID))
	}
	e.conn = nil
}

// validID: a folder name of ours (model.NewID), never a path.
func validID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\:`)
}

// Detach is Close for a daemon that is restarting: hosted tools are left running in their
// hosts (the next daemon adopts them); the daemon's own tools are stopped as before.
func (r *Registry) Detach() {
	r.mu.Lock()
	r.closed = true
	var wait []chan struct{}
	for _, e := range r.entries {
		if e.stopping {
			wait = append(wait, e.done)
			continue
		}
		e.stopping = true
		if e.conn != nil || e.unknown {
			e.detached = true
		}
		if e.conn != nil {
			_ = e.conn.Close()
		}
		close(e.cancel)
		wait = append(wait, e.done)
	}
	r.mu.Unlock()
	for _, done := range wait {
		<-done
	}
}

// Adopt takes over the hosted tools an earlier daemon left running: every one whose host
// still runs is supervised again (same PID). One whose host is proven gone is forgotten. One
// that cannot be checked is listed as "unknown" (never started a second time): the owner sees
// it, and a stop ends it by the identity its host recorded (host.Stop).
func (r *Registry) Adopt() []model.ProcessInfo {
	r.mu.Lock()
	dir := r.dir
	r.mu.Unlock()
	if dir == "" {
		return nil
	}
	dirs, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []model.ProcessInfo
	for _, d := range dirs {
		id := d.Name()
		if !d.IsDir() || !validID(id) {
			continue
		}
		toolDir := filepath.Join(dir, id)
		var m meta
		b, err := os.ReadFile(filepath.Join(toolDir, "meta.json"))
		if err != nil || json.Unmarshal(b, &m) != nil || m.Info.ID != id || m.Spec.System {
			_ = os.RemoveAll(toolDir) // not one of ours, or half made
			continue
		}
		hostDir := filepath.Join(toolDir, "host")
		// Outside r.mu: a host that does not answer must not hold up the daemon's start.
		c, err := dialHost(hostDir, host.End) // its output is not shown: only what comes next
		if err != nil && !errors.Is(err, host.ErrHostUnknown) {
			_ = os.RemoveAll(toolDir)
			continue
		}
		e := &entry{info: m.Info, spec: m.Spec, dir: hostDir, done: make(chan struct{}), cancel: make(chan struct{})}
		r.mu.Lock()
		if r.closed || r.entries[id] != nil {
			r.mu.Unlock()
			if c != nil {
				_ = c.Close()
			}
			continue
		}
		if c != nil {
			r.attach(e, c)
			go r.supervise(e)
		} else {
			e.unknown = true
			e.info.State = "unknown"
			go r.superviseUnknown(e)
		}
		r.entries[id] = e
		out = append(out, e.info)
		r.mu.Unlock()
	}
	return out
}

// superviseUnknown waits for a stop of a tool whose host could not be checked: it ends it by
// the identity its host recorded, never by name. A daemon that detaches leaves it as it is.
func (r *Registry) superviseUnknown(e *entry) {
	defer close(e.done)
	<-e.cancel
	r.mu.Lock()
	detached := e.detached
	r.mu.Unlock()
	if detached {
		return
	}
	_, err := stopHost(e.dir)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		e.info.State = "unknown" // still not verifiable: a later stop tries again
		e.stopping = false
		e.cancel = make(chan struct{})
		e.done = make(chan struct{})
		go r.superviseUnknown(e)
		return
	}
	e.info.PID = 0
	e.info.State = "stopped"
	r.forgetHosted(e)
}

// stopHost and dialHost are host.Stop and host.Dial; variables for tests.
var (
	stopHost = host.Stop
	dialHost = host.Dial
)
