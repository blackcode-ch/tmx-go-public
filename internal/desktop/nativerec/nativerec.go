// Package nativerec records the screen natively in the macOS app (DESIGN.md "Recorder", native
// capture). The WebView's getDisplayMedia cannot capture what the Mac plays; ScreenCaptureKit
// can. In the window process it records a display, a window or an app the owner picks in the
// system picker, with device audio and the microphone.
//
// The capture itself is a Capturer (capture_darwin.m on macOS 15+, a fake in tests). This
// package drives it and keeps the daemon told. It begins the recording through the API (source
// native) and writes the 16 kHz WAV and the frames next to the MP4 the capturer writes. It
// rotates segments at the recorder's settings and ends a segment on pause. Every segment, frame
// and heartbeat goes to POST /api/recordings/{id}/native.
//
// The daemon owns the recording's state. A pause or stop made in another window comes back in
// its answers and is followed here.
package nativerec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

var (
	// ErrCancelled: the owner closed the picker without choosing.
	ErrCancelled = errors.New("the picker was closed")
	// ErrNoFrame: the capture has no frame yet.
	ErrNoFrame = errors.New("no frame yet")
)

// Picked is what the owner chose in the system picker.
type Picked struct {
	Kind          string // display | window | application
	Width, Height int    // its size in pixels
}

// StreamConfig is what the capture records.
type StreamConfig struct {
	FPS         int
	MaxSide     int // the video's longest side in pixels
	SystemAudio bool
	Mic         bool
}

// Opened says what the capture actually records.
type Opened struct {
	Mic     bool   // false when the microphone is not allowed (Warning says so)
	Warning string // shown to the owner; the recording goes on
}

// Capturer is the platform's screen capture. Calls come one at a time; pcm and stopped are
// called from the capture's own threads.
type Capturer interface {
	// Supported says whether this machine can capture natively, and why not.
	Supported() (bool, string)
	// Pick shows the system picker and waits for the owner's choice (ErrCancelled).
	Pick(ctx context.Context) (Picked, error)
	// Open starts the stream on the picked content. pcm receives the open segment's sound,
	// mixed to 16 kHz mono; stopped is called once when the stream ends by itself.
	Open(cfg StreamConfig, pcm func([]int16), stopped func(error)) (Opened, error)
	// StartSegment starts writing an MP4 at path, which must not exist.
	StartSegment(path string) error
	// Progress is the open segment's recorded time and bytes.
	Progress() (time.Duration, int64)
	// EndSegment finishes the open segment's MP4 and delivers its last sound; it returns the
	// segment's recorded time.
	EndSegment() (time.Duration, error)
	// Frame is the newest screen frame as a JPEG (longest side ≤ 1280 px, ≤ 600 KB; ErrNoFrame).
	Frame() ([]byte, error)
	// Close stops the stream and lets go of the picked content.
	Close()
}

// Phases of the window's native capture.
const (
	PhaseIdle      = "idle"
	PhasePicking   = "picking" // the system picker is open
	PhaseStarting  = "starting"
	PhaseRecording = "recording"
	PhasePaused    = "paused"
	PhaseStopping  = "stopping"
)

const (
	maxFrames     = 400
	maxFrameBytes = 600 << 10
	maxSide       = 1920            // the video's longest side; screen text stays sharp, files stay small
	giveUpAfter   = 2 * time.Minute // without the daemon, the capture ends (the files are kept)
)

type Options struct {
	URL, Token string // the daemon and the owner token
	Capturer   Capturer
	Client     *http.Client
	// Notify gets every change (the app sends it to the page as the Wails event "tmx:native-rec").
	Notify func(Status)
	Logger *slog.Logger
	Now    func() time.Time
	// Tick runs rotation, frames and reports (250 ms); 0 in tests: they call tick themselves.
	Tick       time.Duration
	Heartbeat  time.Duration // 5 s
	FrameEvery time.Duration // 30 s
}

// StartRequest is the owner's choice in the start dialog.
type StartRequest struct {
	Mic         bool   `json:"mic"`
	SystemAudio bool   `json:"system_audio"`
	Title       string `json:"title,omitempty"`
}

// StartResult: the recording that began, or Cancelled when the picker was closed.
type StartResult struct {
	Recording *model.Recording `json:"recording,omitempty"`
	Cancelled bool             `json:"cancelled,omitempty"`
	Warning   string           `json:"warning,omitempty"`
}

// Status is the window's native capture as the page shows it.
type Status struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"` // why not
	Phase     string `json:"phase"`
	ID        string `json:"id,omitempty"` // the recording being captured
	Warning   string `json:"warning,omitempty"`
	// Ended is set once, in the notification of a capture that ended.
	Ended *Ended `json:"ended,omitempty"`
}

// Ended is how a capture ended: the finished recording, or why it stopped without one.
type Ended struct {
	Recording *model.Recording `json:"recording,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// Recorder is the window's native capture: one recording at a time.
type Recorder struct {
	o   Options
	api *daemon

	op sync.Mutex // one operation on the capture at a time

	mu      sync.Mutex
	phase   string
	sess    *session
	cancel  context.CancelFunc // the picker's wait
	warning string

	pcmMu  sync.Mutex
	wav    *wavFile // the open segment's WAV
	pcmErr error
}

// session is the recording being captured.
type session struct {
	id, dir   string
	rec       model.Recording // the daemon's latest answer
	settings  config.RecorderConfig
	audio     bool      // a WAV per segment (the microphone or device audio)
	seg       int       // the open segment; 0 while paused
	last      int       // the highest segment number used
	segAt     time.Time // when the open segment started
	doneMS    int64     // recorded ms of the closed segments
	frames    int
	nextFrame int64   // recorded ms at which the next frame is due
	newFrames []int64 // written, not yet reported
	queue     []recorder.NativeReport
	beatAt    time.Time // the last answer from the daemon
	failAt    time.Time // the daemon stopped answering; zero while it answers
	paused    bool
	stopping  bool
	done      chan struct{}
}

func New(o Options) *Recorder {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = 5 * time.Second
	}
	if o.FrameEvery <= 0 {
		o.FrameEvery = 30 * time.Second
	}
	if o.Notify == nil {
		o.Notify = func(Status) {}
	}
	return &Recorder{o: o, api: &daemon{url: strings.TrimRight(o.URL, "/"), token: o.Token, c: o.Client}, phase: PhaseIdle}
}

// Status returns the capture's state now.
func (r *Recorder) Status() Status {
	ok, why := r.o.Capturer.Supported()
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{Supported: ok, Reason: why, Phase: r.phase, Warning: r.warning}
	if r.sess != nil {
		st.ID = r.sess.id
	}
	return st
}

func (r *Recorder) setPhase(p string) {
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
	r.o.Notify(r.Status())
}

func (r *Recorder) warn(w string) {
	r.mu.Lock()
	r.warning = w
	r.mu.Unlock()
	r.o.Notify(r.Status())
}

// Start records what the owner picks: the system picker first, then the capture, then the
// recording on the daemon. Only the owner's click in the window calls it.
func (r *Recorder) Start(ctx context.Context, req StartRequest) (StartResult, error) {
	if ok, why := r.o.Capturer.Supported(); !ok {
		return StartResult{}, errors.New(why)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	if r.phase != PhaseIdle {
		r.mu.Unlock()
		return StartResult{}, errors.New("a recording is already running in this window")
	}
	r.phase, r.cancel, r.warning = PhasePicking, cancel, ""
	r.mu.Unlock()
	r.o.Notify(r.Status())
	started := false
	defer func() {
		r.mu.Lock()
		r.cancel = nil
		if !started {
			r.phase = PhaseIdle
		}
		r.mu.Unlock()
		if !started {
			r.o.Notify(r.Status())
		}
	}()

	// Ask the daemon first, so the picker never shows for a recording that cannot begin.
	act, err := r.api.active()
	if err != nil {
		return StartResult{}, err
	}
	if act.Recording != nil && act.Recording.State.Live() {
		return StartResult{}, fmt.Errorf("a recording is already running on this machine (%s)", act.Recording.Title)
	}
	picked, err := r.o.Capturer.Pick(ctx)
	if errors.Is(err, ErrCancelled) {
		return StartResult{Cancelled: true}, nil
	}
	if err != nil {
		return StartResult{}, err
	}
	r.setPhase(PhaseStarting)

	r.op.Lock()
	defer r.op.Unlock()
	set := act.Settings
	opened, err := r.o.Capturer.Open(StreamConfig{FPS: set.FPS, MaxSide: maxSide, SystemAudio: req.SystemAudio, Mic: req.Mic}, r.onPCM, r.onStopped)
	if err != nil {
		r.o.Capturer.Close()
		return StartResult{}, err
	}
	kind := model.RecordingWindow // a window, or an app's windows
	if picked.Kind == "display" {
		kind = model.RecordingScreen
	}
	mime := recorder.NativeMime
	if !opened.Mic && !req.SystemAudio {
		mime = "video/mp4;codecs=avc1" // no sound at all
	}
	rec, err := r.api.begin(recorder.BeginRequest{Kind: kind, Mic: opened.Mic, SystemAudio: req.SystemAudio, Mime: mime,
		Title: req.Title, Source: model.RecordingSourceNative})
	if err != nil {
		r.o.Capturer.Close()
		return StartResult{}, err
	}
	if st, err := os.Stat(rec.Dir); err != nil || !st.IsDir() {
		r.o.Capturer.Close()
		const why = "the daemon's recordings folder is not on this machine"
		_, _ = r.api.native(rec.ID, recorder.NativeReport{Last: true, Error: why})
		return StartResult{}, errors.New(why)
	}
	now := r.o.Now()
	s := &session{id: rec.ID, dir: rec.Dir, rec: rec, settings: set, audio: opened.Mic || req.SystemAudio, beatAt: now,
		done: make(chan struct{})}
	r.mu.Lock()
	r.sess, r.warning = s, opened.Warning
	r.mu.Unlock()
	if err := r.startSegmentLocked(1); err != nil {
		r.endLocked(nil, "")
		_, _ = r.api.native(rec.ID, recorder.NativeReport{Last: true, Error: err.Error()}) // nothing recorded: failed
		return StartResult{}, err
	}
	r.frameLocked(true) // the first frame, when the capture has one already
	r.sendLocked(true)
	started = true
	r.setPhase(PhaseRecording)
	if r.o.Tick > 0 {
		go r.loop(s)
	}
	v := s.rec
	return StartResult{Recording: &v, Warning: opened.Warning}, nil
}

// Pause pauses the recording on the daemon and ends the open segment.
func (r *Recorder) Pause() error {
	r.op.Lock()
	defer r.op.Unlock()
	s := r.sess
	if s == nil || s.stopping || s.paused {
		return nil
	}
	rec, err := r.api.state(s.id, "paused")
	if err != nil {
		return err
	}
	if r.answerLocked(rec) {
		return nil
	}
	r.followLocked()
	r.sendLocked(false)
	return nil
}

// Resume resumes the recording on the daemon and starts the next segment.
func (r *Recorder) Resume() error {
	r.op.Lock()
	defer r.op.Unlock()
	s := r.sess
	if s == nil || s.stopping || !s.paused {
		return nil
	}
	rec, err := r.api.state(s.id, "recording")
	if err != nil {
		return err
	}
	if r.answerLocked(rec) {
		return nil
	}
	r.followLocked()
	r.sendLocked(false)
	return nil
}

// Stop ends the capture, finishes the last segment and the recording, and returns it.
func (r *Recorder) Stop() (*model.Recording, error) {
	return r.stop(15 * time.Second)
}

func (r *Recorder) stop(wait time.Duration) (*model.Recording, error) {
	r.mu.Lock()
	if r.cancel != nil { // the picker is open: give up waiting for it
		r.cancel()
	}
	r.mu.Unlock()
	r.op.Lock()
	defer r.op.Unlock()
	s := r.sess
	if s == nil {
		return nil, nil
	}
	if !s.stopping {
		r.stopCaptureLocked("")
	}
	return r.finishLocked(s, wait)
}

// Sync asks the daemon now and follows its answer (the page calls it on a recording.updated
// event that differs from the capture: a pause or stop in another window).
func (r *Recorder) Sync() {
	r.op.Lock()
	defer r.op.Unlock()
	if r.sess != nil {
		r.sendLocked(true)
		r.followLocked()
		if r.sess != nil {
			r.sendLocked(false)
		}
	}
}

// Close ends a capture when the window closes: the recording is finished, not interrupted.
func (r *Recorder) Close() {
	if _, err := r.stop(5 * time.Second); err != nil {
		r.o.Logger.Warn("native recording: stop at close", "err", err)
	}
}

func (r *Recorder) loop(s *session) {
	t := time.NewTicker(r.o.Tick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			r.op.Lock()
			if r.sess == s {
				r.tick()
			}
			r.op.Unlock()
		}
	}
}

// tick (under op): frames, rotation, the reports and the daemon's answer.
func (r *Recorder) tick() {
	s := r.sess
	if s == nil {
		return
	}
	r.pcmMu.Lock()
	perr := r.pcmErr
	r.pcmErr = nil
	r.pcmMu.Unlock()
	if perr != nil {
		r.warn("The sound for transcription is not being saved: " + perr.Error())
	}
	if s.seg > 0 && !s.stopping {
		r.frameLocked(false)
		if dur, size := r.o.Capturer.Progress(); size >= segmentLimit(s.settings) || dur >= time.Duration(s.settings.SegmentMinutes)*time.Minute {
			r.rotateLocked()
		}
	}
	if r.sess == nil {
		return
	}
	r.sendLocked(false)
	r.followLocked()
}

// segmentLimit is the size at which a segment ends: segment_mb with room for the MP4's index
// and the last second.
func segmentLimit(c config.RecorderConfig) int64 {
	return max(2<<20, int64(c.SegmentMB)<<20-1<<20)
}

func (r *Recorder) recordedMS(now time.Time) int64 {
	s := r.sess
	if s.seg == 0 {
		return s.doneMS
	}
	return s.doneMS + now.Sub(s.segAt).Milliseconds()
}

// startSegmentLocked opens segment n: its WAV, then the capture's MP4; the daemon is told.
func (r *Recorder) startSegmentLocked(n int) error {
	s := r.sess
	media, wav := recorder.NativeFiles(n)
	mp4 := filepath.Join(s.dir, media)
	var w *wavFile
	if s.audio {
		var err error
		if w, err = createWAV(filepath.Join(s.dir, wav)); err != nil {
			return fmt.Errorf("could not create the sound file: %w", err)
		}
	}
	r.pcmMu.Lock()
	r.wav = w
	r.pcmMu.Unlock()
	if err := r.o.Capturer.StartSegment(mp4); err != nil {
		r.closeWAV()
		return err
	}
	_ = os.Chmod(mp4, 0o600)
	s.seg, s.last, s.segAt = n, n, r.o.Now()
	s.queue = append(s.queue, recorder.NativeReport{Segment: n})
	return nil
}

// endSegmentLocked finishes the open segment (MP4 and WAV) and queues its report.
func (r *Recorder) endSegmentLocked() error {
	s := r.sess
	if s.seg == 0 {
		return nil
	}
	dur, err := r.o.Capturer.EndSegment()
	r.closeWAV()
	wall := r.o.Now().Sub(s.segAt)
	if dur <= 0 {
		dur = wall
	}
	media, _ := recorder.NativeFiles(s.seg)
	_ = os.Chmod(filepath.Join(s.dir, media), 0o600)
	s.doneMS += wall.Milliseconds()
	s.queue = append(s.queue, recorder.NativeReport{Segment: s.seg, DurationMS: dur.Milliseconds(), Closed: true})
	s.seg = 0
	return err
}

func (r *Recorder) closeWAV() {
	r.pcmMu.Lock()
	w := r.wav
	r.wav = nil
	r.pcmMu.Unlock()
	if w != nil {
		if err := w.close(); err != nil {
			r.o.Logger.Warn("native recording: close wav", "err", err)
		}
	}
}

// rotateLocked starts the next segment at the size or time limit.
func (r *Recorder) rotateLocked() {
	s := r.sess
	next := s.seg + 1
	if err := r.endSegmentLocked(); err != nil {
		r.stopCaptureLocked("the recording could not finish part " + fmt.Sprint(next-1) + ": " + err.Error())
		return
	}
	if err := r.startSegmentLocked(next); err != nil {
		r.stopCaptureLocked("the recording could not start part " + fmt.Sprint(next) + ": " + err.Error())
	}
}

// frameLocked writes frames/f-<ms>.jpg when one is due (at the start, then every FrameEvery
// of recorded time), or now (at stop).
func (r *Recorder) frameLocked(now bool) {
	s := r.sess
	if s.seg == 0 || s.frames >= maxFrames {
		return
	}
	ms := r.recordedMS(r.o.Now())
	if !now && ms < s.nextFrame {
		return
	}
	jpg, err := r.o.Capturer.Frame()
	if errors.Is(err, ErrNoFrame) {
		return // the next tick tries again
	}
	every := r.o.FrameEvery.Milliseconds()
	s.nextFrame = (ms/every + 1) * every
	if err == nil && len(jpg) > maxFrameBytes {
		err = fmt.Errorf("the frame is %d KB", len(jpg)>>10)
	}
	if err == nil {
		err = writePrivate(filepath.Join(s.dir, recorder.NativeFrameFile(ms)), jpg)
	}
	if err != nil {
		r.o.Logger.Warn("native recording: frame", "ms", ms, "err", err)
		return
	}
	s.frames++
	s.newFrames = append(s.newFrames, ms)
}

// stopCaptureLocked ends the capture: a last frame, the last segment, the stream; the last
// report (with why, when the capture ended by itself) is queued.
func (r *Recorder) stopCaptureLocked(why string) {
	s := r.sess
	s.stopping = true
	r.setPhase(PhaseStopping)
	r.frameLocked(true)
	if err := r.endSegmentLocked(); err != nil && why == "" {
		why = "the last part could not be finished: " + err.Error()
	}
	r.o.Capturer.Close()
	s.queue = append(s.queue, recorder.NativeReport{Last: true, Error: why})
}

// finishLocked delivers what is left until the daemon has finished the recording.
func (r *Recorder) finishLocked(s *session, wait time.Duration) (*model.Recording, error) {
	deadline := time.Now().Add(wait)
	for r.sess == s {
		r.sendLocked(false)
		if r.sess != s {
			break
		}
		if time.Now().After(deadline) {
			msg := "the daemon didn't answer, so the recording may show as interrupted; its files are kept in " + s.dir
			r.endLocked(nil, msg)
			return nil, errors.New(msg)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !s.rec.State.Live() {
		v := s.rec
		return &v, nil
	}
	return nil, errors.New("the recording ended")
}

// sendLocked delivers the queued reports in order, then a progress report when one is due
// (new frames, the heartbeat) or force is set. Reports that could not be delivered stay queued.
func (r *Recorder) sendLocked(force bool) {
	s := r.sess
	for len(s.queue) > 0 {
		rep := s.queue[0]
		if rep.Last { // the frames written since the last report go with it
			rep.Frames = s.newFrames
		}
		rec, err := r.api.native(s.id, rep)
		if err != nil {
			r.failedLocked(err)
			return
		}
		s.queue = s.queue[1:]
		if rep.Last {
			s.newFrames = nil
		}
		if r.answerLocked(rec) {
			return
		}
	}
	if s.stopping || !(force || len(s.newFrames) > 0 || r.o.Now().Sub(s.beatAt) >= r.o.Heartbeat) {
		return
	}
	rep := recorder.NativeReport{Segment: s.seg, Frames: s.newFrames}
	if s.seg > 0 {
		dur, _ := r.o.Capturer.Progress()
		if dur <= 0 {
			dur = r.o.Now().Sub(s.segAt)
		}
		rep.DurationMS = dur.Milliseconds()
	}
	rec, err := r.api.native(s.id, rep)
	if err != nil {
		r.failedLocked(err)
		return
	}
	s.newFrames = nil
	r.answerLocked(rec)
}

// answerLocked takes the daemon's answer; it reports true when the recording ended there.
func (r *Recorder) answerLocked(rec model.Recording) bool {
	s := r.sess
	s.rec, s.beatAt, s.failAt = rec, r.o.Now(), time.Time{}
	if rec.State.Live() {
		return false
	}
	why := ""
	if !s.stopping {
		why = "the recording ended on the daemon"
		if rec.Error != "" {
			why += ": " + rec.Error
		}
	}
	r.endLocked(&rec, why)
	return true
}

// failedLocked handles a report the daemon refused or never got.
func (r *Recorder) failedLocked(err error) {
	s := r.sess
	var he *httpError
	if errors.As(err, &he) && (he.status == http.StatusNotFound || he.status == http.StatusConflict) {
		r.endLocked(nil, "the recording ended on the daemon: "+he.msg)
		return
	}
	now := r.o.Now()
	if s.failAt.IsZero() {
		s.failAt = now
		r.o.Logger.Info("native recording: the daemon is not answering", "err", err)
		return
	}
	if now.Sub(s.failAt) > giveUpAfter {
		if !s.stopping {
			r.stopCaptureLocked("")
		}
		r.endLocked(nil, "the daemon stopped answering, so the recording ended; its files are kept in "+s.dir)
	}
}

// followLocked follows the daemon's state: a pause, resume or stop made in another window.
func (r *Recorder) followLocked() {
	s := r.sess
	if s == nil || s.stopping {
		return
	}
	switch {
	case s.rec.Stopping:
		r.stopCaptureLocked("")
	case s.rec.State == model.RecordingPaused && !s.paused:
		if err := r.endSegmentLocked(); err != nil {
			r.o.Logger.Warn("native recording: pause", "err", err)
		}
		s.paused = true
		r.setPhase(PhasePaused)
	case s.rec.State == model.RecordingActive && s.paused:
		s.paused = false
		if err := r.startSegmentLocked(s.last + 1); err != nil {
			r.stopCaptureLocked("the recording could not go on after the pause: " + err.Error())
			return
		}
		r.setPhase(PhaseRecording)
	}
}

// endLocked lets go of the session: the capture stops, the page is told.
func (r *Recorder) endLocked(rec *model.Recording, why string) {
	s := r.sess
	if s == nil {
		return
	}
	r.o.Capturer.Close()
	r.closeWAV()
	r.mu.Lock()
	r.sess, r.phase, r.warning = nil, PhaseIdle, ""
	r.mu.Unlock()
	close(s.done)
	st := r.Status()
	st.Ended = &Ended{Recording: rec, Error: why}
	r.o.Notify(st)
}

// onPCM writes the open segment's sound (the capture's thread).
func (r *Recorder) onPCM(samples []int16) {
	r.pcmMu.Lock()
	defer r.pcmMu.Unlock()
	if r.wav == nil {
		return
	}
	if err := r.wav.write(samples); err != nil && r.pcmErr == nil {
		r.pcmErr = err
	}
}

// onStopped: the stream ended by itself (Stop in the menu bar, the display went away, a failure).
func (r *Recorder) onStopped(err error) {
	go func() {
		r.op.Lock()
		defer r.op.Unlock()
		s := r.sess
		if s == nil || s.stopping {
			return
		}
		why := ""
		if err != nil {
			why = "the capture stopped: " + err.Error()
		}
		r.stopCaptureLocked(why)
		_, _ = r.finishLocked(s, 15*time.Second)
	}()
}

// writePrivate writes a file 0600 (its folder 0700) through a temporary name.
func writePrivate(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
