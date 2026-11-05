// Package summarize writes a summary of every finished recording (DESIGN.md "Summaries"). When
// the recorder says a recording is done, it has every segment's WAV transcribed (setting voice
// up first when it is missing), waits until each transcript is done or failed, then runs Claude
// Code once, `claude -p` with the cheapest Haiku, on the transcript and on up to 20 still frames.
//
// Claude gets the Read tool only, confined to the recording's folder: no MCP servers, no
// commands, no edits, a turn limit and a time limit, at low priority, one summary at a time.
// What it reads is other people's words and screens: the prompt frames it as data, never
// instructions, and the only thing taken from its answer is the summary (JSON, parsed
// leniently). Pending summaries live in the store, so a restart resumes them.
package summarize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

// DefaultModel writes summaries unless config summaries.model names another: the cheapest
// Haiku tmx knows.
const DefaultModel = "claude-haiku-4-5-20251001"

// Models are the ones Settings offers (any other name config.json gives works too).
var Models = []string{DefaultModel, "claude-sonnet-5-5", "claude-opus-5-5"}

// Voice is local transcription (*transcribe.Service).
type Voice interface {
	// Enqueue queues a segment's WAV under its source ("recording:<id>/<n>").
	Enqueue(source, wavPath, language string) error
	AutoTranscribe() bool
	Status() transcribe.Status
	// Setup downloads what is missing (engine and model) in the background.
	Setup() (transcribe.Status, error)
}

// Recordings is the recorder (*recorder.Service): a recording with its files' absolute paths.
type Recordings interface {
	Get(id string) (model.Recording, error)
}

// Bus is where the service hears about transcripts, voice and deleted recordings (*fleet.Fleet).
type Bus interface {
	Subscribe() *fleet.Sub
	Unsubscribe(*fleet.Sub)
}

// Options configures a Service.
type Options struct {
	Store      *store.Store
	Recordings Recordings
	Voice      Voice // nil = no transcription on this daemon: summaries come from the frames alone
	Bus        Bus   // nil = the caller hands events to Observe itself
	// Settings returns config.json "summaries"; Save writes it back.
	Settings func() config.SummaryConfig
	Save     func(config.SummaryConfig) error
	// Claude is config claude_command: the claude that runs ("" = found like the agents' one).
	Claude string
	// ClaudeConfigDir is claude's CLAUDE_CONFIG_DIR (config.Paths.ClaudeConfigDir): never the
	// owner's shared ~/.claude.json. Required: a run without it is refused.
	ClaudeConfigDir string
	Publish         func(model.BusEvent)
	Logger          *slog.Logger
	Now             func() time.Time

	// Timeout bounds one claude run; 0 = RunTimeout.
	Timeout time.Duration
	// CheckEvery: how often waiting summaries are looked at without an event; 0 = 10 s.
	CheckEvery time.Duration
	// Peer runs the local model on a fleet peer (summaries.via_peer); nil = no fleet peers.
	Peer Peer

	// NoLowPriority runs claude at normal priority (tests).
	NoLowPriority bool
}

// RunTimeout bounds one claude run (several for a long recording, one per part and one to
// combine them).
const RunTimeout = 6 * time.Minute

// Service makes the summaries. New, then Start; Close at the end.
type Service struct {
	o   Options
	log *slog.Logger

	mu        sync.Mutex
	running   string          // the recording whose summary Claude writes now ("" = none)
	again     map[string]bool // a new transcription began while its summary ran: once more after
	voiceWait map[string]bool // recordings to transcribe once voice is set up (no summary wanted)
	setupAt   time.Time       // when voice was last set up for a recording (zero: not since the last new one)
	dropped   map[string]bool // optional claude flags this claude does not know

	peerSlot           chan struct{}     // ServePeer: one peer generate at a time
	wroteBy            string            // the model that wrote the latest answer (wrote)
	madeHere           map[string]string // FinishedHere: why the daemon made the report, not its worker
	wakeWatch, wakeRun chan struct{}
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
}

// New creates the service.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Publish == nil {
		o.Publish = func(model.BusEvent) {}
	}
	if o.Settings == nil {
		o.Settings = func() config.SummaryConfig { return config.SummaryConfig{} }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Timeout <= 0 {
		o.Timeout = RunTimeout
	}
	if o.CheckEvery <= 0 {
		o.CheckEvery = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{o: o, log: o.Logger, again: map[string]bool{}, voiceWait: map[string]bool{}, dropped: map[string]bool{}, madeHere: map[string]string{},
		wakeWatch: make(chan struct{}, 1), wakeRun: make(chan struct{}, 1), peerSlot: make(chan struct{}, 1), ctx: ctx, cancel: cancel}
}

// Start forgets the summaries of deleted recordings, queues again what a restart cut off, and
// starts the watcher (transcripts) and the runner (Claude).
func (s *Service) Start() error {
	if n, err := s.o.Store.DeleteOrphanSummaries(); err != nil {
		return err
	} else if n > 0 {
		s.log.Info("summaries: removed those of deleted recordings", "n", n)
	}
	cut, err := s.o.Store.SummariesIn(model.SummaryRunning)
	if err != nil {
		return err
	}
	for _, x := range cut {
		x.State, x.UpdatedAt = model.SummaryQueued, s.now()
		if err := s.o.Store.PutSummary(x); err != nil {
			return err
		}
	}
	if s.o.Bus != nil {
		sub := s.o.Bus.Subscribe()
		s.wg.Add(1)
		go s.listen(sub)
	}
	s.wg.Add(2)
	go s.watch()
	go s.runner()
	s.kick()
	return nil
}

// Close stops the service; a summary Claude is writing is cut off and runs again at the next start.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) listen(sub *fleet.Sub) {
	defer s.wg.Done()
	defer s.o.Bus.Unsubscribe(sub)
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev := <-sub.C():
			s.Observe(ev)
			if sub.TakeResync() { // events were dropped: look at everything again
				s.kick()
			}
		}
	}
}

func (s *Service) now() int64 { return s.o.Now().UnixMilli() }

// kick wakes the watcher and the runner.
func (s *Service) kick() {
	s.kickWatch()
	s.kickRun()
}

func (s *Service) kickWatch() {
	select {
	case s.wakeWatch <- struct{}{}:
	default:
	}
}

func (s *Service) kickRun() {
	select {
	case s.wakeRun <- struct{}{}:
	default:
	}
}

// ---- settings

// Settings are the summary settings as they apply (GET /api/summaries/settings).
type Settings struct {
	Auto         bool     `json:"auto"`  // every finished recording is summarized
	Model        string   `json:"model"` // the model that writes them
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models"` // suggestions
	// Claude is the claude that runs; ClaudeError says why there is none.
	Claude      string `json:"claude,omitempty"`
	ClaudeError string `json:"claude_error,omitempty"`
	// Voice: transcription exists on this daemon; AutoTranscribe is its setting (PUT /api/voice).
	Voice          bool   `json:"voice"`
	AutoTranscribe bool   `json:"auto_transcribe"`
	Running        string `json:"running,omitempty"` // the recording summarized now
}

func (s *Service) settings() config.SummaryConfig { return s.o.Settings() }

// model is the Claude model summaries use now.
func (s *Service) model() string {
	if m := strings.TrimSpace(s.settings().Model); m != "" {
		return m
	}
	return DefaultModel
}

// Settings says how summaries are made here.
func (s *Service) Settings() Settings {
	c := s.settings()
	out := Settings{Auto: c.AutoOn(), Model: s.model(), DefaultModel: DefaultModel, Models: Models, Voice: s.o.Voice != nil}
	if p, err := claude.LookPath(runner.Spec{Command: s.o.Claude}); err == nil {
		out.Claude = p
	} else {
		out.ClaudeError = err.Error()
	}
	if s.o.Voice != nil {
		out.AutoTranscribe = s.o.Voice.AutoTranscribe()
	}
	s.mu.Lock()
	out.Running = s.running
	s.mu.Unlock()
	return out
}

// SettingsPatch is PUT /api/summaries/settings; nil fields stay. Model "" = the default.
type SettingsPatch struct {
	Auto  *bool   `json:"auto"`
	Model *string `json:"model"`
}

// modelRx: a model name or alias as claude --model takes it (claude-haiku-4-5-20251001, haiku,
// claude-sonnet-5-5[1m]).
var modelRx = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,99}$`)

// Configure changes the settings (config.json "summaries").
func (s *Service) Configure(p SettingsPatch) (Settings, error) {
	c := s.settings()
	if p.Auto != nil {
		v := *p.Auto
		c.Auto = &v
	}
	if p.Model != nil {
		m := strings.TrimSpace(*p.Model)
		if m != "" && !modelRx.MatchString(m) {
			return s.Settings(), &fleet.Error{Kind: fleet.KindInvalid, Msg: "the model is a Claude model name such as " + DefaultModel}
		}
		if m == DefaultModel {
			m = ""
		}
		c.Model = m
	}
	if s.o.Save != nil {
		if err := s.o.Save(c); err != nil {
			return s.Settings(), err
		}
	}
	return s.Settings(), nil
}

// ---- requests

// Finished is the recorder's hook: a recording is done. It queues the transcription (when that is
// automatic) and the summary (when that is), and returns at once.
func (s *Service) Finished(r model.Recording) {
	if r.State != model.RecordingDone {
		return // live, interrupted or failed recordings are left alone (Summarize does them by hand)
	}
	auto := s.settings().AutoOn()
	autoTr := s.o.Voice != nil && s.o.Voice.AutoTranscribe()
	if !auto && !autoTr {
		return
	}
	s.mu.Lock()
	s.setupAt = time.Time{} // a new recording may try setting voice up again
	if auto {
		if _, err := s.requestLocked(r.ID, false); err != nil {
			s.log.Warn("summaries: queue", "id", r.ID, "err", err)
		}
	} else {
		s.voiceWait[r.ID] = true
	}
	s.mu.Unlock()
	s.kick()
}

// FinishedHere is Finished when the daemon makes the report itself because its worker could not (recworker):
// the report says so ("Made by the daemon: <why>.").
func (s *Service) FinishedHere(r model.Recording, why string) {
	s.mu.Lock()
	s.madeHere[r.ID] = why
	s.mu.Unlock()
	s.Finished(r)
}

// Summarize (re)makes a recording's summary on the owner's request: it transcribes what is not
// transcribed yet (setting voice up if needed), then has Claude write it again. Any finished
// recording can be summarized, an interrupted one too.
func (s *Service) Summarize(id string) (model.RecordingSummary, error) {
	r, err := s.o.Recordings.Get(id)
	if err != nil {
		return model.RecordingSummary{}, err
	}
	if r.State.Live() {
		return model.RecordingSummary{}, &fleet.Error{Kind: fleet.KindConflict, Msg: "the recording is still running; stop it first"}
	}
	s.mu.Lock()
	if s.running == id {
		s.mu.Unlock()
		return model.RecordingSummary{}, &fleet.Error{Kind: fleet.KindConflict, Msg: "Claude is writing this summary now; wait for it"}
	}
	s.setupAt = time.Time{}
	x, err := s.requestLocked(id, true)
	s.mu.Unlock()
	if err != nil {
		return x, err
	}
	s.kick()
	return x, nil
}

// requestLocked puts a recording's summary in line: it waits for the transcripts first. The
// previous summary's text stays until the new one replaces it.
func (s *Service) requestLocked(id string, requested bool) (model.RecordingSummary, error) {
	now := s.now()
	x, err := s.o.Store.GetSummary(id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		x = model.RecordingSummary{RecordingID: id, CreatedAt: now}
	case err != nil:
		return x, err
	}
	x.State, x.Error, x.Note, x.Requested, x.UpdatedAt = model.SummaryTranscribing, "", "", requested, now
	return x, s.saveLocked(x)
}

// saveLocked stores and announces a summary.
func (s *Service) saveLocked(x model.RecordingSummary) error {
	store.FillSummaryLists(&x)
	if err := s.o.Store.PutSummary(x); err != nil {
		return err
	}
	s.o.Publish(model.BusEvent{Type: model.EvSummaryUpdated, Data: x, At: model.Now()})
	return nil
}

// Observe takes the bus events that matter here: transcripts (a recording's transcription moved
// on, or began again), voice (it may be set up now) and deleted recordings.
func (s *Service) Observe(ev model.BusEvent) {
	switch ev.Type {
	case model.EvTranscriptUpdated:
		t, ok := ev.Data.(model.Transcript)
		if !ok {
			return
		}
		id, _, ok := ParseSource(t.Source)
		if !ok {
			return
		}
		if t.State == model.TranscriptQueued {
			s.retranscribed(id)
		}
		s.kickWatch()
	case model.EvVoiceStatus:
		s.kickWatch()
	case model.EvRecordingRemoved:
		var id string
		switch d := ev.Data.(type) {
		case map[string]string:
			id = d["id"]
		case map[string]any:
			id, _ = d["id"].(string)
		}
		if id == "" {
			return
		}
		s.mu.Lock()
		delete(s.voiceWait, id)
		delete(s.again, id)
		s.mu.Unlock()
		if err := s.o.Store.DeleteSummary(id); err != nil {
			s.log.Warn("summaries: delete", "id", id, "err", err)
		}
	}
}

// retranscribed: a segment of recording id was queued for transcription. A summary that is done
// is made again once the new transcripts are; with automatic summaries a finished recording that
// has none gets one.
func (s *Service) retranscribed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == id {
		s.again[id] = true
		return
	}
	x, err := s.o.Store.GetSummary(id)
	switch {
	case err == nil:
		if x.State == model.SummaryTranscribing {
			return
		}
		x.State, x.Error, x.UpdatedAt = model.SummaryTranscribing, "", s.now()
		if err := s.saveLocked(x); err != nil {
			s.log.Warn("summaries: save", "id", id, "err", err)
		}
	case errors.Is(err, store.ErrNotFound) && s.settings().AutoOn():
		if r, err := s.o.Recordings.Get(id); err == nil && r.State == model.RecordingDone {
			if _, err := s.requestLocked(id, false); err != nil {
				s.log.Warn("summaries: queue", "id", id, "err", err)
			}
		}
	}
}

// ---- reading

// Get returns a recording's summary; State "none" when it has none.
func (s *Service) Get(id string) (model.RecordingSummary, error) {
	if _, err := s.o.Recordings.Get(id); err != nil {
		return model.RecordingSummary{}, err
	}
	x, err := s.o.Store.GetSummary(id)
	if errors.Is(err, store.ErrNotFound) {
		x = model.RecordingSummary{RecordingID: id, State: model.SummaryNone}
		store.FillSummaryLists(&x)
		return x, nil
	}
	return x, err
}

// Fail marks a recording's report failed with reason (internal/recworker: its worker failed or ran out of
// time). A summary being written right now, or already finished, is left alone: that outcome is the truth.
// It reports whether the summary was marked.
func (s *Service) Fail(id, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == id {
		return false, nil
	}
	x, err := s.o.Store.GetSummary(id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		x = model.RecordingSummary{RecordingID: id, CreatedAt: s.now()}
	case err != nil:
		return false, err
	case !x.State.Pending():
		return false, nil
	}
	delete(s.voiceWait, id)
	x.State, x.Error, x.Note, x.UpdatedAt = model.SummaryFailed, reason, "", s.now()
	return true, s.saveLocked(x)
}

// Attach fills each recording's Summary and SummaryState (lists and reads of recordings).
func (s *Service) Attach(list []model.Recording) {
	if len(list) == 0 {
		return
	}
	ids := make([]string, len(list))
	for i, r := range list {
		ids[i] = r.ID
	}
	briefs, err := s.o.Store.SummaryBriefs(ids)
	if err != nil {
		s.log.Warn("summaries: read", "err", err)
		return
	}
	for i := range list {
		if b, ok := briefs[list[i].ID]; ok {
			list[i].Summary, list[i].SummaryState = b.Summary, b.State
		}
	}
}

// ParseSource splits "recording:<id>/<n>".
func ParseSource(src string) (id string, n int, ok bool) {
	rest, ok := strings.CutPrefix(src, "recording:")
	if !ok {
		return "", 0, false
	}
	id, num, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		return "", 0, false
	}
	if _, err := fmt.Sscanf(num, "%d", &n); err != nil || n < 1 {
		return "", 0, false
	}
	return id, n, true
}
