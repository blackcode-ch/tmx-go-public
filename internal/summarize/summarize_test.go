package summarize

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

// ---- a fake voice service: transcripts appear in the store like the real one makes them

type fakeVoice struct {
	mu        sync.Mutex
	st        *store.Store
	publish   func(model.BusEvent)
	ready     bool
	busy      bool
	auto      bool
	available bool
	setupFail string // the engine's error after a setup
	engineErr string
	setups    int
	hold      bool // jobs wait for release
	held      []model.Transcript
	jobs      []string
	failSeg   map[int]bool
	speech    bool // false: every transcript is silence
}

func newFakeVoice(st *store.Store, publish func(model.BusEvent)) *fakeVoice {
	return &fakeVoice{st: st, publish: publish, ready: true, auto: true, available: true, failSeg: map[int]bool{}, speech: true}
}

func (f *fakeVoice) AutoTranscribe() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.auto }

func (f *fakeVoice) Status() transcribe.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := transcribe.Status{Ready: f.ready, Busy: f.busy, Engine: transcribe.EngineStatus{Available: f.available, Error: f.engineErr}}
	if !f.available {
		st.Reason = "Voice transcription is not available on this platform (plan9/386)."
	} else if !f.ready {
		st.Reason = "The voice engine is not downloaded yet."
	}
	return st
}

func (f *fakeVoice) Setup() (transcribe.Status, error) {
	f.mu.Lock()
	f.setups++
	if !f.available {
		f.mu.Unlock()
		return f.Status(), errors.New("not available here")
	}
	f.busy, f.engineErr = true, ""
	f.mu.Unlock()
	go func() {
		time.Sleep(40 * time.Millisecond)
		f.mu.Lock()
		f.busy = false
		if f.setupFail != "" {
			f.engineErr = f.setupFail
		} else {
			f.ready = true
		}
		f.mu.Unlock()
		f.publish(model.BusEvent{Type: model.EvVoiceStatus})
	}()
	return f.Status(), nil
}

func (f *fakeVoice) Enqueue(source, wav, lang string) error {
	f.mu.Lock()
	if !f.ready {
		f.mu.Unlock()
		return errors.New("voice transcription isn't set up")
	}
	f.jobs = append(f.jobs, source)
	hold := f.hold
	f.mu.Unlock()
	if _, err := os.Stat(wav); err != nil {
		return err
	}
	t, err := f.st.TranscriptBySource(source)
	if errors.Is(err, store.ErrNotFound) {
		t = model.Transcript{ID: model.NewID(), Source: source, CreatedAt: model.Now()}
	}
	t.State, t.Text, t.Segments, t.UpdatedAt = model.TranscriptQueued, "", nil, model.Now()
	if err := f.st.PutTranscript(t, wav); err != nil {
		return err
	}
	f.publish(model.BusEvent{Type: model.EvTranscriptUpdated, Data: t})
	if hold {
		f.mu.Lock()
		f.held = append(f.held, t)
		f.mu.Unlock()
		return nil
	}
	go func() { time.Sleep(15 * time.Millisecond); f.finish(t) }()
	return nil
}

func (f *fakeVoice) finish(t model.Transcript) {
	_, n, _ := ParseSource(t.Source)
	f.mu.Lock()
	failed, speech := f.failSeg[n], f.speech
	f.mu.Unlock()
	t.UpdatedAt = model.Now()
	switch {
	case failed:
		t.State, t.Error = model.TranscriptFailed, "the voice engine failed"
	case !speech:
		t.State, t.Language, t.Segments = model.TranscriptDone, "en", []model.TranscriptSegment{}
	default:
		t.State, t.Language = model.TranscriptDone, "en"
		t.Segments = []model.TranscriptSegment{{FromMS: 0, ToMS: 1200, Text: fmt.Sprintf(" Words of segment %d.", n)},
			{FromMS: 1500, ToMS: 2600, Text: "And a second line."}}
		t.Text = "Words."
	}
	_ = f.st.PutTranscript(t, "")
	f.publish(model.BusEvent{Type: model.EvTranscriptUpdated, Data: t})
}

func (f *fakeVoice) release() {
	f.mu.Lock()
	held := f.held
	f.held, f.hold = nil, false
	f.mu.Unlock()
	for _, t := range held {
		f.finish(t)
	}
}

func (f *fakeVoice) set(fn func(*fakeVoice)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

// ---- the test environment: a real recorder and store, the fake voice, the fake claude

type env struct {
	t     *testing.T
	home  string
	st    *store.Store
	rec   *recorder.Service
	voice *fakeVoice
	s     *Service
	log   string

	mu   sync.Mutex
	cfg  config.SummaryConfig
	evs  []model.BusEvent
	bus  chan model.BusEvent
	opts func(*Options)
}

func newEnv(t *testing.T, mode string, opts ...func(*Options)) *env {
	t.Helper()
	return newEnvStale(t, mode, 0, opts...)
}

// newEnvStale: the recorder marks a recording interrupted after stale without data (0 = as usual).
func newEnvStale(t *testing.T, mode string, stale time.Duration, opts ...func(*Options)) *env {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, home: home, st: st, log: filepath.Join(home, "claude.log"), bus: make(chan model.BusEvent, 1024)}
	t.Setenv("TMXG_FAKE_CLAUDE", mode)
	t.Setenv("TMXG_FAKE_CLAUDE_LOG", e.log)
	t.Setenv("TMXG_FAKE_CLAUDE_ANSWER", "")
	e.voice = newFakeVoice(st, e.publish)
	e.rec = recorder.New(recorder.Options{Store: st, Dir: filepath.Join(home, "recordings"), Publish: e.publish,
		DiskFree: func(string) (uint64, error) { return 1 << 40, nil }, StaleAfter: stale, OutageGrace: stale})
	if err := e.rec.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.rec.Close)
	done := make(chan struct{})
	go func() { // the bus: events reach the service in order, from another goroutine
		for {
			select {
			case ev := <-e.bus:
				e.mu.Lock()
				s := e.s
				e.mu.Unlock()
				if s != nil {
					s.Observe(ev)
				}
			case <-done:
				return
			}
		}
	}()
	t.Cleanup(func() { close(done) })
	if len(opts) > 0 {
		e.opts = opts[0]
	}
	e.start()
	return e
}

func (e *env) options() Options {
	o := Options{Store: e.st, Recordings: e.rec, Voice: e.voice, Publish: e.publish, Claude: os.Args[0], NoLowPriority: true,
		CheckEvery: 20 * time.Millisecond, Timeout: 20 * time.Second, ClaudeConfigDir: filepath.Join(e.home, "claude-config"),
		Settings: func() config.SummaryConfig {
			e.mu.Lock()
			defer e.mu.Unlock()
			c := e.cfg
			if c.Backend == "" { // these tests drive the Claude path with the fake; local_test.go the local one
				c.Backend = "claude"
			}
			return c
		},
		Save: func(c config.SummaryConfig) error { e.mu.Lock(); defer e.mu.Unlock(); e.cfg = c; return nil }}
	if e.opts != nil {
		e.opts(&o)
	}
	return o
}

func (e *env) start() {
	s := New(e.options())
	e.mu.Lock()
	e.s = s
	e.mu.Unlock()
	e.rec.SetFinished(s.Finished)
	if err := s.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(s.Close)
}

// restart is a daemon restart: a new service on the same store.
func (e *env) restart() {
	e.s.Close()
	e.start()
}

func (e *env) publish(ev model.BusEvent) {
	e.mu.Lock()
	e.evs = append(e.evs, ev)
	e.mu.Unlock()
	e.bus <- ev
}

func (e *env) setAuto(on bool) { e.mu.Lock(); e.cfg.Auto = &on; e.mu.Unlock() }

func (e *env) config() config.SummaryConfig { e.mu.Lock(); defer e.mu.Unlock(); return e.cfg }

func (e *env) jobs() []string {
	e.voice.mu.Lock()
	defer e.voice.mu.Unlock()
	return append([]string(nil), e.voice.jobs...)
}

func (e *env) setups() int { e.voice.mu.Lock(); defer e.voice.mu.Unlock(); return e.voice.setups }

// record makes a finished-able recording: segs segments of secs seconds each (with the
// microphone's WAV when mic), not stopped yet.
func (e *env) record(kind model.RecordingKind, mic bool, segs, secs int) model.Recording {
	e.t.Helper()
	mime := "video/webm"
	if kind == model.RecordingAudio {
		mime = "audio/webm"
	}
	r, err := e.rec.Begin(recorder.BeginRequest{Kind: kind, Mic: mic, Mime: mime, Title: "Checkout bug repro"})
	if err != nil {
		e.t.Fatal(err)
	}
	for n := 1; n <= segs; n++ {
		if _, err := e.rec.Append(r.ID, n, recorder.TrackMedia, 0, int64(secs*1000), []byte("media bytes")); err != nil {
			e.t.Fatal(err)
		}
		if mic {
			if _, err := e.rec.Append(r.ID, n, recorder.TrackPCM, 0, 0, make([]byte, secs*32000)); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	return r
}

func (e *env) stop(id string) model.Recording {
	e.t.Helper()
	r, err := e.rec.SetState(id, "stopped")
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// frames writes still frames at the given offsets.
func (e *env) frames(r model.Recording, ms ...int64) {
	e.t.Helper()
	d := filepath.Join(e.home, "recordings", r.ID, FramesDir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		e.t.Fatal(err)
	}
	for _, m := range ms {
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("f-%09d.jpg", m)), []byte("\xff\xd8\xff jpeg"), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
}

// wait waits until the summary of id is in one of states.
func (e *env) wait(id string, states ...model.SummaryState) model.RecordingSummary {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var x model.RecordingSummary
	for time.Now().Before(deadline) {
		var err error
		x, err = e.st.GetSummary(id)
		if err == nil {
			for _, s := range states {
				if x.State == s {
					return x
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("summary of %s is %q (%s), not %v", id, x.State, x.Error, states)
	return x
}

// runs are the fake claude's runs so far.
func (e *env) runs() []fakeRun {
	b, err := os.ReadFile(e.log)
	if err != nil {
		return nil
	}
	var out []fakeRun
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r fakeRun
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// states are the summary.updated states of id, in order, without repeats.
func (e *env) states(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.evs {
		if x, ok := ev.Data.(model.RecordingSummary); ok && ev.Type == model.EvSummaryUpdated && x.RecordingID == id {
			if len(out) == 0 || out[len(out)-1] != string(x.State) {
				out = append(out, string(x.State))
			}
		}
	}
	return strings.Join(out, ",")
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// ---- tests

func TestFinishedTranscribesThenSummarizes(t *testing.T) {
	e := newEnv(t, "frames")
	r := e.record(model.RecordingScreen, true, 2, 3)
	e.frames(r, 0, 3000, 5900)
	e.stop(r.ID)
	x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed)
	if x.State != model.SummaryDone {
		t.Fatalf("summary: %+v", x)
	}
	if !strings.HasPrefix(x.Summary, "The owner shows a checkout bug.") || x.Model != DefaultModel || x.Transcripts != 2 || x.Frames != 3 || x.Parts != 1 {
		t.Fatalf("summary: %+v", x)
	}
	if len(x.KeyPoints) != 2 || x.Decisions[0] != "Write a test before fixing it" || x.ActionItems[0].Who != "the lead" || x.ActionItems[1].Who != "" {
		t.Fatalf("lists: %+v", x)
	}
	// chapter at 0:03 is the start of segment 2 (3 s each)
	if len(x.Chapters) != 2 || x.Chapters[1].TMS != 3000 || x.Chapters[1].Segment != 2 || x.Chapters[1].OffsetMS != 0 || x.Chapters[0].Segment != 1 {
		t.Fatalf("chapters: %+v", x.Chapters)
	}
	if got := e.states(r.ID); got != "transcribing,queued,running,done" {
		t.Fatalf("states: %s", got)
	}
	if got := strings.Join(e.jobs(), ","); got != recorder.Source(r.ID, 1)+","+recorder.Source(r.ID, 2) {
		t.Fatalf("jobs: %s", got)
	}
	runs := e.runs()
	if len(runs) != 1 {
		t.Fatalf("%d claude runs", len(runs))
	}
	run := runs[0]
	if real, _ := filepath.EvalSymlinks(r.Dir); run.Cwd != r.Dir && run.Cwd != real {
		t.Fatalf("cwd %s, not the recording's folder %s", run.Cwd, r.Dir)
	}
	if argValue(run.Args, "--tools") != "Read" || argValue(run.Args, "--max-turns") != "6" || argValue(run.Args, "--model") != DefaultModel {
		t.Fatalf("args: %q", run.Args)
	}
	for _, want := range []string{`Recording: "Checkout bug repro"`, "Kind: a recording of the screen, with the microphone", "Length: 0:06 (2 segments)",
		`<transcript language="en">`, "[0:00] Words of segment 1.", "[0:01] And a second line.", "[0:03] Words of segment 2.", "</transcript>",
		"- 0:00 frames/f-000000000.jpg", "- 0:03 frames/f-000003000.jpg", "- 0:05 frames/f-000005900.jpg"} {
		if !strings.Contains(run.Stdin, want) {
			t.Fatalf("the prompt lacks %q:\n%s", want, run.Stdin)
		}
	}
	// the recording carries it
	list := []model.Recording{r}
	e.s.Attach(list)
	if list[0].SummaryState != model.SummaryDone || list[0].Summary != x.Summary {
		t.Fatalf("attach: %+v", list[0])
	}
}

func TestNoAudioSummarizesAtOnceOrSkips(t *testing.T) {
	e := newEnv(t, "ok")
	r := e.record(model.RecordingWindow, false, 1, 4)
	e.frames(r, 0, 2000)
	e.stop(r.ID)
	x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed)
	if x.State != model.SummaryDone || x.Frames != 2 || x.Transcripts != 0 || len(e.jobs()) != 0 {
		t.Fatalf("frames only: %+v %v", x, e.jobs())
	}
	if run := e.runs()[0]; !strings.Contains(run.Stdin, "Transcript: none") || argValue(run.Args, "--max-turns") != "5" {
		t.Fatalf("prompt: %s %q", run.Stdin, run.Args)
	}
	// nothing at all: no claude run
	r2 := e.record(model.RecordingWindow, false, 1, 4)
	e.stop(r2.ID)
	x = e.wait(r2.ID, model.SummarySkipped, model.SummaryDone, model.SummaryFailed)
	if x.State != model.SummarySkipped || !strings.Contains(x.Error, "no sound to transcribe and no still frames") || len(e.runs()) != 1 {
		t.Fatalf("skipped: %+v (%d runs)", x, len(e.runs()))
	}
	// silence and no frames: skipped too
	e.voice.set(func(f *fakeVoice) { f.speech = false })
	r3 := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r3.ID)
	x = e.wait(r3.ID, model.SummarySkipped, model.SummaryDone, model.SummaryFailed)
	if x.State != model.SummarySkipped || !strings.Contains(x.Error, "no speech was heard") {
		t.Fatalf("silence: %+v", x)
	}
	// silence with frames: the frames make the summary; no tools without frames is covered above
	e.voice.set(func(f *fakeVoice) { f.speech = true })
}

func TestAutoSetupWhenVoiceIsMissing(t *testing.T) {
	e := newEnv(t, "ok")
	e.voice.set(func(f *fakeVoice) { f.ready = false })
	r := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r.ID)
	x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed, model.SummarySkipped)
	if x.State != model.SummaryDone || x.Transcripts != 1 || x.Note != "" {
		t.Fatalf("summary: %+v", x)
	}
	if e.setups() != 1 || len(e.jobs()) != 1 {
		t.Fatalf("setups %d, jobs %v", e.setups(), e.jobs())
	}
	saw := false
	e.mu.Lock()
	for _, ev := range e.evs {
		if s, ok := ev.Data.(model.RecordingSummary); ok && s.Note == SettingUpNote {
			saw = true
		}
	}
	e.mu.Unlock()
	if !saw {
		t.Fatal("no summary said voice is being set up")
	}
}

func TestSetupFailsSummaryGoesOn(t *testing.T) {
	e := newEnv(t, "ok")
	e.voice.set(func(f *fakeVoice) {
		f.ready, f.setupFail = false, "the private release repository needs a GitHub token"
	})
	r := e.record(model.RecordingScreen, true, 1, 2)
	e.frames(r, 0)
	e.stop(r.ID)
	x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed, model.SummarySkipped)
	if x.State != model.SummaryDone || x.Frames != 1 || x.Transcripts != 0 ||
		!strings.Contains(x.Note, "voice could not be set up (the private release repository needs a GitHub token)") {
		t.Fatalf("summary: %+v", x)
	}
	if e.setups() != 1 {
		t.Fatalf("setups: %d", e.setups())
	}
	// no frames either: skipped, saying so
	r2 := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r2.ID) // a new recording tries the setup once more
	x = e.wait(r2.ID, model.SummaryDone, model.SummaryFailed, model.SummarySkipped)
	if x.State != model.SummarySkipped || !strings.Contains(x.Error, "isn't transcribed") || !strings.Contains(x.Note, "could not be set up") || e.setups() != 2 {
		t.Fatalf("skipped: %+v (setups %d)", x, e.setups())
	}
	// a platform without the engine: no setup at all
	e.voice.set(func(f *fakeVoice) { f.available = false })
	r3 := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r3.ID)
	x = e.wait(r3.ID, model.SummarySkipped, model.SummaryDone, model.SummaryFailed)
	if !strings.Contains(x.Note, "not available on this platform") || e.setups() != 2 {
		t.Fatalf("unavailable: %+v (setups %d)", x, e.setups())
	}
}

func TestAutoSettings(t *testing.T) {
	e := newEnv(t, "ok")
	// summaries off, transcription on: transcribed, no summary
	e.setAuto(false)
	r := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r.ID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if t, err := e.st.TranscriptBySource(recorder.Source(r.ID, 1)); err == nil && t.State == model.TranscriptDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not transcribed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.st.GetSummary(r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a summary without auto: %v", err)
	}
	// both off: nothing
	e.voice.set(func(f *fakeVoice) { f.auto = false })
	r2 := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r2.ID)
	time.Sleep(150 * time.Millisecond)
	if len(e.jobs()) != 1 {
		t.Fatalf("jobs: %v", e.jobs())
	}
	// summaries on, transcription off: the summary says it is not transcribed (no frames: skipped)
	e.setAuto(true)
	r3 := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r3.ID)
	x := e.wait(r3.ID, model.SummarySkipped, model.SummaryDone, model.SummaryFailed)
	if x.State != model.SummarySkipped || !strings.Contains(x.Note, "transcription is not automatic") || len(e.jobs()) != 1 {
		t.Fatalf("summary: %+v %v", x, e.jobs())
	}
	// asked by hand: transcribed anyway, then summarized
	if _, err := e.s.Summarize(r3.ID); err != nil {
		t.Fatal(err)
	}
	x = e.wait(r3.ID, model.SummaryDone, model.SummaryFailed)
	if x.State != model.SummaryDone || x.Transcripts != 1 || !x.Requested {
		t.Fatalf("by hand: %+v", x)
	}
	// settings round trip
	st, err := e.s.Configure(SettingsPatch{Model: ptr("claude-sonnet-5-5")})
	if err != nil || st.Model != "claude-sonnet-5-5" || !st.Auto || e.config().Model != "claude-sonnet-5-5" {
		t.Fatalf("configure: %+v %v", st, err)
	}
	if _, err := e.s.Configure(SettingsPatch{Model: ptr("rm -rf /")}); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("bad model: %v", err)
	}
	if st, _ := e.s.Configure(SettingsPatch{Model: ptr(""), Auto: ptr(false)}); st.Model != DefaultModel || st.Auto || e.config().Model != "" {
		t.Fatalf("default: %+v", st)
	}
}

func ptr[T any](v T) *T { return &v }

func TestInterruptedLeftAloneSummarizedByHand(t *testing.T) {
	e := newEnvStale(t, "ok", 30*time.Millisecond)
	r := e.record(model.RecordingAudio, true, 1, 2)
	// a live recording cannot be summarized
	if _, err := e.s.Summarize(r.ID); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("live: %v", err)
	}
	// the sweep marks it interrupted: left alone
	time.Sleep(50 * time.Millisecond)
	e.rec.Sweep()
	if got, _ := e.rec.Get(r.ID); got.State != model.RecordingInterrupted {
		t.Fatalf("state %s", got.State)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := e.st.GetSummary(r.ID); !errors.Is(err, store.ErrNotFound) || len(e.jobs()) != 0 {
		t.Fatalf("an interrupted recording was summarized: %v %v", err, e.jobs())
	}
	// by hand it is
	if _, err := e.s.Summarize(r.ID); err != nil {
		t.Fatal(err)
	}
	if x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed); x.State != model.SummaryDone || x.Transcripts != 1 {
		t.Fatalf("by hand: %+v", x)
	}
	if _, err := e.s.Summarize("nope"); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("unknown: %v", err)
	}
}

func TestRestartResumes(t *testing.T) {
	e := newEnv(t, "ok")
	e.voice.set(func(f *fakeVoice) { f.hold = true })
	r := e.record(model.RecordingAudio, true, 2, 2)
	e.stop(r.ID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.voice.mu.Lock()
		n := len(e.voice.held)
		e.voice.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if x := e.wait(r.ID, model.SummaryTranscribing); x.State != model.SummaryTranscribing {
		t.Fatal(x.State)
	}
	// the daemon stops while the transcripts run; they finish meanwhile (the voice service's own restart)
	e.s.Close()
	e.voice.release()
	e.start()
	x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed)
	if x.State != model.SummaryDone || x.Transcripts != 2 {
		t.Fatalf("after restart: %+v", x)
	}
	if len(e.jobs()) != 2 {
		t.Fatalf("transcribed again: %v", e.jobs())
	}
	// a summary cut off while running is queued again at the start
	x.State = model.SummaryRunning
	if err := e.st.PutSummary(x); err != nil {
		t.Fatal(err)
	}
	before := len(e.runs())
	e.restart()
	if x = e.wait(r.ID, model.SummaryDone); len(e.runs()) != before+1 {
		t.Fatalf("not run again: %d runs", len(e.runs()))
	}
	// a summary whose recording is gone is dropped at the start
	orphan := model.RecordingSummary{RecordingID: "gone", State: model.SummaryQueued, CreatedAt: 1, UpdatedAt: 1}
	if err := e.st.PutSummary(orphan); err != nil {
		t.Fatal(err)
	}
	e.restart()
	if _, err := e.st.GetSummary("gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("orphan: %v", err)
	}
}

func TestRegenerateAndRetranscribe(t *testing.T) {
	e := newEnv(t, "ok")
	r := e.record(model.RecordingAudio, true, 1, 2)
	r = e.stop(r.ID)
	e.wait(r.ID, model.SummaryDone)
	// Regenerate: no new transcription (it is done), a new claude run
	t.Setenv("TMXG_FAKE_CLAUDE_ANSWER", `{"summary": "A second, better summary of the bug.", "key_points": [], "decisions": [], "action_items": [], "chapters": []}`)
	x, err := e.s.Summarize(r.ID)
	if err != nil || x.State != model.SummaryTranscribing || !strings.HasPrefix(x.Summary, "The owner shows") {
		t.Fatalf("regenerate: %+v %v", x, err) // the old text stays until the new one is there
	}
	x = e.wait(r.ID, model.SummaryDone)
	if x.Summary != "A second, better summary of the bug." || len(e.runs()) != 2 || len(e.jobs()) != 1 || len(x.Chapters) != 0 {
		t.Fatalf("runs %d jobs %v %+v", len(e.runs()), e.jobs(), x)
	}
	// the owner transcribes again (Transcribe on the recording's page): the summary follows
	t.Setenv("TMXG_FAKE_CLAUDE_ANSWER", "")
	if err := e.voice.Enqueue(recorder.Source(r.ID, 1), r.Segments[0].WAV, "de"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(e.runs()) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	x = e.wait(r.ID, model.SummaryDone)
	if len(e.runs()) != 3 || !strings.HasPrefix(x.Summary, "The owner shows") {
		t.Fatalf("after re-transcription: %d runs, %+v", len(e.runs()), x)
	}
}

func TestRegenerateWhileRunningIsRefused(t *testing.T) {
	e := newEnv(t, "sleep", func(o *Options) { o.Timeout = 3 * time.Second })
	r := e.record(model.RecordingWindow, false, 1, 2)
	e.frames(r, 0)
	e.stop(r.ID)
	e.wait(r.ID, model.SummaryRunning)
	if _, err := e.s.Summarize(r.ID); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("while running: %v", err)
	}
	if st := e.s.Settings(); st.Running != r.ID {
		t.Fatalf("running: %+v", st)
	}
	x := e.wait(r.ID, model.SummaryFailed)
	if !strings.Contains(x.Error, "took longer than 3s and was stopped") {
		t.Fatalf("timeout: %+v", x)
	}
}

func TestClaudeFailures(t *testing.T) {
	for _, c := range []struct{ mode, want string }{
		{"malformed", "Claude's answer has no summary"},
		{"error", "API Error: 529 Overloaded"},
		{"maxturns", "used up its 4 turns"},
		{"crash", "Invalid API key"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			e := newEnv(t, "ok")
			r := e.record(model.RecordingWindow, false, 1, 2)
			e.frames(r, 1000)
			e.stop(r.ID)
			e.wait(r.ID, model.SummaryDone)
			t.Setenv("TMXG_FAKE_CLAUDE", c.mode)
			if _, err := e.s.Summarize(r.ID); err != nil {
				t.Fatal(err)
			}
			x := e.wait(r.ID, model.SummaryFailed)
			if !strings.Contains(x.Error, c.want) || !strings.HasPrefix(x.Summary, "The owner shows") {
				t.Fatalf("%+v", x) // the earlier summary stays, with the error
			}
		})
	}
	for _, c := range []struct{ mode, want string }{
		{"fenced", "The owner shows a checkout bug."},
		{"prose", "The owner walks through the settings page"},
		{"old", "The owner shows a checkout bug."},
	} {
		t.Run(c.mode, func(t *testing.T) {
			e := newEnv(t, c.mode)
			r := e.record(model.RecordingWindow, false, 1, 2)
			e.frames(r, 0)
			e.stop(r.ID)
			x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed)
			if x.State != model.SummaryDone || !strings.HasPrefix(x.Summary, c.want) {
				t.Fatalf("%+v", x)
			}
			if c.mode == "old" {
				runs := e.runs()
				if len(runs) != 2 || argValue(runs[1].Args, "--tools") != "Read" {
					t.Fatalf("runs: %d", len(runs))
				}
				for _, a := range runs[1].Args {
					if a == "--restricted" {
						t.Fatal("the retry still has --restricted")
					}
				}
			}
		})
	}
	// claude is not there at all
	e := newEnv(t, "ok", func(o *Options) { o.Claude = filepath.Join(t.TempDir(), "no-claude") })
	r := e.record(model.RecordingWindow, false, 1, 2)
	e.frames(r, 0)
	e.stop(r.ID)
	if x := e.wait(r.ID, model.SummaryFailed); !strings.Contains(x.Error, "Claude Code does not start") {
		t.Fatalf("%+v", x)
	}
}

func TestLongTranscriptInParts(t *testing.T) {
	old := MaxChars
	MaxChars = 60
	t.Cleanup(func() { MaxChars = old })
	e := newEnv(t, "parts")
	r := e.record(model.RecordingAudio, true, 3, 2)
	e.frames(r, 0, 4000)
	e.stop(r.ID)
	x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed)
	if x.State != model.SummaryDone || x.Parts != 3 {
		t.Fatalf("summary: %+v", x)
	}
	runs := e.runs()
	if len(runs) != 4 {
		t.Fatalf("%d runs", len(runs))
	}
	for i, run := range runs[:3] {
		if !strings.Contains(run.Stdin, fmt.Sprintf("This is part %d of 3", i+1)) || argValue(run.Args, "--tools") != "" ||
			argValue(run.Args, "--max-turns") != "2" || strings.Contains(run.Stdin, "frames/") {
			t.Fatalf("part run %d: %q\n%s", i, run.Args, run.Stdin)
		}
	}
	last := runs[3]
	for _, want := range []string{`<part n="1" from="0:00" to="0:02">`, `"summary":"Part 2 is about item 2."`, `<part n="3"`, "- 0:04 frames/f-000004000.jpg"} {
		if !strings.Contains(last.Stdin, want) {
			t.Fatalf("combine prompt lacks %q:\n%s", want, last.Stdin)
		}
	}
	if argValue(last.Args, "--tools") != "Read" {
		t.Fatalf("combine args: %q", last.Args)
	}
}

func TestRecordingRemovedDropsSummary(t *testing.T) {
	e := newEnv(t, "ok")
	r := e.record(model.RecordingWindow, false, 1, 2)
	e.frames(r, 0)
	e.stop(r.ID)
	e.wait(r.ID, model.SummaryDone)
	if err := e.rec.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := e.st.GetSummary(r.ID); errors.Is(err, store.ErrNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the summary outlived its recording")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.s.Get(r.ID); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("get: %v", err)
	}
}

func TestGetWithoutSummary(t *testing.T) {
	e := newEnv(t, "ok")
	e.setAuto(false)
	e.voice.set(func(f *fakeVoice) { f.auto = false })
	r := e.record(model.RecordingWindow, false, 1, 2)
	e.stop(r.ID)
	x, err := e.s.Get(r.ID)
	if err != nil || x.State != model.SummaryNone || x.KeyPoints == nil || x.Chapters == nil {
		t.Fatalf("%+v %v", x, err)
	}
	b, _ := json.Marshal(x)
	if !strings.Contains(string(b), `"key_points":[]`) || !strings.Contains(string(b), `"chapters":[]`) {
		t.Fatalf("json: %s", b)
	}
}
