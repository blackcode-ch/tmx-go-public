package sysinfo

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

const watchdogArg = "--internal-process-resume-watchdog"

type watchRequest struct {
	Daemon Proc
	Tree   []Proc
}
type watchResponse struct {
	Procs   []Proc
	Error   string
	Resumed bool
}
type pauseLease struct {
	input   io.WriteCloser
	done    chan struct{}
	procs   []Proc
	process *os.Process
}

var leases = struct {
	sync.Mutex
	byRoot map[int]*pauseLease
}{byRoot: map[int]*pauseLease{}}

// init exits before application startup. The helper has no daemon, listeners,
// config, ledger, credentials or install path. It only owns a single bounded lease.
func init() {
	if len(os.Args) == 2 && os.Args[1] == watchdogArg {
		code := watchdogMain(os.Stdin, os.Stdout)
		os.Exit(code)
	}
}

// SuspendTree starts an independent helper BEFORE anything is suspended. The
// helper itself performs suspension and always resumes its pinned handles after
// 30 seconds, parent exit, or closing the private pipe (manual resume).
func SuspendTree(root int, stop bool) ([]Proc, error) {
	leases.Lock()
	defer leases.Unlock()
	if lease := leases.byRoot[root]; lease != nil {
		if stop {
			return nil, errors.New("process already has a pause lease")
		}
		_ = lease.input.Close()
		<-lease.done
		delete(leases.byRoot, root)
		return lease.procs, nil
	}
	if !stop {
		tree, _, err := registeredTree(root)
		if err != nil {
			return nil, err
		}
		ResumeProcs(tree)
		return tree, nil
	}
	tree, daemon, err := registeredTree(root)
	if err != nil {
		return nil, err
	}
	// Fail closed on platforms without pinned process control, before spawning.
	pinned, err := pinTree(tree, daemon)
	if err != nil {
		return nil, err
	}
	closeProcesses(pinned)
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := hiddenexec.Command(exe, watchdogArg)
	watchdogCommand(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return nil, err
	}
	done := make(chan struct{})
	lease := &pauseLease{input: in, done: done, procs: tree, process: cmd.Process}
	leases.byRoot[root] = lease
	go func() {
		<-done
		in.Close()
		leases.Lock()
		if leases.byRoot[root] == lease {
			delete(leases.byRoot, root)
		}
		leases.Unlock()
	}()
	// Wait only after the acknowledgement reader finishes (StdoutPipe contract).
	ack := make(chan watchResponse, 1)
	go func() {
		dec := json.NewDecoder(io.LimitReader(out, 2<<20))
		var r watchResponse
		firstErr := dec.Decode(&r)
		if firstErr != nil {
			r.Error = firstErr.Error()
		}
		ack <- r
		confirmed := false
		if firstErr == nil {
			var final watchResponse
			if dec.Decode(&final) == nil {
				confirmed = final.Resumed
			}
		}
		_ = cmd.Wait()
		recoverLease(confirmed, func() { ResumeProcs(tree) })
		close(done)
	}()
	if err = json.NewEncoder(in).Encode(watchRequest{daemon, tree}); err != nil {
		in.Close()
		return nil, err
	}
	var response watchResponse
	select {
	case response = <-ack:
	case <-time.After(5 * time.Second):
		in.Close()
		return nil, errors.New("resume watchdog did not acknowledge")
	}
	if response.Error != "" {
		in.Close()
		<-done
		return nil, errors.New(response.Error)
	}

	return response.Procs, nil
}

func watchdogMain(input io.Reader, output io.Writer) int {
	// Also bounds malformed callers that never supply a request.
	timer := time.NewTimer(MaxPause)
	defer timer.Stop()
	reqs := make(chan watchRequest, 1)
	go func() {
		var req watchRequest
		_ = json.NewDecoder(io.LimitReader(input, 1<<20)).Decode(&req)
		reqs <- req
	}()
	var req watchRequest
	select {
	case req = <-reqs:
	case <-timer.C:
		return 1
	}
	rows, err := Procs()
	if err != nil {
		return 1
	}
	self, ok := find(rows, os.Getpid())
	if !ok || self.PPID != req.Daemon.PID || req.Daemon.Start <= 0 || self.Start < req.Daemon.Start {
		return 1
	}
	daemon, err := openIdentity(req.Daemon)
	if err != nil {
		return 1
	}
	defer daemon.close()
	ps, err := pinTree(req.Tree, req.Daemon)
	if err != nil {
		_ = json.NewEncoder(output).Encode(watchResponse{Error: err.Error()})
		return 1
	}
	defer closeProcesses(ps)
	released := make(chan struct{})
	go func() { var b [1]byte; _, _ = input.Read(b[:]); close(released) }()
	// The private pipe closes on daemon death; a second identity check also handles
	// accidental pipe inheritance. This monitoring process is outside the agent tree.
	dead := make(chan struct{})
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-ticker.C:
				if !daemon.alive() {
					close(dead)
					return
				}
			}
		}
	}()
	err = runPauseLease(ps, func() error { return json.NewEncoder(output).Encode(watchResponse{Procs: req.Tree}) }, released, dead, timer.C)
	if err == nil {
		_ = json.NewEncoder(output).Encode(watchResponse{Resumed: true})
	}
	if err != nil {
		return 1
	}
	return 0
}

// runPauseLease pairs exactly one resume with each successful suspend, including
// partial failure and failed acknowledgement. Channels make crash/deadline tests
// deterministic without touching any live process.
func runPauseLease(ps []controlledProcess, ready func() error, release, dead <-chan struct{}, deadline <-chan time.Time) (result error) {
	var suspended []controlledProcess
	defer func() {
		for i := len(suspended) - 1; i >= 0; i-- {
			result = errors.Join(result, suspended[i].resume())
		}
	}()
	for _, p := range ps {
		select {
		case <-release:
			return errors.New("pause cancelled")
		case <-dead:
			return errors.New("daemon exited")
		case <-deadline:
			return errors.New("pause expired")
		default:
		}
		if err := p.suspend(); err != nil {
			return err
		}
		suspended = append(suspended, p)
	}
	ack := make(chan error, 1)
	go func() { ack <- ready() }()
	select {
	case err := <-ack:
		if err != nil {
			return err
		}
	case <-release:
		return errors.New("pause cancelled")
	case <-dead:
		return errors.New("daemon exited")
	case <-deadline:
		return errors.New("pause expired")
	}
	select {
	case <-release:
	case <-dead:
	case <-deadline:
	}
	return nil
}

func recoverLease(confirmed bool, resume func()) {
	if !confirmed {
		resume()
	}
}
