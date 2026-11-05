package recworker

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/summarize"
)

type fakeFleet struct {
	mu        sync.Mutex
	launched  []fleet.LaunchRequest
	callers   []fleet.Caller
	agents    map[string]model.Agent
	removed   []string
	profile   *model.Profile
	launchErr error
}

func (f *fakeFleet) LaunchAgent(c fleet.Caller, r fleet.LaunchRequest) (model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.launchErr != nil {
		return model.Agent{}, f.launchErr
	}
	f.launched, f.callers = append(f.launched, r), append(f.callers, c)
	a := model.Agent{ID: "ag-" + r.Name, Name: r.Name, State: model.StateStarting}
	f.agents[a.ID] = a
	return a, nil
}
func (f *fakeFleet) Agent(id string) (model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[id]; ok {
		return a, nil
	}
	return model.Agent{}, errors.New("no such agent")
}
func (f *fakeFleet) set(id string, st model.AgentState, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	a.State, a.Error = st, msg
	f.agents[id] = a
}
func (f *fakeFleet) StopAgent(_ fleet.Caller, id string) (model.Agent, error) { return f.Agent(id) }
func (f *fakeFleet) RemoveAgent(_ fleet.Caller, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.agents, id)
	f.removed = append(f.removed, id)
	return nil
}
func (f *fakeFleet) GetProfile(string) (model.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.profile == nil {
		return model.Profile{}, errors.New("not found")
	}
	return *f.profile, nil
}
func (f *fakeFleet) CreateProfile(_ fleet.Caller, p model.Profile) (model.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profile = &p
	return p, nil
}
func (f *fakeFleet) UpdateProfile(c fleet.Caller, _ string, p model.Profile) (model.Profile, error) {
	return f.CreateProfile(c, p)
}

type fakeSum struct {
	mu       sync.Mutex
	auto     bool
	state    map[string]model.SummaryState
	failed   map[string]string
	finished []string
	here     []string // FinishedHere reasons
}

func (s *fakeSum) Finished(r model.Recording) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, r.ID)
	s.state[r.ID] = model.SummaryTranscribing
}
func (s *fakeSum) FinishedHere(r model.Recording, why string) {
	s.mu.Lock()
	s.here = append(s.here, why)
	s.mu.Unlock()
	s.Finished(r)
}
func (s *fakeSum) Get(id string) (model.RecordingSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state[id]
	if !ok {
		st = model.SummaryNone
	}
	return model.RecordingSummary{RecordingID: id, State: st}, nil
}
func (s *fakeSum) Fail(id, why string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed[id], s.state[id] = why, model.SummaryFailed
	return true, nil
}
func (s *fakeSum) Settings() summarize.Settings { return summarize.Settings{Auto: s.auto} }
func (s *fakeSum) set(id string, st model.SummaryState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[id] = st
}
func (s *fakeSum) made() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.finished)
}
func (s *fakeSum) why(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed[id]
}

type fakeRecs struct{}

func (fakeRecs) Get(id string) (model.Recording, error) {
	return model.Recording{ID: id, State: model.RecordingDone}, nil
}

type rig struct {
	s   *Service
	f   *fakeFleet
	sum *fakeSum
	now time.Time
}

func newRig(t *testing.T) *rig {
	r := &rig{f: &fakeFleet{agents: map[string]model.Agent{}}, sum: &fakeSum{auto: true, state: map[string]model.SummaryState{}, failed: map[string]string{}},
		now: time.Unix(1_800_000_000, 0)}
	r.s = New(Options{Fleet: r.f, Summaries: r.sum, Recordings: fakeRecs{}, Exe: `C:\tmx\tmx.exe`, Dir: t.TempDir(),
		Now: func() time.Time { return r.now }, Deadline: func(int) time.Duration { return 30 * time.Minute }})
	return r
}

// stop finishes a recording and waits for its worker's launch (Finished returns at once).
func (r *rig) stop(t *testing.T, id string) string {
	t.Helper()
	r.s.Finished(model.Recording{ID: id, State: model.RecordingDone, Segments: make([]model.RecordingSegment, 3)})
	for range 200 {
		if a := r.s.Tracking(id); a != "" {
			return a
		}
		if len(r.sum.made()) > 0 {
			return ""
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no worker launched")
	return ""
}

// Stop launches one cheap worker through the launch path with a profile that cannot read the recording.
func TestStopLaunchesAnOrchestratorOnlyWorker(t *testing.T) {
	r := newRig(t)
	r.stop(t, "rec_20261103_AbC-123")
	if len(r.f.launched) != 1 {
		t.Fatalf("launched %d workers", len(r.f.launched))
	}
	req := r.f.launched[0]
	if req.Model != Model || req.Level != model.LevelWorker || req.Profile != ProfileSlug || req.Harness != model.HarnessClaude {
		t.Fatalf("launch request: %+v", req)
	}
	if !model.ValidName(req.Name) {
		t.Fatalf("invalid agent name %q", req.Name)
	}
	if !strings.Contains(req.Prompt, `C:\tmx\tmx.exe rec process rec_20261103_AbC-123`) {
		t.Fatalf("prompt: %q", req.Prompt)
	}
	if !r.f.callers[0].Owner {
		t.Fatal("launched on the owner's behalf (the owner recorded)")
	}
	p := r.f.profile
	if p == nil || p.PermissionMode != "dontAsk" || p.ReadOnly || !slices.Equal(p.AllowedTools, []string{`Bash(C:\tmx\tmx.exe rec process:*)`}) {
		t.Fatalf("profile: %+v", p)
	}
	for _, tool := range []string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"} {
		if !slices.Contains(p.DisallowedTools, tool) {
			t.Fatalf("profile must deny %s: %v", tool, p.DisallowedTools)
		}
	}
}

// Only the worker launched for the recording may process it, and asking twice starts the report once.
func TestProcessIsBoundToItsWorker(t *testing.T) {
	r := newRig(t)
	ag := r.stop(t, "rec1")
	for _, c := range []fleet.Caller{fleet.AgentCaller("someone-else"), {}, fleet.CompanionCaller} {
		if _, _, err := r.s.Process(c, "rec1"); fleet.KindOf(err) != fleet.KindForbidden {
			t.Fatalf("caller %+v: want forbidden, got %v", c, err)
		}
	}
	for range 2 {
		st, _, err := r.s.Process(fleet.AgentCaller(ag), "rec1")
		if err != nil || st != model.SummaryTranscribing {
			t.Fatalf("process: %s %v", st, err)
		}
	}
	if len(r.sum.finished) != 1 {
		t.Fatalf("report started %d times", len(r.sum.finished))
	}
}

// A worker that cannot do its job (it fails, ends, waits for a person or idles before it asked) never loses the
// report: the daemon makes it itself, once, and the worker is removed; the deadline still holds.
func TestWorkerFailureTheDaemonMakesTheReport(t *testing.T) {
	for _, st := range []model.AgentState{model.StateError, model.StateExited, model.StateWaiting, model.StateIdle} {
		r := newRig(t)
		ag := r.stop(t, "rec1")
		r.f.set(ag, st, "claude exited 1")
		r.s.Check()
		r.s.Check()
		if got := r.sum.made(); !slices.Equal(got, []string{"rec1"}) || r.sum.why("rec1") != "" {
			t.Fatalf("%s: made %v, failed %q", st, got, r.sum.why("rec1"))
		}
		if len(r.sum.here) != 1 || !strings.HasPrefix(r.sum.here[0], "the report worker rec-rec1 didn't make it (") {
			t.Fatalf("%s: the report must say why the daemon made it: %q", st, r.sum.here)
		}
		if !slices.Contains(r.f.removed, ag) || r.s.Tracking("rec1") != "" {
			t.Fatalf("%s: the worker is removed", st)
		}
		r.now = r.now.Add(31 * time.Minute) // still tracked: a report the daemon does not finish fails at the deadline
		r.s.Check()
		if !strings.Contains(r.sum.why("rec1"), "not finished in time") {
			t.Fatalf("%s: deadline: %q", st, r.sum.why("rec1"))
		}
	}
}

// When the report is made the worker is removed and nothing is marked failed.
func TestReportDoneRemovesTheWorker(t *testing.T) {
	r := newRig(t)
	ag := r.stop(t, "rec1")
	r.f.set(ag, model.StateWorking, "")
	if _, _, err := r.s.Process(fleet.AgentCaller(ag), "rec1"); err != nil {
		t.Fatal(err)
	}
	r.s.Check()
	if slices.Contains(r.f.removed, ag) {
		t.Fatal("removed while the report is still being made")
	}
	r.sum.set("rec1", model.SummaryDone)
	r.s.Check()
	if !slices.Contains(r.f.removed, ag) || r.sum.why("rec1") != "" {
		t.Fatalf("removed %v, failed %q", r.f.removed, r.sum.why("rec1"))
	}
}

// A worker gone after it asked leaves the report to the daemon: not failed, but the deadline still holds.
func TestWorkerGoneAfterAskingKeepsTheReportThenDeadline(t *testing.T) {
	r := newRig(t)
	ag := r.stop(t, "rec1")
	if _, _, err := r.s.Process(fleet.AgentCaller(ag), "rec1"); err != nil {
		t.Fatal(err)
	}
	r.f.set(ag, model.StateExited, "")
	r.s.Check()
	if r.sum.why("rec1") != "" {
		t.Fatalf("failed too early: %q", r.sum.why("rec1"))
	}
	r.now = r.now.Add(31 * time.Minute)
	r.s.Check()
	if why := r.sum.why("rec1"); !strings.Contains(why, "not finished in time (30m0s after Stop)") {
		t.Fatalf("deadline: %q", why)
	}
}

// A worker that cannot be launched fails the report with the launch error.
// A worker that cannot be launched (no claude on this machine, over its limits) never loses the report: the
// daemon makes it itself.
func TestLaunchErrorTheDaemonMakesTheReport(t *testing.T) {
	r := newRig(t)
	r.f.launchErr = errors.New("start failed: claude not found on PATH")
	r.s.Finished(model.Recording{ID: "rec1", State: model.RecordingDone})
	for range 200 {
		if len(r.sum.made()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.sum.made(); !slices.Equal(got, []string{"rec1"}) || r.sum.why("rec1") != "" {
		t.Fatalf("made %v, failed %q", got, r.sum.why("rec1"))
	}
	if len(r.sum.here) != 1 || r.sum.here[0] != "the report worker couldn't start (start failed: claude not found on PATH)" {
		t.Fatalf("why: %q", r.sum.here)
	}
}

// Without automatic summaries no worker is launched: the summary service decides as before.
func TestAutoOffLeavesItToSummaries(t *testing.T) {
	r := newRig(t)
	r.sum.auto = false
	r.s.Finished(model.Recording{ID: "rec1", State: model.RecordingDone})
	r.s.Finished(model.Recording{ID: "rec2", State: model.RecordingFailed})
	if len(r.f.launched) != 0 || !slices.Equal(r.sum.finished, []string{"rec1"}) {
		t.Fatalf("launched %d, finished %v", len(r.f.launched), r.sum.finished)
	}
}
