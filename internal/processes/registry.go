// Package processes owns explicitly launched tools and their supervision.
package processes

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/host"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

type Spec struct {
	OwnerID, ProjectID, Purpose, Command, Cwd string
	Args                                      []string
	System                                    bool // trusted daemon code only
}

type entry struct {
	info     model.ProcessInfo
	spec     Spec
	cmd      *exec.Cmd  // a tool of the daemon's own (system tools, or no hosts)
	conn     *host.Conn // a tool in a session host (hosted.go)
	dir      string     // its host folder, for a hosted tool
	exited   chan struct{}
	done     chan struct{}
	cancel   chan struct{}
	stopping bool
	detached bool // Detach: it runs on in its host; its end here is not an exit
	unknown  bool // adopted, but its host could not be checked (hosted.go)
	identity int64
}

type Registry struct {
	mu      sync.Mutex
	entries map[string]*entry
	closed  bool
	// hostExe and dir: agents' tools run in session hosts that outlive the daemon
	// (EnableHosts); "" = the daemon's own children.
	hostExe, dir string
}

func New() *Registry { return &Registry{entries: make(map[string]*entry)} }

// Start records the child before publishing it; all starts and ownership changes serialize.
func (r *Registry) Start(s Spec) (model.ProcessInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return model.ProcessInfo{}, errors.New("process registry closed")
	}
	if s.OwnerID == "" || s.Purpose == "" || s.Command == "" {
		return model.ProcessInfo{}, errors.New("owner, purpose and command are required")
	}
	s.Args = append([]string(nil), s.Args...)
	e := &entry{spec: s, done: make(chan struct{}), cancel: make(chan struct{})}
	e.info = model.ProcessInfo{ID: model.NewID(), Category: model.CategoryAgentTool, OwnerID: s.OwnerID, ProjectID: s.ProjectID, Purpose: s.Purpose, Command: filepath.Base(s.Command)}
	e.info.CreatorID = s.OwnerID
	if s.System {
		e.info.Category = model.CategorySystemTool
	}
	err := r.spawn(e)
	if err != nil && !s.System {
		return model.ProcessInfo{}, err
	}
	r.entries[e.info.ID] = e
	go r.supervise(e)
	return e.info, nil
}

// spawn and fields of entry are guarded by mu; Wait alone owns cmd.Wait.
func (r *Registry) spawn(e *entry) error {
	if r.hosted(e) {
		return r.spawnHosted(e) // hosted.go
	}
	cmd := hiddenexec.Command(e.spec.Command, e.spec.Args...)
	cmd.Dir = e.spec.Cwd
	// Do not delegate the daemon's bearer credentials or harness identity to tools.
	cmd.Env = toolEnv(os.Environ())
	if err := cmd.Start(); err != nil {
		e.cmd = nil
		e.info.PID = 0
		e.info.State = "retrying"
		return err
	}
	sysinfo.RegisterChild(cmd.Process.Pid, false)
	e.identity = 0
	if tree, err := sysinfo.RegisteredTree(cmd.Process.Pid); err == nil && len(tree) > 0 {
		e.identity = tree[0].Start
	}
	e.cmd = cmd
	e.exited = make(chan struct{})
	e.info.PID = cmd.Process.Pid
	e.info.StartedAt = model.Now()
	e.info.State = "running"
	go func(done chan struct{}) { _ = cmd.Wait(); close(done) }(e.exited)
	return nil
}

func (r *Registry) supervise(e *entry) {
	defer func() {
		r.mu.Lock()
		if e.stopping && !e.detached {
			e.info.PID = 0
			e.info.State = "stopped"
		}
		r.mu.Unlock()
		close(e.done)
	}()
	for {
		r.mu.Lock()
		exited := e.exited
		running := e.cmd != nil || e.conn != nil
		r.mu.Unlock()
		if running {
			select {
			case <-exited:
			case <-e.cancel:
				r.mu.Lock()
				detached := e.detached
				r.mu.Unlock()
				if !detached {
					r.terminate(e)
				}
				return
			}
		}
		r.mu.Lock()
		if e.detached { // the daemon left it running in its host
			r.mu.Unlock()
			return
		}
		e.info.PID = 0
		e.info.State = "exited"
		if e.conn != nil {
			r.forgetHosted(e) // hosted.go: its host has ended with it
		}
		if e.stopping || !e.spec.System {
			r.mu.Unlock()
			return
		}
		e.info.State = "retrying"
		r.mu.Unlock()
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-e.cancel:
			timer.Stop()
			return
		case <-timer.C:
		}
		r.mu.Lock()
		if e.stopping {
			r.mu.Unlock()
			return
		}
		e.info.Restarts++
		_ = r.spawn(e)
		r.mu.Unlock()
	}
}

func (r *Registry) terminate(e *entry) {
	r.mu.Lock()
	cmd, conn, exited, identity := e.cmd, e.conn, e.exited, e.identity
	e.info.State = "stopping"
	r.mu.Unlock()
	switch {
	case conn != nil:
		r.terminateHosted(e, conn, exited) // hosted.go
	case cmd != nil:
		// Pin before graceful exit can reparent descendants; PID reuse cannot
		// redirect this cleanup to a replacement process.
		if cleanup, err := sysinfo.PinStopTree(cmd.Process.Pid, identity); err == nil {
			defer cleanup()
		}
		// Unix SIGTERM; Windows cannot signal a hidden console gracefully. Give
		// already-exiting children a grace period before the verified handle kill.
		graceful(cmd.Process)
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-exited:
			timer.Stop()
		case <-timer.C:
			// Original os.Process handle only; descendants use the pinned snapshot.
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	r.mu.Lock()
	e.info.PID = 0
	e.info.State = "stopped"
	r.mu.Unlock()
}

func (r *Registry) List() []model.ProcessInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.ProcessInfo, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Promote(id, owner, project string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entries[id]
	if e == nil || e.stopping || e.info.State != "running" || e.spec.System {
		return errors.New("no running agent tool")
	}
	e.info.OwnerID = owner
	e.info.ProjectID = project
	e.info.Promoted = true
	if e.conn != nil {
		r.saveMeta(e) // hosted.go: the next daemon adopts it with its new owner
	}
	return nil
}

func (r *Registry) stopMatching(match func(model.ProcessInfo) bool) {
	r.mu.Lock()
	var wait []chan struct{}
	for _, e := range r.entries {
		if match(e.info) {
			if !e.stopping {
				e.stopping = true
				close(e.cancel)
			}
			wait = append(wait, e.done)
		}
	}
	r.mu.Unlock()
	for _, done := range wait {
		<-done
	}
}
func (r *Registry) Stop(id string) {
	r.stopMatching(func(p model.ProcessInfo) bool { return p.ID == id })
}
func (r *Registry) StopOwner(owner string) {
	r.stopMatching(func(p model.ProcessInfo) bool { return p.OwnerID == owner && p.Category != model.CategorySystemTool })
}

func (r *Registry) StopProject(project string) {
	r.stopMatching(func(p model.ProcessInfo) bool {
		return p.ProjectID == project && p.Category != model.CategorySystemTool
	})
}

// Close stops every tool. A daemon that is restarting uses Detach instead.
func (r *Registry) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stopMatching(func(model.ProcessInfo) bool { return true })
}
