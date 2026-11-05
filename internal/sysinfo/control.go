package sysinfo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MaxPause is a hard lease, independent of resource policy or daemon health.
const MaxPause = 30 * time.Second

var ErrProtected = errors.New("process is protected from automatic control")

// ProtectedProcess protects recovery services and interactive desktop programs even
// when a snapshot claims they belong to an agent. Missing image names fail closed.
func ProtectedProcess(name string) bool {
	name = strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "", ".", "explorer", "winlogon", "csrss", "lsass", "services", "smss", "dwm", "sihost", "conhost", "windowsterminal", "openconsole", "tailscale", "tailscaled", "tailscale-ipn", "python", "pythonw", "mstsc", "rdpclip", "tmx-desktop", "tmxg-desktop", "sshd", "ssh", "tmx", "tmxg", "tmx-go", "tmx-ui", "terminalmatrix", "terminalmatrix2", "terminal", "finder", "loginwindow", "windowserver":
		return true
	}
	return false
}

type trackedChild struct {
	proc   Proc
	exempt bool
	// anchor is the process the child's ownership is proven by: its parent, verified by PID
	// and start time. Zero = the daemon itself (RegisterChild); else a session host
	// (RegisterHosted).
	anchor Proc
}

var children = struct {
	sync.Mutex
	roots map[int]trackedChild
}{roots: map[int]trackedChild{}}

// RegisterChild records the identity immediately after a successful Start. Passing
// exempt protects the owner's shell and its whole subtree. Failure grants no control.
func RegisterChild(pid int, exempt bool) {
	rows, err := Procs()
	if err != nil {
		return
	}
	p, ok := find(rows, pid)
	me, mok := find(rows, os.Getpid())
	if !ok || !mok || p.PPID != me.PID || p.Start <= 0 || me.Start <= 0 || p.Start < me.Start {
		return
	}
	children.Lock()
	defer children.Unlock()
	for id, old := range children.roots {
		now, exists := find(rows, id)
		if !exists || now.Start != old.proc.Start {
			delete(children.roots, id)
		}
	}
	children.roots[pid] = trackedChild{p, exempt, Proc{}}
}

// RegisterHosted records a program an agent's session host started (runner/host): the host,
// not the daemon, is its parent, so the host's identity (PID and start time, from its
// host.json) anchors it instead. It counts only if the program's parent really is that host, a
// tmx host copy (tmx-host-*) started no later than the program. Called after a start and after
// a daemon reattached, so brakes and stops reach hosted agents like the daemon's own children.
func RegisterHosted(pid, hostPID int, hostStart int64, exempt bool) {
	rows, err := Procs()
	if err != nil {
		return
	}
	p, ok := find(rows, pid)
	h, hok := find(rows, hostPID)
	if !ok || !hok || hostStart <= 0 || h.Start != hostStart || p.PPID != h.PID || p.Start <= 0 || p.Start < h.Start || !hostImage(h.Name) {
		return
	}
	children.Lock()
	defer children.Unlock()
	for id, old := range children.roots {
		now, exists := find(rows, id)
		if !exists || now.Start != old.proc.Start {
			delete(children.roots, id)
		}
	}
	children.roots[pid] = trackedChild{p, exempt, h}
}

// KillHost ends a session host by the identity it recorded (runner/host): only if pid is still
// the process with that start time and image name, a tmx-host-* copy; else ErrNotChild. It
// pins the process before the kill (openIdentity checks the start time on the pinned handle),
// so a reused PID is never hit. Never by name.
func KillHost(pid int, start int64, name string) error {
	if pid <= 0 || start <= 0 || !hostImage(name) {
		return ErrNotChild
	}
	rows, err := Procs()
	if err != nil {
		return err
	}
	p, ok := find(rows, pid)
	if !ok || p.Start != start || p.Name != name {
		return ErrNotChild
	}
	h, err := openIdentity(p)
	if err != nil {
		return err
	}
	defer h.close()
	return h.kill()
}

// IsHostImage reports whether name (a path or an image name) is a session host's executable:
// the daemon's copy of tmx, tmx-host-<hash> (cli/hostexe.go).
func IsHostImage(name string) bool { return hostImage(name) }

func hostImage(name string) bool {
	name = strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	return strings.HasPrefix(name, "tmx-host-")
}

// captureControl prunes protected subtrees, checks every edge and requires the
// registered root identity. PGIDs and stale parent IDs never establish ownership.
func captureControl(rows []Proc, root, daemon Proc) ([]Proc, error) {
	current, ok := find(rows, root.PID)
	parent, pok := find(rows, daemon.PID)
	if !ok || !pok || root.Start <= 0 || daemon.Start <= 0 || current.Start != root.Start || parent.Start != daemon.Start || current.PPID != daemon.PID || current.Start < parent.Start {
		return nil, ErrNotChild
	}
	if ProtectedProcess(current.Name) {
		return nil, ErrProtected
	}
	accepted := map[int]bool{root.PID: true}
	var out []Proc
	for _, p := range Tree(rows, root.PID) {
		if ProtectedProcess(p.Name) || (p.PID != root.PID && !accepted[p.PPID]) {
			continue
		}
		accepted[p.PID] = true
		out = append(out, p)
	}
	return out, nil
}

func registeredTree(pid int) ([]Proc, Proc, error) { return registeredTreeFor(pid, true) }

func registeredTreeFor(pid int, brake bool) ([]Proc, Proc, error) {
	children.Lock()
	child, ok := children.roots[pid]
	children.Unlock()
	if !ok {
		return nil, Proc{}, ErrNotChild
	}
	if child.exempt && brake {
		return nil, Proc{}, ErrProtected
	}
	rows, err := Procs()
	if err != nil {
		return nil, Proc{}, err
	}
	anchor, ok := find(rows, os.Getpid())
	if !ok {
		return nil, Proc{}, ErrNotChild
	}
	if child.anchor.PID != 0 { // a hosted program: its host, still the same process
		h, ok := find(rows, child.anchor.PID)
		if !ok || h.Start != child.anchor.Start || !hostImage(h.Name) {
			return nil, Proc{}, ErrNotChild
		}
		anchor = h
	}
	tree, err := captureControl(rows, child.proc, anchor)
	return tree, anchor, err
}

// controlledProcess pins an OS process object. Actions never reopen a numeric PID.
type controlledProcess interface {
	alive() bool
	suspend() error
	resume() error
	kill() error
	close()
}

// KillTree only terminates a registered, verified tree. No taskkill /T or group
// signals are used: those can include processes not represented by the snapshot.
func KillTree(pid int) error {
	tree, daemon, err := registeredTreeFor(pid, false)
	if err != nil {
		return err
	}
	parent, err := openIdentity(daemon)
	if err != nil {
		return err
	}
	defer parent.close()
	handles, err := pinKillCandidates(tree, daemon, openControlled, Procs)
	if err != nil {
		return err
	}
	defer closeProcesses(handles)
	return killPinned(handles)
}

// PinStopTree captures verified process handles before a graceful stop can make
// children lose their parent. The returned function kills that exact snapshot
// and releases every handle, once. It never discovers children by an old PID.
func PinStopTree(pid int, expectedStart int64) (func() error, error) {
	tree, daemon, err := registeredTreeFor(pid, false)
	if err != nil {
		return nil, err
	}
	if expectedStart <= 0 || len(tree) == 0 || tree[0].Start != expectedStart {
		return nil, ErrNotChild
	}
	parent, err := openIdentity(daemon)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	handles, err := pinKillCandidates(tree, daemon, openControlled, Procs)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() { result = killPinned(handles); closeProcesses(handles) })
		return result
	}, nil
}

// RegisteredTree is a read-only snapshot anchored to a child this daemon started.
func RegisteredTree(pid int) ([]Proc, error) {
	tree, _, err := registeredTreeFor(pid, false)
	return tree, err
}

// KillStarted is for an explicit Stop/cancellation. The original exec handle
// always ends the launched root, even if tree inspection failed or it was exempt
// from automatic resource brakes. It cannot target a replacement numeric PID.
func KillStarted(p *os.Process) error {
	if p == nil {
		return ErrNotChild
	}
	_ = KillTree(p.Pid)
	err := p.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func killPinned(handles []controlledProcess) error {
	var errs []error
	for i := len(handles) - 1; i >= 0; i-- {
		if err := handles[i].kill(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// A failed descendant does not prevent terminating the root. An unreadable
// ancestor excludes its subtree, while all remaining pinned identities are
// checked against a second snapshot before any termination.
func pinKillCandidates(tree []Proc, daemon Proc, open func(Proc) (controlledProcess, error), snapshot func() ([]Proc, error)) ([]controlledProcess, error) {
	if len(tree) == 0 {
		return nil, ErrNotChild
	}
	var ps []controlledProcess
	var captured []Proc
	held := map[int]bool{}
	for i, p := range tree {
		if i > 0 && !held[p.PPID] {
			continue
		}
		h, err := open(p)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			continue
		}
		ps = append(ps, h)
		captured = append(captured, p)
		held[p.PID] = true
	}
	rows, err := snapshot()
	var fresh []Proc
	if err == nil {
		fresh, err = captureControl(rows, tree[0], daemon)
	}
	if err != nil {
		closeProcesses(ps)
		return nil, err
	}
	var valid []controlledProcess
	verified := map[int]bool{}
	for i, p := range captured {
		now, ok := find(fresh, p.PID)
		if !ok || now.Start != p.Start || now.PPID != p.PPID || now.Name != p.Name || (i > 0 && !verified[p.PPID]) {
			ps[i].close()
			if i == 0 {
				closeProcesses(ps[1:])
				return nil, ErrNotChild
			}
			continue
		}
		verified[p.PID] = true
		valid = append(valid, ps[i])
	}
	return valid, nil
}

func closeProcesses(ps []controlledProcess) {
	for _, p := range ps {
		p.close()
	}
}

func pinTree(tree []Proc, daemon Proc) ([]controlledProcess, error) {
	if len(tree) == 0 {
		return nil, ErrNotChild
	}
	// Hold the daemon and every ancestor while validating the second snapshot.
	parent, err := openIdentity(daemon)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	var ps []controlledProcess
	for _, p := range tree {
		h, e := openControlled(p)
		if e != nil {
			closeProcesses(ps)
			return nil, e
		}
		ps = append(ps, h)
	}
	rows, err := Procs()
	if err == nil {
		var fresh []Proc
		fresh, err = captureControl(rows, tree[0], daemon)
		if err == nil {
			for _, want := range tree {
				now, ok := find(fresh, want.PID)
				if !ok || now.Start != want.Start || now.PPID != want.PPID || now.Name != want.Name {
					err = ErrNotChild
					break
				}
			}
		}
	}
	if err != nil {
		closeProcesses(ps)
		return nil, err
	}
	return ps, nil
}

// ResumeProcs is restart recovery for the old ledger. Identity-pinned handles
// prevent signalling a replacement PID. Protected images are always skipped.
func ResumeProcs(list []Proc) int {
	n := 0
	for _, p := range list {
		if p.PID <= 0 || p.Start == 0 {
			continue
		}
		h, err := openResume(p)
		if err != nil {
			continue
		}
		if h.resume() == nil {
			n++
		}
		h.close()
	}
	return n
}
