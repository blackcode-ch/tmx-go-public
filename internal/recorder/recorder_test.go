package recorder

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	s      *Service
	st     *store.Store
	clk    *clock
	dir    string
	free   uint64
	events []model.BusEvent
	mu     sync.Mutex
	cfg    config.RecorderConfig
}

func newRig(t *testing.T) *rig {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{st: st, clk: &clock{t: time.Date(2026, 10, 28, 14, 3, 0, 0, time.UTC)}, dir: filepath.Join(home, "recordings"), free: 50 << 30}
	r.s = r.service()
	if err := r.s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.s.Close)
	return r
}

func (r *rig) service() *Service {
	return New(Options{Store: r.st, Dir: r.dir, Now: r.clk.now,
		Publish:  func(ev model.BusEvent) { r.mu.Lock(); r.events = append(r.events, ev); r.mu.Unlock() },
		DiskFree: func(string) (uint64, error) { return r.free, nil },
		Settings: func() config.RecorderConfig { return r.cfg }})
}

func (r *rig) lastEvent(t *testing.T) model.BusEvent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		t.Fatal("no events")
	}
	return r.events[len(r.events)-1]
}

func begin(t *testing.T, s *Service, kind model.RecordingKind, mic bool) model.Recording {
	t.Helper()
	mime := "video/webm;codecs=vp8,opus"
	if kind == model.RecordingAudio {
		mime = "audio/webm;codecs=opus"
	}
	rec, err := s.Begin(BeginRequest{Kind: kind, Mic: mic, Mime: mime})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func mustAppend(t *testing.T, s *Service, id string, n int, tr Track, off int64, data []byte) int64 {
	t.Helper()
	size, err := s.Append(id, n, tr, off, 0, data)
	if err != nil {
		t.Fatalf("append %d/%s@%d: %v", n, tr, off, err)
	}
	return size
}

func pcm(ms int) []byte { return bytes.Repeat([]byte{1, 0}, ms*PCMRate/1000) }

func TestBeginValidatesAndAllowsOne(t *testing.T) {
	r := newRig(t)
	for _, bad := range []BeginRequest{
		{Kind: "webcam", Mime: "video/webm"},
		{Kind: model.RecordingCamera, Mime: "audio/webm"},
		{Kind: model.RecordingCamera, Mic: true, SystemAudio: true, Mime: "video/webm"}, // device audio only with the screen
		{Kind: model.RecordingAudio, Mic: true, SystemAudio: true, Mime: "audio/webm"},
		{Kind: model.RecordingScreen, Mime: "audio/webm"},
		{Kind: model.RecordingAudio, Mime: "audio/webm"}, // no mic
		{Kind: model.RecordingAudio, Mic: true, Mime: "video/webm"},
		{Kind: model.RecordingScreen, Mime: "text/html"},
		{Kind: model.RecordingScreen, Mime: "video/webm", Title: strings.Repeat("x", MaxTitle+1)},
	} {
		if _, err := r.s.Begin(bad); fleet.KindOf(err) != fleet.KindInvalid {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	rec := begin(t, r.s, model.RecordingScreen, true)
	if rec.State != model.RecordingActive || rec.Title != "Screen recording 28 Oct 14:03" || rec.Dir != filepath.Join(r.dir, rec.ID) {
		t.Fatalf("begin: %+v", rec)
	}
	// Windows does not expose POSIX permission bits; still verify the file type.
	if st, err := os.Stat(rec.Dir); err != nil || !st.IsDir() || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o700) {
		t.Fatalf("dir: %v %v", st, err)
	}
	if ev := r.lastEvent(t); ev.Type != model.EvRecordingUpdated {
		t.Fatalf("event %s", ev.Type)
	}
	if _, err := r.s.Begin(BeginRequest{Kind: model.RecordingAudio, Mic: true, Mime: "audio/webm"}); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("second recording: %v", err)
	}
	if a := r.s.Active(); a == nil || a.ID != rec.ID {
		t.Fatalf("active: %+v", a)
	}
	if b := r.s.Busy(); len(b) != 1 {
		t.Fatalf("busy: %v", b)
	}
}

func TestBeginNeedsFreeDisk(t *testing.T) {
	r := newRig(t)
	r.free = 300 << 20
	if _, err := r.s.Begin(BeginRequest{Kind: model.RecordingAudio, Mic: true, Mime: "audio/webm"}); err == nil || !strings.Contains(err.Error(), "1 GB") {
		t.Fatalf("low disk: %v", err)
	}
}

func TestChunksAreIdempotentAndGapsRefused(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingScreen, true)
	a, b := []byte("0123456789"), []byte("abcdef")
	if n := mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, a); n != 10 {
		t.Fatalf("size %d", n)
	}
	if n := mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, a); n != 10 { // a retry of the same chunk
		t.Fatalf("retry size %d", n)
	}
	if n := mustAppend(t, r.s, rec.ID, 1, TrackMedia, 7, []byte("789XY")); n != 12 { // overlapping: only the tail is new
		t.Fatalf("overlap size %d", n)
	}
	_, err := r.s.Append(rec.ID, 1, TrackMedia, 20, 0, b)
	var gap *GapError
	if !errors.As(err, &gap) || gap.Size != 12 {
		t.Fatalf("gap: %v", err)
	}
	if _, err := r.s.Append(rec.ID, 3, TrackMedia, 0, 0, b); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("segment skipped: %v", err)
	}
	if _, err := r.s.Append(rec.ID, 1, TrackPCM, 0, 0, []byte{1, 2, 3}); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("odd PCM: %v", err)
	}
	if n, err := r.s.Append(rec.ID, 1, TrackMedia, 12, 0, nil); err != nil || n != 12 { // heartbeat
		t.Fatalf("heartbeat: %d %v", n, err)
	}
	got, _ := os.ReadFile(filepath.Join(rec.Dir, "seg-0001.webm"))
	if string(got) != "0123456789XY" {
		t.Fatalf("file: %q", got)
	}
	cur, _ := r.s.Get(rec.ID)
	if len(cur.Segments) != 1 || cur.Segments[0].Bytes != 12 || cur.Bytes != 12 || cur.Segments[0].File != filepath.Join(rec.Dir, "seg-0001.webm") {
		t.Fatalf("live view: %+v", cur)
	}
	// a screen recording without the microphone has no PCM track
	r2 := newRig(t)
	rec2 := begin(t, r2.s, model.RecordingWindow, false)
	if _, err := r2.s.Append(rec2.ID, 1, TrackPCM, 0, 0, pcm(10)); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("pcm without mic: %v", err)
	}
}

func TestSegmentsPauseStopAndFinalize(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingScreen, true)
	m1, m2 := bytes.Repeat([]byte("A"), 1000), bytes.Repeat([]byte("B"), 700)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, m1)
	mustAppend(t, r.s, rec.ID, 1, TrackPCM, 0, pcm(1500))
	r.clk.add(2 * time.Second)
	if x, err := r.s.SetState(rec.ID, "paused"); err != nil || x.State != model.RecordingPaused || x.DurationMS != 2000 {
		t.Fatalf("pause: %+v %v", x, err)
	}
	// paused time does not count
	r.clk.add(time.Minute)
	if x, err := r.s.SetState(rec.ID, "paused"); err != nil || x.DurationMS != 2000 { // heartbeat
		t.Fatalf("pause again: %+v %v", x, err)
	}
	if x, err := r.s.SetState(rec.ID, "recording"); err != nil || x.State != model.RecordingActive || x.ResumedAt != r.clk.now().UnixMilli() {
		t.Fatalf("resume: %+v %v", x, err)
	}
	mustAppend(t, r.s, rec.ID, 2, TrackMedia, 0, m2) // the window rotated
	mustAppend(t, r.s, rec.ID, 2, TrackPCM, 0, pcm(500))
	r.clk.add(time.Second)
	fin, err := r.s.SetState(rec.ID, "stopped")
	if err != nil {
		t.Fatal(err)
	}
	if fin.State != model.RecordingDone || fin.EndedAt == 0 || fin.ResumedAt != 0 || len(fin.Segments) != 2 || fin.Bytes != 1700 {
		t.Fatalf("finished: %+v", fin)
	}
	if fin.DurationMS != 2000 { // from the WAVs: 1.5 s + 0.5 s
		t.Fatalf("duration %d", fin.DurationMS)
	}
	want := sha256.Sum256(m1)
	if fin.Segments[0].SHA256 != hex.EncodeToString(want[:]) || fin.Segments[0].DurationMS != 1500 {
		t.Fatalf("segment 1: %+v", fin.Segments[0])
	}
	// the WAV header is complete
	wav, _ := os.ReadFile(fin.Segments[1].WAV)
	if len(wav) != wavHeaderSize+16000 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" ||
		binary.LittleEndian.Uint32(wav[40:]) != 16000 || binary.LittleEndian.Uint32(wav[24:]) != 16000 || binary.LittleEndian.Uint32(wav[4:]) != 36+16000 {
		t.Fatalf("wav header: % x", wav[:44])
	}
	// finished: nothing more, stopping again is harmless, the store has it
	if _, err := r.s.Append(rec.ID, 2, TrackMedia, 700, 0, []byte("x")); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("append after stop: %v", err)
	}
	if again, err := r.s.SetState(rec.ID, "stopped"); err != nil || again.State != model.RecordingDone {
		t.Fatalf("stop again: %+v %v", again, err)
	}
	stored, err := r.st.GetRecording(rec.ID)
	if err != nil || stored.DurationMS != 2000 || stored.Segments[1].SHA256 == "" || stored.Segments[1].WAVBytes != wavHeaderSize+16000 {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	if r.s.Active() != nil {
		t.Fatal("still active")
	}
	list, _ := r.s.List(0)
	if len(list) != 1 || list[0].Segments[0].File != filepath.Join(rec.Dir, "seg-0001.webm") {
		t.Fatalf("list: %+v", list)
	}
}

func TestDurationWithoutMicUsesTheWindowsCount(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingWindow, false)
	if _, err := r.s.Append(rec.ID, 1, TrackMedia, 0, 4200, []byte("frames")); err != nil {
		t.Fatal(err)
	}
	r.clk.add(5 * time.Second)
	fin, _ := r.s.SetState(rec.ID, "stopped")
	if fin.DurationMS != 4200 || fin.Segments[0].WAV != "" {
		t.Fatalf("finished: %+v", fin)
	}
}

func TestInterruptedSweep(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingAudio, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("opus"))
	mustAppend(t, r.s, rec.ID, 1, TrackPCM, 0, pcm(100))
	r.clk.add(15 * time.Second)
	r.s.Sweep()
	if r.s.Active() == nil {
		t.Fatal("interrupted too early")
	}
	// a heartbeat keeps it alive and changes nothing (also not a pause made elsewhere)
	r.s.SetState(rec.ID, "paused")
	if x, err := r.s.SetState(rec.ID, "heartbeat"); err != nil || x.State != model.RecordingPaused {
		t.Fatalf("heartbeat: %+v %v", x, err)
	}
	r.clk.add(15 * time.Second)
	r.s.Sweep()
	if r.s.Active() == nil {
		t.Fatal("interrupted despite the heartbeat")
	}
	r.clk.add(OutageGrace + time.Second)
	r.s.Sweep()
	if r.s.Active() != nil {
		t.Fatal("not interrupted")
	}
	got, _ := r.s.Get(rec.ID)
	if got.State != model.RecordingInterrupted || !strings.Contains(got.Error, "was it closed") || got.Bytes != 4 || got.DurationMS != 100 {
		t.Fatalf("interrupted: %+v", got)
	}
	if ev := r.lastEvent(t); ev.Type != model.EvRecordingUpdated || ev.Data.(model.Recording).State != model.RecordingInterrupted {
		t.Fatalf("event: %+v", ev)
	}
	if _, err := r.s.Append(rec.ID, 1, TrackMedia, 4, 0, []byte("late")); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("late chunk: %v", err)
	}
	// a session with no data at all is a failed one
	rec2 := begin(t, r.s, model.RecordingAudio, true)
	r.clk.add(OutageGrace + time.Second)
	r.s.Sweep()
	if got, _ := r.s.Get(rec2.ID); got.State != model.RecordingFailed || got.Error != "nothing was recorded" {
		t.Fatalf("empty: %+v", got)
	}
	// only the microphone's WAV arrived (the media muxer held its data): kept, not failed
	rec3 := begin(t, r.s, model.RecordingScreen, true)
	mustAppend(t, r.s, rec3.ID, 1, TrackPCM, 0, pcm(3000))
	r.clk.add(OutageGrace + time.Second)
	r.s.Sweep()
	if got, _ := r.s.Get(rec3.ID); got.State != model.RecordingInterrupted || got.Segments[0].DurationMS != 3000 {
		t.Fatalf("pcm only: %+v", got)
	}
}

func TestLimits(t *testing.T) {
	r := newRig(t)
	r.cfg.MaxMinutes = 1
	rec := begin(t, r.s, model.RecordingAudio, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("a"))
	r.clk.add(61 * time.Second)
	if _, err := r.s.Append(rec.ID, 1, TrackMedia, 1, 0, []byte("b")); err != nil { // stored, then stopped
		t.Fatal(err)
	}
	got, _ := r.s.Get(rec.ID)
	if got.State != model.RecordingDone || !strings.Contains(got.Error, "longest recording length (1m0s") {
		t.Fatalf("too long: %+v", got)
	}
	// the sweep stops one that only sends heartbeats
	rec = begin(t, r.s, model.RecordingAudio, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("a"))
	for range 7 {
		r.clk.add(10 * time.Second)
		if _, err := r.s.SetState(rec.ID, "recording"); err != nil {
			t.Fatal(err)
		}
	}
	r.s.Sweep()
	if got, _ := r.s.Get(rec.ID); got.State != model.RecordingDone {
		t.Fatalf("sweep too long: %+v", got)
	}
	// the disk fills up
	r.cfg.MaxMinutes = 0
	rec = begin(t, r.s, model.RecordingAudio, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("a"))
	r.free = 100 << 20
	r.clk.add(diskEvery + time.Second)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 1, []byte("b"))
	if got, _ := r.s.Get(rec.ID); got.State != model.RecordingDone || !strings.Contains(got.Error, "disk is almost full") {
		t.Fatalf("disk full: %+v", got)
	}
	// a chunk is capped, and so is a segment
	r.free = 50 << 30
	rec = begin(t, r.s, model.RecordingAudio, true)
	if _, err := r.s.Append(rec.ID, 1, TrackMedia, 0, 0, make([]byte, MaxChunkBytes+1)); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("big chunk: %v", err)
	}
	if _, err := r.s.Append(rec.ID, 1, TrackMedia, MaxSegmentBytes, 0, nil); err != nil {
		t.Fatal(err) // a heartbeat at any offset is fine
	}
}

func TestDeleteAndRename(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingScreen, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("x"))
	if err := r.s.Delete(rec.ID); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("delete while live: %v", err)
	}
	if x, err := r.s.Rename(rec.ID, "  Standup   notes "); err != nil || x.Title != "Standup notes" {
		t.Fatalf("rename live: %+v %v", x, err)
	}
	r.s.SetState(rec.ID, "stopped")
	if x, err := r.s.Rename(rec.ID, "Demo"); err != nil || x.Title != "Demo" {
		t.Fatalf("rename: %+v %v", x, err)
	}
	if _, err := r.s.Rename(rec.ID, " "); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("empty title: %v", err)
	}
	if err := r.s.Delete(rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Fatalf("files left: %v", err)
	}
	if _, err := r.s.Get(rec.ID); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("get deleted: %v", err)
	}
	if ev := r.lastEvent(t); ev.Type != model.EvRecordingRemoved {
		t.Fatalf("event %s", ev.Type)
	}
	if err := r.s.Delete("../secrets"); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("path id: %v", err)
	}
}

type fakeTranscriber struct {
	mu   sync.Mutex
	auto bool
	err  error
	jobs []string
}

func (f *fakeTranscriber) Enqueue(source, wav, lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.jobs = append(f.jobs, source+" "+filepath.Base(wav)+" "+lang)
	return nil
}
func (f *fakeTranscriber) AutoTranscribe() bool { return f.auto }

// A part is transcribed as soon as the window moves on to the next one, not only after Stop
// (the owner: "chunks of 5 min"; the report is ready minutes after Stop).
func TestClosedSegmentTranscribedWhileRecording(t *testing.T) {
	r := newRig(t)
	ft := &fakeTranscriber{auto: true}
	r.s.SetTranscriber(ft)
	rec := begin(t, r.s, model.RecordingScreen, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("a"))
	mustAppend(t, r.s, rec.ID, 1, TrackPCM, 0, pcm(20))
	if len(ft.jobs) != 0 {
		t.Fatalf("the open part is not transcribed: %q", ft.jobs)
	}
	mustAppend(t, r.s, rec.ID, 2, TrackMedia, 0, []byte("b")) // the window rotated
	mustAppend(t, r.s, rec.ID, 2, TrackPCM, 0, pcm(20))
	mustAppend(t, r.s, rec.ID, 2, TrackMedia, 1, []byte("c"))
	if strings.Join(ft.jobs, "|") != "recording:"+rec.ID+"/1 seg-0001.wav " {
		t.Fatalf("part 1 queued once, while recording: %q", ft.jobs)
	}
	if x, _ := r.s.Get(rec.ID); x.State != model.RecordingActive {
		t.Fatalf("still recording: %s", x.State)
	}
	// a part without sound yet is not queued; nor is anything when transcription is not automatic
	ft.jobs = nil
	mustAppend(t, r.s, rec.ID, 3, TrackMedia, 0, []byte("d"))
	ft.auto = false
	mustAppend(t, r.s, rec.ID, 3, TrackPCM, 0, pcm(20))
	mustAppend(t, r.s, rec.ID, 4, TrackMedia, 0, []byte("e"))
	if strings.Join(ft.jobs, "|") != "recording:"+rec.ID+"/2 seg-0002.wav " {
		t.Fatalf("jobs: %q", ft.jobs)
	}
	// a refusal (voice not set up yet) never stops the recording
	ft.auto, ft.err = true, errors.New("voice is not set up")
	mustAppend(t, r.s, rec.ID, 4, TrackPCM, 0, pcm(20))
	mustAppend(t, r.s, rec.ID, 5, TrackMedia, 0, []byte("f"))
	if x, _ := r.s.Get(rec.ID); x.State != model.RecordingActive || len(x.Segments) != 5 {
		t.Fatalf("after a refusal: %s, %d parts", x.State, len(x.Segments))
	}
	// no microphone: nothing to queue
	r.s.SetState(rec.ID, "stopped")
	ft.err, ft.jobs = nil, nil
	rec2 := begin(t, r.s, model.RecordingWindow, false)
	mustAppend(t, r.s, rec2.ID, 1, TrackMedia, 0, []byte("a"))
	mustAppend(t, r.s, rec2.ID, 2, TrackMedia, 0, []byte("b"))
	if len(ft.jobs) != 0 {
		t.Fatalf("no sound, no jobs: %q", ft.jobs)
	}
}

func TestTranscription(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingAudio, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("a"))
	mustAppend(t, r.s, rec.ID, 1, TrackPCM, 0, pcm(20))
	mustAppend(t, r.s, rec.ID, 2, TrackMedia, 0, []byte("b"))
	mustAppend(t, r.s, rec.ID, 2, TrackPCM, 0, pcm(20))
	if _, err := r.s.Transcribe(rec.ID, ""); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("transcribe live: %v", err)
	}
	ft := &fakeTranscriber{auto: true}
	r.s.SetTranscriber(ft)
	r.s.SetState(rec.ID, "stopped")
	if strings.Join(ft.jobs, "|") != "recording:"+rec.ID+"/1 seg-0001.wav |recording:"+rec.ID+"/2 seg-0002.wav " {
		t.Fatalf("auto jobs: %q", ft.jobs)
	}
	ft.jobs = nil
	if n, err := r.s.Transcribe(rec.ID, "de"); err != nil || n != 2 || !strings.HasSuffix(ft.jobs[0], " de") {
		t.Fatalf("transcribe: %d %v %q", n, err, ft.jobs)
	}
	ft.err = errors.New("download a model first")
	var ve VoiceError
	if _, err := r.s.Transcribe(rec.ID, ""); !errors.As(err, &ve) || err.Error() != "download a model first" {
		t.Fatalf("voice error: %v", err)
	}
	r.s.SetTranscriber(nil)
	if _, err := r.s.Transcribe(rec.ID, ""); !errors.Is(err, ErrNoVoice) {
		t.Fatalf("no voice: %v", err)
	}
	// no microphone: nothing to transcribe
	r.s.SetTranscriber(&fakeTranscriber{})
	rec2 := begin(t, r.s, model.RecordingWindow, false)
	mustAppend(t, r.s, rec2.ID, 1, TrackMedia, 0, []byte("a"))
	r.s.SetState(rec2.ID, "stopped")
	if _, err := r.s.Transcribe(rec2.ID, ""); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("no mic: %v", err)
	}
}

func TestTickets(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingAudio, true)
	tk, exp, err := r.s.Ticket(rec.ID)
	if err != nil || exp.Sub(r.clk.now()) > TicketTTL {
		t.Fatalf("ticket: %v %v", exp, err)
	}
	if !r.s.CheckTicket(rec.ID, tk) {
		t.Fatal("fresh ticket refused")
	}
	other := begin(t, newRig(t).s, model.RecordingAudio, true)
	for _, bad := range []struct{ id, tk string }{{other.ID, tk}, {rec.ID, tk + "x"}, {rec.ID, ""}, {rec.ID, "zz"}, {rec.ID, "." + tk}} {
		if r.s.CheckTicket(bad.id, bad.tk) {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if New(Options{Store: r.st, Dir: r.dir, Now: r.clk.now}).CheckTicket(rec.ID, tk) {
		t.Fatal("a ticket outlived its daemon")
	}
	r.clk.add(TicketTTL + time.Second)
	if r.s.CheckTicket(rec.ID, tk) {
		t.Fatal("expired ticket accepted")
	}
	if _, _, err := r.s.Ticket("nope"); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("ticket for nothing: %v", err)
	}
}

func TestRestartContinuesTheLiveRecording(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingScreen, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("hello "))
	mustAppend(t, r.s, rec.ID, 1, TrackPCM, 0, pcm(250))
	r.s.Close()

	s2 := r.service() // the daemon starts again; the window keeps sending
	if err := s2.Start(); err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if a := s2.Active(); a == nil || a.ID != rec.ID || a.Bytes != 6 || a.Segments[0].DurationMS != 250 {
		t.Fatalf("reloaded: %+v", a)
	}
	if n := mustAppend(t, s2, rec.ID, 1, TrackMedia, 6, []byte("world")); n != 11 {
		t.Fatalf("size %d", n)
	}
	fin, _ := s2.SetState(rec.ID, "stopped")
	want := sha256.Sum256([]byte("hello world"))
	if fin.Segments[0].SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("sha after restart: %+v", fin.Segments[0])
	}
}

func TestFinishedHook(t *testing.T) {
	r := newRig(t)
	ft := &fakeTranscriber{auto: true}
	r.s.SetTranscriber(ft)
	var got []model.Recording
	r.s.SetFinished(func(rec model.Recording) { got = append(got, rec) })
	rec := begin(t, r.s, model.RecordingAudio, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("a"))
	mustAppend(t, r.s, rec.ID, 1, TrackPCM, 0, pcm(20))
	r.s.SetState(rec.ID, "stopped")
	if len(got) != 1 || got[0].ID != rec.ID || got[0].State != model.RecordingDone || got[0].Segments[0].DurationMS != 20 ||
		!filepath.IsAbs(got[0].Segments[0].WAV) {
		t.Fatalf("hook: %+v", got)
	}
	if len(ft.jobs) != 0 {
		t.Fatalf("with a hook the recorder queued transcriptions itself: %v", ft.jobs)
	}
	// stopping again, an interrupted and a failed recording: no hook
	r.s.SetState(rec.ID, "stopped")
	rec2 := begin(t, r.s, model.RecordingAudio, true)
	mustAppend(t, r.s, rec2.ID, 1, TrackPCM, 0, pcm(20))
	r.clk.add(OutageGrace + time.Second)
	r.s.Sweep()
	rec3 := begin(t, r.s, model.RecordingAudio, true)
	r.s.SetState(rec3.ID, "stopped") // nothing recorded: failed
	if len(got) != 1 || len(ft.jobs) != 0 {
		t.Fatalf("hook ran %d times; jobs %v", len(got), ft.jobs)
	}
	if g, _ := r.s.Get(rec2.ID); g.State != model.RecordingInterrupted {
		t.Fatalf("rec2 %s", g.State)
	}
}

// The owner, 2 Nov: "no time limit" - with max_minutes unset a recording goes on for a day and more, heartbeats and
// chunks alike; only the owner's Stop (or a full disk) ends it.
func TestNoTimeLimitByDefault(t *testing.T) {
	r := newRig(t)
	if got := r.s.Settings().MaxMinutes; got != 0 {
		t.Fatalf("default max_minutes = %d, want 0 (no limit)", got)
	}
	rec := begin(t, r.s, model.RecordingScreen, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("a"))
	for i := range 26 * 6 { // 26 hours, a heartbeat every 10 minutes
		r.clk.add(10 * time.Minute)
		if _, err := r.s.SetState(rec.ID, "recording"); err != nil {
			t.Fatal(err)
		}
		if i%6 == 0 {
			mustAppend(t, r.s, rec.ID, 1, TrackMedia, int64(1+i/6), []byte("b"))
		}
	}
	r.s.Sweep()
	if got, _ := r.s.Get(rec.ID); got.State != model.RecordingActive || got.Error != "" {
		t.Fatalf("stopped by itself: %+v", got)
	}
}

// tmx-lead, 3 Nov: a window's recording rides out an outage (the window queues and retries); a closed window
// does not block the next Record.
func TestOutageGraceAndTakeover(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingScreen, true)
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("a"))
	r.clk.add(5 * time.Minute) // the Wi-Fi is gone for 5 minutes
	r.s.Sweep()
	if r.s.Active() == nil {
		t.Fatal("interrupted during a 5-min outage")
	}
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 1, []byte("b")) // back: the queued chunks arrive
	if got, _ := r.s.Get(rec.ID); got.State != model.RecordingActive || got.Bytes != 2 {
		t.Fatalf("after the outage: %+v", got)
	}
	// the window is closed; 30 s later the owner taps Record again: the new one takes over
	r.clk.add(StaleAfter + 10*time.Second)
	rec2 := begin(t, r.s, model.RecordingScreen, true)
	if got, _ := r.s.Get(rec.ID); got.State != model.RecordingInterrupted || got.Bytes != 2 || !strings.Contains(got.Error, "was it closed") {
		t.Fatalf("old one: %+v", got)
	}
	// while one is live and sending, a second Record is still refused
	mustAppend(t, r.s, rec2.ID, 1, TrackMedia, 0, []byte("a"))
	if _, err := r.s.Begin(BeginRequest{Kind: model.RecordingAudio, Mic: true, Mime: "audio/webm;codecs=opus"}); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("second start: %v", err)
	}
}
