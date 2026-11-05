package transcribe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/slots"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// fakeCatalog replaces the model catalog with small files (the real ones are 60 MB and up) and
// returns their contents by name.
func fakeCatalog(t *testing.T) map[string][]byte {
	t.Helper()
	oldModels, oldVAD := Models, VADModel
	t.Cleanup(func() { Models, VADModel = oldModels, oldVAD })
	files := map[string][]byte{}
	mk := func(name, file string, heavy, vad bool) Model {
		b := []byte(strings.Repeat(name, 300))
		h := sha256.Sum256(b)
		files[name] = b
		return Model{Name: name, File: file, Label: strings.ToUpper(name[:1]) + name[1:], Size: int64(len(b)), SHA256: hex.EncodeToString(h[:]),
			Repo: "org/repo", Revision: "rev1", RAMMB: 100, Heavy: heavy, VAD: vad}
	}
	Models = []Model{mk("base-q5_1", "ggml-base-q5_1.bin", false, false), mk("small-q5_1", "ggml-small-q5_1.bin", true, false)}
	VADModel = mk("silero-v6.2.0", "ggml-silero-v6.2.0.bin", false, true)
	return files
}

type testEnv struct {
	t      *testing.T
	s      *Service
	home   string
	rec    string
	st     *store.Store
	log    string
	files  map[string][]byte
	slots  *slots.Manager
	mu     sync.Mutex
	cfg    config.VoiceConfig
	events []model.BusEvent
}

type envOpt func(*testEnv, *Options)

func noEngine(e *testEnv, o *Options) { o.GOOS = "plan9" }

// newEnv starts a service in a throwaway home with the fake engine and the (fake) default and
// VAD models installed.
func newEnv(t *testing.T, opts ...envOpt) *testEnv {
	t.Helper()
	files := fakeCatalog(t)
	home := t.TempDir()
	e := &testEnv{t: t, home: home, rec: filepath.Join(home, "recordings"), files: files, log: filepath.Join(home, "engine.log")}
	os.MkdirAll(e.rec, 0o700)
	st, err := store.Open(filepath.Join(home, "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.st = st
	e.slots = slots.New(slots.Options{Sizes: map[string]int{"heavy": 1, "browser": 2}})
	t.Setenv("TMXG_FAKE_WHISPER", "ok")
	t.Setenv("TMXG_FAKE_WHISPER_LOG", e.log)
	o := Options{Paths: config.Paths{Home: home}, Store: st, Slots: e.slots, RecordingsDir: e.rec, Version: "test",
		NumCPU: 4, MemTotal: 32 << 30, NoLowPriority: true,
		Settings: func() config.VoiceConfig { e.mu.Lock(); defer e.mu.Unlock(); return e.cfg },
		Save:     func(c config.VoiceConfig) error { e.mu.Lock(); defer e.mu.Unlock(); e.cfg = c; return nil },
		Publish:  func(ev model.BusEvent) { e.mu.Lock(); defer e.mu.Unlock(); e.events = append(e.events, ev) },
	}
	for _, f := range opts {
		f(e, &o)
	}
	e.s = New(o)
	if o.GOOS == "" || o.GOOS == runtime.GOOS {
		installFake(t, e.s.enginePath())
		e.install(Models[0])
		e.install(VADModel)
	}
	if err := e.s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.s.Close)
	return e
}

func (e *testEnv) install(m Model) {
	e.t.Helper()
	os.MkdirAll(e.s.modelsDir(), 0o700)
	if err := os.WriteFile(e.s.modelPath(m), e.files[m.Name], 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) wav(name string, secs float64) string {
	p := filepath.Join(e.rec, name)
	writeWAV(e.t, p, secs)
	return p
}

// wait polls until the transcript of source reaches a final state.
func (e *testEnv) wait(source string) model.Transcript {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if t, err := e.st.TranscriptBySource(source); err == nil && (t.State == model.TranscriptDone || t.State == model.TranscriptFailed) {
			return t
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("%s did not finish", source)
	return model.Transcript{}
}

func (e *testEnv) until(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

// runs reads the fake engine's log: the input file of every run, in order of start; it fails
// the test if two runs overlapped.
func (e *testEnv) runs() []string {
	e.t.Helper()
	f, err := os.Open(e.log)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	open := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || (fields[0] != "start" && fields[0] != "end") {
			continue
		}
		if fields[0] == "start" {
			open++
			if open > 1 {
				e.t.Errorf("two engine runs at once: %s", sc.Text())
			}
			for i, f := range fields {
				if f == "-f" && i+1 < len(fields) {
					out = append(out, filepath.Base(fields[i+1]))
				}
			}
		} else {
			open--
		}
	}
	return out
}

func (e *testEnv) eventTypes(source string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		if t, ok := ev.Data.(model.Transcript); ok && ev.Type == model.EvTranscriptUpdated && t.Source == source {
			out = append(out, string(t.State))
		}
	}
	return out
}

func TestEnqueueTranscribesAndStores(t *testing.T) {
	e := newEnv(t)
	if !e.s.AutoTranscribe() {
		t.Fatal("auto-transcribe is off by default")
	}
	st := e.s.Status()
	if !st.Ready || st.Settings.Model != DefaultModel || st.Settings.Language != "auto" || st.Settings.Threads != 2 || !st.Settings.VAD {
		t.Fatalf("status: %+v", st)
	}
	p := e.wav("r1/seg-0001.wav", 3)
	if err := e.s.Enqueue("recording:r1/1", p, ""); err != nil {
		t.Fatal(err)
	}
	tr := e.wait("recording:r1/1")
	if tr.State != model.TranscriptDone || tr.Text != "fake words of seg-0001.wav" || tr.Language != "en" || tr.Model != DefaultModel {
		t.Fatalf("%+v", tr)
	}
	if len(tr.Segments) != 1 || tr.Segments[0].ToMS != 1500 { // the [BLANK_AUDIO] segment is dropped
		t.Fatalf("segments %+v", tr.Segments)
	}
	if got := strings.Join(e.eventTypes("recording:r1/1"), ","); got != "queued,running,done" {
		t.Fatalf("events: %s", got)
	}
	b, _ := os.ReadFile(e.log)
	if !bytes.Contains(b, []byte("--vad -vm "+e.s.modelPath(VADModel))) || !bytes.Contains(b, []byte("-l auto")) {
		t.Fatalf("args: %s", b)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("the recording's audio was removed; it belongs to the recorder")
	}
	// Search finds it; the same source again runs again (same transcript).
	list, _ := e.s.List(store.TranscriptQuery{Q: "fake words"})
	if len(list) != 1 || list[0].ID != tr.ID {
		t.Fatalf("search: %+v", list)
	}
	if err := e.s.Enqueue("recording:r1/1", p, "de"); err != nil {
		t.Fatal(err)
	}
	e.until("the second run", func() bool { return len(e.runs()) == 2 })
	again := e.wait("recording:r1/1")
	if again.ID != tr.ID || again.Language != "de" {
		t.Fatalf("again: %+v", again)
	}
	if b, _ := os.ReadFile(e.log); !bytes.Contains(b, []byte("-l de")) {
		t.Fatalf("language not passed: %s", b)
	}
}

func TestEnqueueRefuses(t *testing.T) {
	e := newEnv(t)
	good := e.wav("ok.wav", 1)
	outside := filepath.Join(t.TempDir(), "secret.wav")
	writeWAV(t, outside, 1)
	link := filepath.Join(e.rec, "link.wav")
	if err := os.Symlink(outside, link); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	notWAV := filepath.Join(e.rec, "notes.txt")
	os.WriteFile(notWAV, []byte("hello"), 0o600)
	for name, c := range map[string]struct{ source, path string }{
		"outside":   {"recording:x/1", outside},
		"traversal": {"recording:x/1", e.rec + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(e.rec) + "x"},
		"dotdot":    {"recording:x/1", filepath.Join(e.rec, "..", "tmx.db")},
		"symlink":   {"recording:x/1", link},
		"relative":  {"recording:x/1", "recordings/ok.wav"},
		"folder":    {"recording:x/1", e.rec},
		"not a wav": {"recording:x/1", notWAV},
		"no source": {"", good},
	} {
		if err := e.s.Enqueue(c.source, c.path, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := e.s.Enqueue("recording:x/1", good, "english please"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad language: %v", err)
	}
	os.Remove(e.s.modelPath(Models[0]))
	err := e.s.Enqueue("recording:x/1", good, "")
	if !errors.Is(err, ErrNotSetUp) || !strings.Contains(err.Error(), "base-q5_1") {
		t.Fatalf("not set up: %v", err)
	}
	if list, _ := e.s.List(store.TranscriptQuery{}); len(list) != 0 {
		t.Fatalf("refused jobs were stored: %+v", list)
	}
	if len(e.runs()) != 0 {
		t.Fatal("the engine ran")
	}
}

func TestJobsOneAtATimeInAHeavySlot(t *testing.T) {
	e := newEnv(t)
	builder := slots.Holder{AgentID: "a1", Name: "builder"}
	held, err := e.slots.Acquire("heavy", builder, "make test", 0)
	if err != nil || held.State != slots.Held {
		t.Fatal(held, err)
	}
	for i := 1; i <= 3; i++ {
		if err := e.s.Enqueue(fmt.Sprintf("recording:r/%d", i), e.wav(fmt.Sprintf("s%d.wav", i), 1), ""); err != nil {
			t.Fatal(err)
		}
	}
	e.until("a job waiting for the slot", func() bool {
		st := e.s.Status()
		return st.Job != nil && st.Job.Phase == "waiting" && st.Queued == 2
	})
	time.Sleep(200 * time.Millisecond)
	if len(e.runs()) != 0 {
		t.Fatal("the engine ran without a heavy slot")
	}
	var sawLease bool
	if err := e.slots.Release(held.ID, builder); err != nil {
		t.Fatal(err)
	}
	e.until("a lease of the voice job", func() bool {
		for _, l := range e.slots.View().Leases {
			if strings.HasPrefix(l.Command, "voice transcription") && l.State == slots.Held {
				sawLease = true
			}
		}
		return sawLease || len(e.runs()) == 3
	})
	for i := 1; i <= 3; i++ {
		if tr := e.wait(fmt.Sprintf("recording:r/%d", i)); tr.State != model.TranscriptDone {
			t.Fatalf("%d: %+v", i, tr)
		}
	}
	if got := strings.Join(e.runs(), ","); got != "s1.wav,s2.wav,s3.wav" {
		t.Fatalf("order: %s", got)
	}
	e.until("the slots are free", func() bool { return len(e.slots.View().Leases) == 0 })
}

func TestDictationGoesFirstAndIsNotStored(t *testing.T) {
	e := newEnv(t)
	builder := slots.Holder{AgentID: "a1", Name: "builder"}
	held, _ := e.slots.Acquire("heavy", builder, "make test", 0)
	e.s.Enqueue("recording:r/1", e.wav("s1.wav", 1), "")
	e.s.Enqueue("recording:r/2", e.wav("s2.wav", 1), "")
	e.until("s1 waiting", func() bool { st := e.s.Status(); return st.Job != nil && st.Queued == 1 })

	var wav bytes.Buffer
	p := filepath.Join(t.TempDir(), "d.wav")
	writeWAV(t, p, 2)
	b, _ := os.ReadFile(p)
	wav.Write(b)
	done := make(chan Result, 1)
	go func() {
		r, err := e.s.Dictate(context.Background(), wav.Bytes(), "")
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	e.until("dictation queued", func() bool { return e.s.Status().Queued == 2 })
	e.slots.Release(held.ID, builder)
	r := <-done
	if !strings.HasPrefix(r.Text, "fake words of dictation-") || r.Language != "en" || len(r.Segments) != 1 {
		t.Fatalf("%+v", r)
	}
	e.wait("recording:r/2")
	runs := e.runs()
	if len(runs) != 3 || runs[0] != "s1.wav" || !strings.HasPrefix(runs[1], "dictation-") || runs[2] != "s2.wav" {
		t.Fatalf("order: %v", runs)
	}
	if list, _ := e.s.List(store.TranscriptQuery{SourcePrefix: "dictation"}); len(list) != 0 {
		t.Fatal("dictation was stored")
	}
	if left, _ := os.ReadDir(e.s.tmpDir()); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestDictationLimits(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(t.TempDir(), "long.wav")
	writeWAV(t, p, 130)
	b, _ := os.ReadFile(p)
	if _, err := e.s.Dictate(context.Background(), b, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "at most 2m0s") {
		t.Fatalf("too long: %v", err)
	}
	if _, err := e.s.Dictate(context.Background(), []byte("hello"), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not a wav: %v", err)
	}
	writeWAV(t, p, 1)
	b, _ = os.ReadFile(p)
	if _, err := e.s.Dictate(context.Background(), b, "xx-yy"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad language: %v", err)
	}
	// Dictation does not wait forever for a slot somebody keeps.
	old := slotPatienceForTest(300 * time.Millisecond)
	defer old()
	e.slots.Acquire("heavy", slots.Holder{AgentID: "a1", Name: "builder"}, "make test", 0)
	r, err := e.s.Dictate(context.Background(), b, "de")
	if err != nil || r.Language != "de" {
		t.Fatalf("with the slot taken: %+v %v", r, err)
	}
	// A caller that goes away takes its job along.
	t.Setenv("TMXG_FAKE_WHISPER", "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := e.s.Dictate(ctx, b, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
	e.until("the worker is free", func() bool { return e.s.Status().Job == nil })
}

func TestTranscribeFile(t *testing.T) {
	e := newEnv(t)
	mp3 := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 100)...)
	r, err := e.s.TranscribeFile(context.Background(), bytes.NewReader(mp3), "")
	if err != nil || !strings.Contains(r.Text, ".mp3") {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := e.s.TranscribeFile(context.Background(), strings.NewReader("<html>no audio</html>"), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("garbage: %v", err)
	}
	if _, err := e.s.TranscribeFile(context.Background(), strings.NewReader(""), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty: %v", err)
	}
}

func TestLongRecordingRunsInParts(t *testing.T) {
	old := partLength
	partLength = 4 * time.Second
	defer func() { partLength = old }()
	e := newEnv(t)
	e.s.Enqueue("recording:long/1", e.wav("long.wav", 9.3), "")
	tr := e.wait("recording:long/1")
	if tr.State != model.TranscriptDone || len(tr.Segments) != 3 {
		t.Fatalf("%+v", tr)
	}
	for i := 1; i < len(tr.Segments); i++ {
		if tr.Segments[i].FromMS <= tr.Segments[i-1].FromMS || tr.Segments[i].FromMS < 3000*int64(i) {
			t.Fatalf("offsets not moved along: %+v", tr.Segments)
		}
	}
	if runs := e.runs(); len(runs) != 3 || runs[0] != "part-000.wav" {
		t.Fatalf("runs %v", runs)
	}
}

func TestFailuresAreStored(t *testing.T) {
	e := newEnv(t)
	t.Setenv("TMXG_FAKE_WHISPER", "fail")
	e.s.Enqueue("recording:f/1", e.wav("f1.wav", 1), "")
	if tr := e.wait("recording:f/1"); tr.State != model.TranscriptFailed || !strings.Contains(tr.Error, "failed to initialize whisper context") {
		t.Fatalf("%+v", tr)
	}
	t.Setenv("TMXG_FAKE_WHISPER", "slow")
	oldMargin := timeoutMargin
	timeoutMargin = 300 * time.Millisecond
	defer func() { timeoutMargin = oldMargin }()
	e.s.Enqueue("recording:f/2", e.wav("f2.wav", 0.1), "")
	if tr := e.wait("recording:f/2"); tr.State != model.TranscriptFailed || !strings.Contains(tr.Error, "took longer than") {
		t.Fatalf("%+v", tr)
	}
}

func TestDeleteStopsTheJob(t *testing.T) {
	e := newEnv(t)
	t.Setenv("TMXG_FAKE_WHISPER", "slow")
	e.s.Enqueue("recording:d/1", e.wav("d1.wav", 1), "")
	e.s.Enqueue("recording:d/2", e.wav("d2.wav", 1), "")
	e.until("d1 running", func() bool {
		tr, _ := e.st.TranscriptBySource("recording:d/1")
		return tr.State == model.TranscriptRunning && len(e.runs()) == 1
	})
	d1, _ := e.st.TranscriptBySource("recording:d/1")
	d2, _ := e.st.TranscriptBySource("recording:d/2")
	t.Setenv("TMXG_FAKE_WHISPER", "ok")
	if err := e.s.Delete(d2.ID); err != nil { // queued: never runs
		t.Fatal(err)
	}
	if err := e.s.Delete(d1.ID); err != nil { // running: stopped
		t.Fatal(err)
	}
	if err := e.s.Delete(d1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	e.until("the worker is free", func() bool { return e.s.Status().Job == nil })
	time.Sleep(100 * time.Millisecond)
	if _, err := e.s.Get(d1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted transcript came back: %v", err)
	}
	if runs := e.runs(); len(runs) != 1 {
		t.Fatalf("runs %v", runs)
	}
	e.s.Enqueue("recording:d/3", e.wav("d3.wav", 1), "")
	if tr := e.wait("recording:d/3"); tr.State != model.TranscriptDone {
		t.Fatalf("after a delete: %+v", tr)
	}
}

func TestRestartPicksUpUnfinished(t *testing.T) {
	files := fakeCatalog(t)
	_ = files
	home := t.TempDir()
	rec := filepath.Join(home, "recordings")
	st, _ := store.Open(filepath.Join(home, "tmx.db"))
	defer st.Close()
	audio := filepath.Join(rec, "a.wav")
	writeWAV(t, audio, 1)
	now := model.Now()
	st.PutTranscript(model.Transcript{ID: "t1", Source: "recording:a/1", State: model.TranscriptRunning, CreatedAt: now, UpdatedAt: now}, audio)
	st.PutTranscript(model.Transcript{ID: "t2", Source: "recording:a/2", State: model.TranscriptQueued, CreatedAt: now, UpdatedAt: now}, filepath.Join(rec, "gone.wav"))
	t.Setenv("TMXG_FAKE_WHISPER", "ok")
	s := New(Options{Paths: config.Paths{Home: home}, Store: st, RecordingsDir: rec, NoLowPriority: true})
	installFake(t, s.enginePath())
	os.MkdirAll(s.modelsDir(), 0o700)
	os.WriteFile(s.modelPath(Models[0]), files[Models[0].Name], 0o600)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := &testEnv{t: t, st: st}
	if tr := e.wait("recording:a/1"); tr.State != model.TranscriptDone {
		t.Fatalf("t1 %+v", tr)
	}
	if tr := e.wait("recording:a/2"); tr.State != model.TranscriptFailed || !strings.Contains(tr.Error, "audio is gone") {
		t.Fatalf("t2 %+v", tr)
	}
}

func TestSettingsAndWarnings(t *testing.T) {
	e := newEnv(t, func(e *testEnv, o *Options) { o.MemTotal = 8 << 30 })
	for name, p := range map[string]SettingsPatch{
		"model":    {Model: ptr("huge")},
		"vad":      {Model: ptr(VADModel.Name)},
		"language": {Language: ptr("klingon")},
		"threads":  {Threads: ptr(100)},
	} {
		if _, err := e.s.Configure(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	st, err := e.s.Configure(SettingsPatch{Model: ptr("small-q5_1"), Language: ptr("DE"), Threads: ptr(3), VAD: ptr(false), AutoTranscribe: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if e.cfg.Model != "small-q5_1" || e.cfg.Language != "de" || e.cfg.Threads != 3 || *e.cfg.VAD || e.cfg.AutoTranscribeOn() || e.s.AutoTranscribe() {
		t.Fatalf("saved: %+v", e.cfg)
	}
	if st.Ready || !strings.Contains(st.Reason, "small-q5_1") {
		t.Fatalf("a model that is not downloaded is ready: %+v", st)
	}
	var small ModelStatus
	for _, m := range st.Models {
		if m.Name == "small-q5_1" {
			small = m
		}
		if m.Name == "base-q5_1" && m.Warning != "" {
			t.Errorf("base warns: %s", m.Warning)
		}
	}
	if !small.Selected || small.Installed || !strings.Contains(small.Warning, "8 GB") {
		t.Fatalf("small: %+v", small)
	}
	e2 := newEnv(t)
	for _, m := range e2.s.Status().Models {
		if m.Warning != "" {
			t.Errorf("a 32 GB machine is warned: %s", m.Warning)
		}
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	e := newEnv(t, noEngine)
	st := e.s.Status()
	if st.Ready || st.Engine.Available || !strings.Contains(st.Reason, "not available on this platform (plan9/") {
		t.Fatalf("%+v", st)
	}
	if _, err := e.s.Setup(); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("setup: %v", err)
	}
	if _, err := e.s.Dictate(context.Background(), fallbackWAV(false), ""); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("dictate: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func slotPatienceForTest(d time.Duration) (restore func()) {
	old := slotPatience
	slotPatience = d
	return func() { slotPatience = old }
}

// ---- downloads through the service

// fakeGitHub serves the release whisper-engine-<tag> the way GitHub's API does.
type fakeGitHub struct {
	srv      *httptest.Server
	assets   map[string][]byte
	noTag    bool // the release does not exist
	noAccess bool // nothing is visible
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{assets: map[string][]byte{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/repos/blackcode-ch/tmx-go/releases"
		switch {
		case g.noAccess && strings.HasPrefix(r.URL.Path, "/repos/"):
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case r.URL.Path == base+"/tags/"+EngineRelease() && !g.noTag:
			var assets []map[string]any
			for name, b := range g.assets {
				assets = append(assets, map[string]any{"name": name, "size": len(b), "url": g.srv.URL + "/assets/" + name})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": EngineRelease(), "html_url": "https://example.invalid/r", "assets": assets})
		case r.URL.Path == base:
			w.Write([]byte("[]"))
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			b, ok := g.assets[strings.TrimPrefix(r.URL.Path, "/assets/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, "a", time.Time{}, bytes.NewReader(b))
		default:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// publish attaches files to the release with a SHA256SUMS signed by key.
func (g *fakeGitHub) publish(key ed25519.PrivateKey, files map[string][]byte) {
	var sums strings.Builder
	for name, b := range files {
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%x  %s\n", h, name)
		g.assets[name] = b
	}
	g.assets["SHA256SUMS"] = []byte(sums.String())
	g.assets["SHA256SUMS.sig"] = []byte(update.FleetKeyID + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(update.FleetExtraSignatureContext+"\nblackcode-ch/tmx-go\n"+EngineRelease()+"\n"+sums.String()))) + "\n")
}

func withGitHub(g *fakeGitHub, trusted ed25519.PublicKey, hf *httptest.Server) envOpt {
	return func(e *testEnv, o *Options) {
		e.t.Setenv("TMX_GITHUB_TOKEN", "")
		e.t.Setenv("GH_TOKEN", "")
		e.t.Setenv("GITHUB_TOKEN", "")
		o.Releases = update.New(update.Options{Paths: o.Paths, Version: "test", APIBase: g.srv.URL, HTTP: g.srv.Client(),
			Keys: []string{base64.StdEncoding.EncodeToString(trusted)}, GH: func() string { return "" }})
		if hf != nil {
			o.ModelBase, o.HTTP = hf.URL, hf.Client()
		}
	}
}

func emptyEnv(t *testing.T, opts ...envOpt) *testEnv {
	t.Helper()
	e := newEnv(t, opts...)
	os.RemoveAll(e.s.voiceDir()) // nothing installed
	os.MkdirAll(e.s.tmpDir(), 0o700)
	return e
}

func (e *testEnv) waitIdle() Status {
	e.t.Helper()
	var st Status
	e.until("downloads to end", func() bool { st = e.s.Status(); return !st.Busy })
	return st
}

func TestSetupDownloadsVerified(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	exe, _ := os.Executable()
	bin, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	g := newFakeGitHub(t)
	asset := engineAsset(runtime.GOOS, runtime.GOARCH)
	if asset == "" {
		t.Skip("no engine for this platform")
	}
	g.publish(priv, map[string][]byte{asset: bin})
	var hfHits []string
	var hfMu sync.Mutex
	files := map[string][]byte{}
	hf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hfMu.Lock()
		hfHits = append(hfHits, r.URL.Path)
		hfMu.Unlock()
		for _, m := range append([]Model{VADModel}, Models...) {
			if r.URL.Path == "/"+m.Repo+"/resolve/"+m.Revision+"/"+m.File {
				http.ServeContent(w, r, m.File, time.Time{}, bytes.NewReader(files[m.Name]))
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer hf.Close()
	e := emptyEnv(t, withGitHub(g, pub, hf))
	for k, v := range e.files {
		files[k] = v
	}
	if st := e.s.Status(); st.Ready || st.Engine.Installed {
		t.Fatalf("installed before setup: %+v", st)
	}
	st, err := e.s.Setup()
	if err != nil || !st.Busy {
		t.Fatalf("setup: %+v %v", st, err)
	}
	st = e.waitIdle()
	if !st.Ready || !st.Engine.Installed || !st.VAD.Installed || st.Engine.Error != "" {
		t.Fatalf("after setup: %+v", st)
	}
	if fi, _ := os.Stat(e.s.enginePath()); runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("the engine is not executable")
	}
	if hfHits[0] != "/org/repo/resolve/rev1/ggml-base-q5_1.bin" {
		t.Fatalf("model URL: %v", hfHits)
	}
	// It works end to end.
	e.s.Enqueue("recording:s/1", e.wav("s.wav", 1), "")
	if tr := e.wait("recording:s/1"); tr.State != model.TranscriptDone {
		t.Fatalf("%+v", tr)
	}
	// Another model, then removing it.
	if _, err := e.s.GetModel("small-q5_1"); err != nil {
		t.Fatal(err)
	}
	e.until("small installed", func() bool { return e.s.modelInstalled(Models[1]) })
	if _, err := e.s.RemoveModel("small-q5_1"); err != nil || e.s.modelInstalled(Models[1]) {
		t.Fatalf("remove: %v", err)
	}
	if _, err := e.s.GetModel("nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown model: %v", err)
	}
}

func TestEngineDownloadRefusesForgeries(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	asset := engineAsset(runtime.GOOS, runtime.GOARCH)
	if asset == "" {
		t.Skip("no engine for this platform")
	}
	exe, _ := os.Executable()
	bin, _ := os.ReadFile(exe)
	cases := []struct {
		name  string
		setup func(g *fakeGitHub)
		mode  string
		want  string
	}{
		{"signed by another key", func(g *fakeGitHub) { g.publish(other, map[string][]byte{asset: bin}) }, "ok", "not signed with a key this tmx knows"},
		{"binary swapped", func(g *fakeGitHub) {
			g.publish(priv, map[string][]byte{asset: bin})
			g.assets[asset] = append([]byte("evil"), bin[4:]...)
		}, "ok", "does not match its checksum"},
		{"unsigned", func(g *fakeGitHub) {
			g.publish(priv, map[string][]byte{asset: bin})
			delete(g.assets, "SHA256SUMS.sig")
		}, "ok", "not signed"},
		{"no build for this platform", func(g *fakeGitHub) { g.publish(priv, map[string][]byte{"whisper-cli_other": bin}) }, "ok", "no voice engine for " + runtime.GOOS},
		{"wrong program", func(g *fakeGitHub) { g.publish(priv, map[string][]byte{asset: bin}) }, "badversion", "0.0.1"},
		{"not published yet", func(g *fakeGitHub) { g.noTag = true }, "ok", "not published yet"},
		{"private, no token", func(g *fakeGitHub) { g.noAccess = true }, "ok", "gh auth login"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			c.setup(g)
			e := emptyEnv(t, withGitHub(g, pub, nil))
			t.Setenv("TMXG_FAKE_WHISPER", c.mode)
			e.s.Setup()
			st := e.waitIdle()
			if st.Engine.Installed || !strings.Contains(st.Engine.Error, c.want) {
				t.Fatalf("engine: %+v", st.Engine)
			}
			if _, err := os.Stat(e.s.enginePath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a refused engine is on disk")
			}
			if c.name == "private, no token" && !st.Engine.NeedsToken {
				t.Fatal("needs_token not set")
			}
			if st.Models[0].Installed {
				t.Fatal("setup went on after the engine failed")
			}
		})
	}
}
