// Package recworker makes a recording's report without anyone asked (the owner's rule "tool > agent", G5): when
// a recording is done, the recorder itself launches a short-lived worker through the daemon's own launch path,
// and tracks it. No lead is in the loop. When the worker cannot do its job (no harness, a failed start, it ends
// before it asked) the daemon makes the report itself; only a report not finished by the deadline fails, with why.
//
// The worker is a cheap model and only an orchestrator: its one command is `tmx rec process <id>`, which asks
// this daemon to make the report (transcription, the summary with the model internal/summarize picks, key
// moments) and prints its states. Its profile keeps it on that one job (one allowed command, an empty folder),
// so a cheap model cannot wander off; the work itself is the daemon's.
//
// Only the agent launched for a recording may process it (Process checks the caller).
package recworker

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/summarize"
)

// ProfileSlug is the worker's profile, created (and put back as it should be) before every launch.
const ProfileSlug = "recording-worker"

// Model is the worker's model: the cheapest Haiku. It never sees the recording, only states.
const Model = summarize.DefaultModel

// Fleet is the daemon's launch path (*fleet.Fleet).
type Fleet interface {
	LaunchAgent(fleet.Caller, fleet.LaunchRequest) (model.Agent, error)
	Agent(idOrName string) (model.Agent, error)
	StopAgent(fleet.Caller, string) (model.Agent, error)
	RemoveAgent(fleet.Caller, string) error
	GetProfile(slug string) (model.Profile, error)
	CreateProfile(fleet.Caller, model.Profile) (model.Profile, error)
	UpdateProfile(fleet.Caller, string, model.Profile) (model.Profile, error)
}

// Summaries makes the report (*summarize.Service).
type Summaries interface {
	Finished(model.Recording)
	FinishedHere(r model.Recording, why string) // the daemon makes it: the report says why
	Get(id string) (model.RecordingSummary, error)
	Fail(id, reason string) (bool, error)
	Settings() summarize.Settings
}

// Recordings is the recorder (*recorder.Service).
type Recordings interface {
	Get(id string) (model.Recording, error)
}

// Options configures a Service.
type Options struct {
	Fleet      Fleet
	Summaries  Summaries
	Recordings Recordings
	// Exe is the tmx binary the worker runs ("" = os.Executable()).
	Exe string
	// Dir holds the workers' empty working folders (config home/rec-workers).
	Dir    string
	Logger *slog.Logger
	Now    func() time.Time
	// Every is how often workers are looked at; 0 = 5 s.
	Every time.Duration
	// Deadline is how long a report may take after Stop for a recording of n segments; nil = DefaultDeadline.
	Deadline func(segments int) time.Duration
}

// DefaultDeadline: 20 minutes and 2 more per segment (most of the transcription is done while recording), at most 2 h.
func DefaultDeadline(segments int) time.Duration {
	return min(20*time.Minute+time.Duration(segments)*2*time.Minute, 2*time.Hour)
}

type job struct {
	rec       string
	agent     string // "" once the worker is gone
	name      string
	deadline  time.Time
	budget    time.Duration // deadline - Stop
	triggered bool          // the worker asked for the report (Process)
}

// Service launches and tracks the workers. New, then Start; Close at the end.
type Service struct {
	o    Options
	log  *slog.Logger
	mu   sync.Mutex
	jobs map[string]*job // by recording id
	stop chan struct{}
	wg   sync.WaitGroup
}

// New creates the service.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Every <= 0 {
		o.Every = 5 * time.Second
	}
	if o.Deadline == nil {
		o.Deadline = DefaultDeadline
	}
	if o.Exe == "" {
		o.Exe, _ = os.Executable()
	}
	return &Service{o: o, log: o.Logger, jobs: map[string]*job{}, stop: make(chan struct{})}
}

// Start looks at the workers until Close.
func (s *Service) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(s.o.Every)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.Check()
			}
		}
	}()
}

// Close stops looking; running workers are left to the fleet.
func (s *Service) Close() {
	close(s.stop)
	s.wg.Wait()
}

// Finished is the recorder's hook (recorder.SetFinished): a done recording gets a worker when summaries are
// automatic; otherwise the summary service decides as before (transcription only). It returns at once.
func (s *Service) Finished(r model.Recording) {
	if r.State != model.RecordingDone {
		return
	}
	if !s.o.Summaries.Settings().Auto {
		s.o.Summaries.Finished(r)
		return
	}
	go s.launch(r)
}

// workerName is the worker's agent name: rec-<the recording id's tail>, within model.ValidName.
func workerName(id string) string {
	t := strings.ToLower(strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, id))
	if len(t) > 12 {
		t = t[len(t)-12:]
	}
	return "rec-" + t
}

func (s *Service) launch(r model.Recording) {
	name := workerName(r.ID)
	fail := func(why string) { s.makeHere(r, why) }
	if err := s.ensureProfile(); err != nil {
		fail("the report worker couldn't be set up (" + err.Error() + ")")
		return
	}
	dir := filepath.Join(s.o.Dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail("the report worker has no folder (" + err.Error() + ")")
		return
	}
	s.mu.Lock()
	if _, dup := s.jobs[r.ID]; dup {
		s.mu.Unlock()
		return
	}
	budget := s.o.Deadline(len(r.Segments))
	j := &job{rec: r.ID, name: name, budget: budget, deadline: s.o.Now().Add(budget)}
	s.jobs[r.ID] = j
	s.mu.Unlock()

	cmd := s.command(r.ID)
	a, err := s.o.Fleet.LaunchAgent(fleet.OwnerCaller, fleet.LaunchRequest{
		Name: name, Level: model.LevelWorker, Profile: ProfileSlug, Harness: model.HarnessClaude, Model: Model, Cwd: dir,
		Role:   "makes one recording's report",
		Brief:  "Run this one command and nothing else, then say its last line: " + cmd,
		Prompt: "Run exactly this command with Bash, once, and wait for it to finish: " + cmd + "\nThen answer with its last line only. Do nothing else.",
	})
	if err != nil {
		s.mu.Lock()
		delete(s.jobs, r.ID)
		s.mu.Unlock()
		fail("the report worker couldn't start (" + err.Error() + ")")
		return
	}
	s.mu.Lock()
	j.agent = a.ID
	s.mu.Unlock()
	s.log.Info("recworker: worker launched", "id", r.ID, "agent", name)
}

// command is what the worker runs (also its profile's only allowed command).
func (s *Service) command(id string) string { return quoteExe(s.o.Exe) + " rec process " + id }

func quoteExe(p string) string {
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

// profile is the worker's profile: no files, no web, one command.
func (s *Service) profile() model.Profile {
	return model.Profile{
		Slug: ProfileSlug, Name: "Recording worker", Level: model.LevelWorker,
		Description:   "Launched by the recorder when a recording stops: asks this daemon to make the report, sees states only.",
		Instructions:  "You make one recording's report by running one tmx command. Never read files, never open the web, never run any other command.",
		Model:         Model,
		AllowedModels: []string{Model},
		// dontAsk: whatever is not allowed below is refused, nobody is asked
		PermissionMode:  "dontAsk",
		AllowedTools:    []string{"Bash(" + quoteExe(s.o.Exe) + " rec process:*)"},
		DisallowedTools: []string{"Read", "Glob", "Grep", "Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Task", "Agent"},
		// never ReadOnly: that runs claude in plan mode and denies Bash, so the one command would never run
	}
}

func (s *Service) ensureProfile() error {
	want := s.profile()
	if _, err := s.o.Fleet.GetProfile(ProfileSlug); err != nil {
		_, err = s.o.Fleet.CreateProfile(fleet.OwnerCaller, want)
		return err
	}
	_, err := s.o.Fleet.UpdateProfile(fleet.OwnerCaller, ProfileSlug, want) // put back what someone changed
	return err
}

// Process is POST /api/recordings/{id}/process: the worker asks for the report (the first time) and is told its
// state. Only the agent launched for this recording may call it.
func (s *Service) Process(c fleet.Caller, id string) (model.SummaryState, string, error) {
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || c.AgentID == "" || c.AgentID != j.agent {
		s.mu.Unlock()
		return "", "", &fleet.Error{Kind: fleet.KindForbidden, Msg: "only the worker launched for this recording can process it"}
	}
	first := !j.triggered
	j.triggered = true
	s.mu.Unlock()
	if first {
		r, err := s.o.Recordings.Get(id)
		if err != nil {
			return "", "", err
		}
		s.o.Summaries.Finished(r)
	}
	x, err := s.o.Summaries.Get(id)
	if err != nil {
		return "", "", err
	}
	return x.State, x.Error, nil
}

// Check looks at every worker once (the loop calls it; tests too).
func (s *Service) Check() {
	s.mu.Lock()
	jobs := make([]job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, *j)
	}
	s.mu.Unlock()
	now := s.o.Now()
	for _, j := range jobs {
		s.check(j, now)
	}
}

func (s *Service) check(j job, now time.Time) {
	x, err := s.o.Summaries.Get(j.rec)
	if err != nil { // the recording is gone
		s.done(j, "")
		return
	}
	if j.triggered && !x.State.Pending() && x.State != model.SummaryNone {
		s.done(j, "") // the report is made (or failed on its own, with its own reason)
		return
	}
	if now.After(j.deadline) {
		s.done(j, fmt.Sprintf("the report was not finished in time (%s after Stop); worker %s was stopped", j.budget.Round(time.Minute), j.name))
		return
	}
	if j.agent == "" {
		return // the worker is gone after it asked: the daemon finishes the report, the deadline still holds
	}
	a, err := s.o.Fleet.Agent(j.agent)
	var why string
	switch {
	case err != nil:
		why = "it was removed"
	case a.State == model.StateError:
		why = "it failed"
		if a.Error != "" {
			why += ": " + a.Error
		}
	case a.State == model.StateExited, a.State == model.StateStopped:
		why = "it ended"
	case a.State == model.StateWaiting:
		why = "it asked for a permission or a person"
	case a.State == model.StateIdle && !j.triggered:
		why = "it stopped without running the report"
	default:
		return // starting, queued, working, paused: give it time
	}
	if !j.triggered { // it never asked: the daemon makes the report itself, the deadline still holds
		s.mu.Lock()
		if cur := s.jobs[j.rec]; cur != nil {
			cur.triggered = true
		}
		s.mu.Unlock()
		if r, err := s.o.Recordings.Get(j.rec); err == nil {
			s.makeHere(r, "the report worker "+j.name+" didn't make it ("+why+")")
		}
	}
	s.retire(j) // the daemon finishes the report
}

// makeHere: the worker could not do its job (no harness, a failed start, it ended before it asked). A report is
// never lost to that: the daemon makes it itself, as it would for the worker.
func (s *Service) makeHere(r model.Recording, why string) {
	s.log.Warn("recworker: the daemon makes the report itself", "id", r.ID, "why", why)
	s.o.Summaries.FinishedHere(r, why)
}

// retire stops and removes the worker but keeps tracking the report until it is finished or late.
func (s *Service) retire(j job) {
	s.removeAgent(j.agent)
	s.mu.Lock()
	if cur := s.jobs[j.rec]; cur != nil {
		cur.agent = ""
	}
	s.mu.Unlock()
}

// done ends the job: the worker is stopped and removed, and the report is marked failed with why ("" = it is fine).
func (s *Service) done(j job, why string) {
	s.mu.Lock()
	delete(s.jobs, j.rec)
	s.mu.Unlock()
	if j.agent != "" {
		s.removeAgent(j.agent)
	}
	if why == "" {
		return
	}
	s.log.Warn("recworker: report failed", "id", j.rec, "why", why)
	if _, err := s.o.Summaries.Fail(j.rec, why); err != nil {
		s.log.Warn("recworker: mark failed", "id", j.rec, "err", err)
	}
}

func (s *Service) removeAgent(id string) {
	if _, err := s.o.Fleet.StopAgent(fleet.OwnerCaller, id); err != nil {
		s.log.Debug("recworker: stop worker", "agent", id, "err", err)
	}
	if err := s.o.Fleet.RemoveAgent(fleet.OwnerCaller, id); err != nil {
		s.log.Info("recworker: remove worker", "agent", id, "err", err)
	}
}

// Tracking reports the agent working on a recording's report ("" = none): for the API and tests.
func (s *Service) Tracking(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.jobs[id]; j != nil {
		return j.agent
	}
	return ""
}
