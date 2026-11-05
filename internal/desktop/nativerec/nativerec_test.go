package nativerec

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

// fakeCap stands in for ScreenCaptureKit: it writes MP4-shaped files (boxes, the index last
// when finished), delivers sound when the test says so, and has a frame once told.
type fakeCap struct {
	mu        sync.Mutex
	pickErr   error
	picked    Picked
	openErr   error
	micDenied bool
	pickCalls int

	cfg      StreamConfig
	pcm      func([]int16)
	stopped  func(error)
	path     string // the open segment's MP4
	size     int64  // what Progress says
	dur      time.Duration
	frame    []byte
	segments []string
	closes   int
	open     bool
}

func (f *fakeCap) Supported() (bool, string) { return true, "" }

func (f *fakeCap) Pick(ctx context.Context) (Picked, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pickCalls++
	if f.pickErr != nil {
		return Picked{}, f.pickErr
	}
	return f.picked, nil
}

func (f *fakeCap) Open(cfg StreamConfig, pcm func([]int16), stopped func(error)) (Opened, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return Opened{}, f.openErr
	}
	f.cfg, f.pcm, f.stopped, f.open = cfg, pcm, stopped, true
	if cfg.Mic && f.micDenied {
		return Opened{Mic: false, Warning: "The microphone isn't allowed for tmx"}, nil
	}
	return Opened{Mic: cfg.Mic}, nil
}

func fakeBox(typ string, n int) []byte {
	b := make([]byte, 8+n)
	binary.BigEndian.PutUint32(b, uint32(8+n))
	copy(b[4:], typ)
	return b
}

func (f *fakeCap) StartSegment(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return errors.New("the file exists")
	}
	f.path, f.size, f.dur = path, 0, 0
	f.segments = append(f.segments, path)
	return os.WriteFile(path, append(fakeBox("ftyp", 8), fakeBox("mdat", 100)...), 0o644)
}

func (f *fakeCap) Progress() (time.Duration, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dur, f.size
}

func (f *fakeCap) EndSegment() (time.Duration, error) {
	f.mu.Lock()
	path, dur, pcm := f.path, f.dur, f.pcm
	f.path = ""
	f.mu.Unlock()
	if path == "" {
		return 0, nil
	}
	if pcm != nil {
		pcm(make([]int16, 1600)) // the last 100 ms of sound
	}
	b, _ := os.ReadFile(path)
	return dur, os.WriteFile(path, append(b, fakeBox("moov", 20)...), 0o644)
}

func (f *fakeCap) Frame() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.frame == nil {
		return nil, ErrNoFrame
	}
	return f.frame, nil
}

func (f *fakeCap) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	f.open = false
}

func (f *fakeCap) set(fn func(f *fakeCap)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type rig struct {
	d   *apitest.Daemon
	cap *fakeCap
	clk *clock
	r   *Recorder

	mu    sync.Mutex
	notes []Status
}

func newRig(t *testing.T) *rig {
	t.Helper()
	g := &rig{d: apitest.New(t), clk: &clock{t: time.Date(2026, 10, 28, 20, 0, 0, 0, time.UTC)},
		cap: &fakeCap{picked: Picked{Kind: "display", Width: 3024, Height: 1964}, frame: []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3}}}
	g.r = New(Options{URL: g.d.URL, Token: g.d.OwnerToken, Capturer: g.cap, Now: g.clk.now,
		Notify: func(s Status) { g.mu.Lock(); g.notes = append(g.notes, s); g.mu.Unlock() }})
	return g
}

func (g *rig) lastNote(t *testing.T) Status {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.notes) == 0 {
		t.Fatal("no notifications")
	}
	return g.notes[len(g.notes)-1]
}

func (g *rig) daemonRec(t *testing.T, id string) model.Recording {
	t.Helper()
	r, err := g.d.Recorder.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (g *rig) start(t *testing.T, req StartRequest) model.Recording {
	t.Helper()
	res, err := g.r.Start(context.Background(), req)
	if err != nil || res.Recording == nil {
		t.Fatalf("start: %+v %v", res, err)
	}
	return *res.Recording
}

func wavSamples(t *testing.T, p string) int64 {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := binary.LittleEndian.Uint32(b[40:]); int64(n) != int64(len(b)-44) {
		t.Fatalf("%s: header says %d bytes, the file has %d", p, n, len(b)-44)
	}
	return int64(len(b)-44) / 2
}

func TestRecordRotatePauseStop(t *testing.T) {
	g := newRig(t)
	rec := g.start(t, StartRequest{Mic: true, SystemAudio: true, Title: "Demo"})
	if rec.Source != model.RecordingSourceNative || rec.Kind != model.RecordingScreen || !rec.Mic || !rec.SystemAudio || rec.Title != "Demo" {
		t.Fatalf("begun: %+v", rec)
	}
	if g.cap.cfg.FPS != 15 || g.cap.cfg.MaxSide != 1920 || !g.cap.cfg.SystemAudio || !g.cap.cfg.Mic {
		t.Fatalf("stream config: %+v", g.cap.cfg)
	}
	if st := g.r.Status(); st.Phase != PhaseRecording || st.ID != rec.ID {
		t.Fatalf("status: %+v", st)
	}
	dr := g.daemonRec(t, rec.ID)
	if len(dr.Segments) != 1 || !strings.HasSuffix(g.cap.segments[0], "seg-0001.mp4") || dr.Segments[0].File != g.cap.segments[0] {
		t.Fatalf("segment 1: %+v %v", dr.Segments, g.cap.segments)
	}
	if _, err := os.Stat(filepath.Join(rec.Dir, "frames", "f-000000000.jpg")); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	// sound arrives; 31 s later a frame is due
	g.cap.pcm(make([]int16, 16000))
	g.clk.add(31 * time.Second)
	g.cap.set(func(f *fakeCap) { f.dur = 31 * time.Second })
	g.r.tick()
	frame := filepath.Join(rec.Dir, "frames", "f-000031000.jpg")
	// Windows does not expose POSIX permission bits; still verify the file type.
	if st, err := os.Stat(frame); err != nil || !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("frame at 0:31: %v %v", st, err)
	}
	// the segment reaches its size: segment 2 starts
	g.cap.set(func(f *fakeCap) { f.size = 20 << 20 })
	g.r.tick()
	dr = g.daemonRec(t, rec.ID)
	if len(dr.Segments) != 2 || dr.Segments[0].SHA256 == "" || dr.Segments[0].Bytes == 0 || dr.Segments[1].SHA256 != "" {
		t.Fatalf("rotated: %+v", dr.Segments)
	}
	if n := wavSamples(t, dr.Segments[0].WAV); n != 17600 {
		t.Fatalf("segment 1 wav: %d samples", n)
	}
	// pause ends segment 2; resume starts segment 3
	g.clk.add(2 * time.Second)
	if err := g.r.Pause(); err != nil {
		t.Fatal(err)
	}
	dr = g.daemonRec(t, rec.ID)
	if dr.State != model.RecordingPaused || dr.Segments[1].SHA256 == "" || g.r.Status().Phase != PhasePaused {
		t.Fatalf("paused: %+v %+v", dr, g.r.Status())
	}
	g.clk.add(time.Minute)
	g.r.tick() // a heartbeat while paused
	if err := g.r.Resume(); err != nil {
		t.Fatal(err)
	}
	if dr = g.daemonRec(t, rec.ID); dr.State != model.RecordingActive || len(dr.Segments) != 3 || len(g.cap.segments) != 3 {
		t.Fatalf("resumed: %+v", dr)
	}
	g.cap.pcm(make([]int16, 8000))
	g.clk.add(3 * time.Second)
	fin, err := g.r.Stop()
	if err != nil || fin == nil || fin.State != model.RecordingDone || len(fin.Segments) != 3 || fin.Error != "" {
		t.Fatalf("stopped: %+v %v", fin, err)
	}
	for _, s := range fin.Segments {
		if s.SHA256 == "" || s.Bytes == 0 {
			t.Fatalf("segment %d not finished: %+v", s.N, s)
		}
		if st, err := os.Stat(s.File); err != nil || !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
			t.Fatalf("segment %d: %v %v", s.N, st, err)
		}
	}
	if fin.DurationMS != (17600+1600+9600)/16 { // from the WAVs: the sound of all three parts
		t.Fatalf("duration %d", fin.DurationMS)
	}
	// frames: the first, 0:31, and one at stop (31 s + 2 s + 3 s of recorded time)
	if _, err := os.Stat(filepath.Join(rec.Dir, "frames", "f-000036000.jpg")); err != nil {
		t.Fatalf("frame at stop: %v", err)
	}
	if n := g.lastNote(t); n.Phase != PhaseIdle || n.Ended == nil || n.Ended.Recording == nil || n.Ended.Error != "" {
		t.Fatalf("ended note: %+v", n)
	}
	if g.cap.open || g.r.Status().Phase != PhaseIdle {
		t.Fatal("still capturing")
	}
}

func TestFollowsPauseAndStopFromAnotherWindow(t *testing.T) {
	g := newRig(t)
	rec := g.start(t, StartRequest{SystemAudio: true})
	other := func(state string) {
		if _, err := g.d.Recorder.SetState(rec.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	other("paused")
	g.r.Sync() // the page saw recording.updated
	if g.r.Status().Phase != PhasePaused || g.daemonRec(t, rec.ID).Segments[0].SHA256 == "" {
		t.Fatalf("did not follow the pause: %+v", g.r.Status())
	}
	other("recording")
	g.clk.add(6 * time.Second) // the heartbeat finds it too
	g.r.tick()
	g.r.tick()
	if g.r.Status().Phase != PhaseRecording || len(g.daemonRec(t, rec.ID).Segments) != 2 {
		t.Fatalf("did not follow the resume: %+v", g.r.Status())
	}
	other("stopped")
	if dr := g.daemonRec(t, rec.ID); !dr.Stopping || !dr.State.Live() {
		t.Fatalf("the daemon must wait for the app: %+v", dr)
	}
	g.r.Sync()
	for i := 0; i < 3 && g.r.Status().Phase != PhaseIdle; i++ {
		g.r.tick()
	}
	dr := g.daemonRec(t, rec.ID)
	if dr.State != model.RecordingDone || dr.Segments[1].SHA256 == "" || g.r.Status().Phase != PhaseIdle {
		t.Fatalf("did not follow the stop: %+v %+v", dr, g.r.Status())
	}
	if n := g.lastNote(t); n.Ended == nil || n.Ended.Error != "" || n.Ended.Recording == nil {
		t.Fatalf("ended note: %+v", n)
	}
}

func TestStreamStoppedByTheSystem(t *testing.T) {
	g := newRig(t)
	rec := g.start(t, StartRequest{Mic: true})
	g.cap.stopped(errors.New("the display was disconnected"))
	deadline := time.Now().Add(5 * time.Second)
	for g.r.Status().Phase != PhaseIdle && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	dr := g.daemonRec(t, rec.ID)
	if dr.State != model.RecordingDone || !strings.Contains(dr.Error, "display was disconnected") || dr.Segments[0].SHA256 == "" {
		t.Fatalf("finished: %+v", dr)
	}
}

func TestStartRefusalsLeaveNoRecording(t *testing.T) {
	g := newRig(t)
	g.cap.pickErr = ErrCancelled
	if res, err := g.r.Start(context.Background(), StartRequest{Mic: true}); err != nil || !res.Cancelled {
		t.Fatalf("cancelled: %+v %v", res, err)
	}
	g.cap.pickErr = nil
	g.cap.openErr = errors.New("Screen recording isn't allowed for tmx: System Settings → Privacy & Security → Screen & System Audio Recording")
	if _, err := g.r.Start(context.Background(), StartRequest{Mic: true}); err == nil || !strings.Contains(err.Error(), "Privacy & Security") {
		t.Fatalf("denied: %v", err)
	}
	if list, _ := g.d.Recorder.List(0); len(list) != 0 || g.r.Status().Phase != PhaseIdle {
		t.Fatalf("a recording was left: %+v %+v", list, g.r.Status())
	}
	// the microphone is not allowed: the screen and device audio are recorded, with a warning
	g.cap.openErr, g.cap.micDenied = nil, true
	res, err := g.r.Start(context.Background(), StartRequest{Mic: true, SystemAudio: true})
	if err != nil || res.Recording.Mic || !res.Recording.SystemAudio || !strings.Contains(res.Warning, "microphone") {
		t.Fatalf("mic denied: %+v %v", res, err)
	}
	// another recording is live: the picker does not even show
	calls := g.cap.pickCalls
	r2 := New(Options{URL: g.d.URL, Token: g.d.OwnerToken, Capturer: &fakeCap{}})
	if _, err := r2.Start(context.Background(), StartRequest{}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second: %v", err)
	}
	if g.cap.pickCalls != calls {
		t.Fatal("the picker showed")
	}
	if _, err := g.r.Start(context.Background(), StartRequest{}); err == nil {
		t.Fatal("two captures in one window")
	}
	g.r.Stop()
}

func TestRecordingEndedOnTheDaemon(t *testing.T) {
	g := newRig(t)
	rec := g.start(t, StartRequest{SystemAudio: true})
	// the daemon finished it (as the sweep would after a long silence from this window)
	if _, err := g.d.Recorder.Native(rec.ID, recorder.NativeReport{Last: true}); err != nil {
		t.Fatal(err)
	}
	g.clk.add(6 * time.Second)
	g.r.tick()
	if g.r.Status().Phase != PhaseIdle || g.cap.open {
		t.Fatalf("still capturing: %+v", g.r.Status())
	}
	if n := g.lastNote(t); n.Ended == nil || !strings.Contains(n.Ended.Error, "ended on the daemon") {
		t.Fatalf("ended note: %+v", n)
	}
}

func TestDaemonAwayForAWhile(t *testing.T) {
	g := newRig(t)
	rec := g.start(t, StartRequest{SystemAudio: true})
	// the daemon restarts (an update's handover): reports wait in order and arrive after
	real := g.r.api.url
	g.r.api.url = "http://127.0.0.1:1"
	g.cap.set(func(f *fakeCap) { f.size = 30 << 20 })
	g.clk.add(10 * time.Second)
	g.r.tick() // rotates; the close and open reports cannot be sent
	if len(g.r.sess.queue) != 2 || g.r.Status().Phase != PhaseRecording {
		t.Fatalf("queue: %+v", g.r.sess.queue)
	}
	g.r.api.url = real
	g.r.tick()
	if dr := g.daemonRec(t, rec.ID); len(dr.Segments) != 2 || dr.Segments[0].SHA256 == "" || len(g.r.sess.queue) != 0 {
		t.Fatalf("after the daemon came back: %+v", dr.Segments)
	}
	// away for good: the capture ends after a while, the files stay
	g.r.api.url = "http://127.0.0.1:1"
	g.clk.add(6 * time.Second)
	g.r.tick()
	g.clk.add(giveUpAfter + time.Second)
	g.r.tick()
	if g.r.Status().Phase != PhaseIdle || g.cap.open {
		t.Fatalf("still capturing: %+v", g.r.Status())
	}
	if n := g.lastNote(t); n.Ended == nil || !strings.Contains(n.Ended.Error, "stopped answering") {
		t.Fatalf("ended note: %+v", n)
	}
}

func TestBridgeJSON(t *testing.T) {
	// what the page gets through the bridge: field names it reads
	b, _ := json.Marshal(Status{Supported: true, Phase: PhaseRecording, ID: "abc", Ended: &Ended{Error: "x"}})
	for _, want := range []string{`"supported":true`, `"phase":"recording"`, `"id":"abc"`, `"ended":{"error":"x"}`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("%s lacks %s", b, want)
		}
	}
	var req StartRequest
	if err := json.Unmarshal([]byte(`{"mic":true,"system_audio":true,"title":"T"}`), &req); err != nil || !req.Mic || !req.SystemAudio || req.Title != "T" {
		t.Fatalf("start request: %+v %v", req, err)
	}
}
