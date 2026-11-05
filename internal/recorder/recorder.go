// Package recorder keeps the owner's recordings of their own screen (whole or one window),
// camera, microphone and device audio (DESIGN.md "Recorder"). The owner's window captures
// (getDisplayMedia, getUserMedia) and streams chunks of about a second to owner-only routes;
// this package appends them to segment files, keeps the still frames for summaries, tracks the
// session's state, marks a session interrupted when its chunks stop (the window closed), and
// finishes it with durations and checksums.
//
// The daemon never captures anything. A recording begins only through Begin, which only the
// owner's API route calls after a click in the owner's window; no CLI command or MCP tool can
// start one. Agents may read finished recordings (the owner's decision): List and Get name
// the files by their absolute paths.
package recorder

import (
	"crypto/rand"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// Track is one of a segment's two files.
type Track string

const (
	TrackMedia Track = "media" // what MediaRecorder produced (mp4 or webm)
	TrackPCM   Track = "pcm"   // 16 kHz mono PCM16 of the microphone and device audio, kept as a WAV
)

const (
	// StaleAfter without chunks or heartbeats marks a live recording interrupted.
	StaleAfter = 20 * time.Second
	// OutageGrace is how long a browser window's recording may go without chunks or heartbeats before it is
	// marked interrupted (tmx-lead, 3 Nov: parts must survive an outage). The window keeps every chunk queued
	// and retries for as long (recorder.ts MAX_BACKLOG_MS), so a dropped Wi-Fi or a daemon restart (an update)
	// loses nothing; a closed window shows as interrupted after it. The native app keeps StaleAfter.
	OutageGrace = 10 * time.Minute
	sweepEvery  = 5 * time.Second
	// MaxSegmentBytes caps one file; the window starts a new segment at about 20 MB.
	MaxSegmentBytes = 48 << 20
	// MaxChunkBytes caps one upload (the window sends about a second at a time).
	MaxChunkBytes = 8 << 20
	maxSegments   = 9999
	minFreeStart  = 1 << 30   // free disk needed to begin
	minFreeKeep   = 256 << 20 // a recording stops when less is left
	diskEvery     = 10 * time.Second
	flushEvery    = 10 * time.Second
	MaxTitle      = 120 // characters
)

// Transcriber is the local transcription service (internal/transcribe); nil when there is none.
type Transcriber interface {
	// Enqueue queues a 16 kHz mono PCM16 WAV for transcription under a source key such as
	// "recording:<id>/<n>" and returns at once; it fails when voice isn't set up.
	Enqueue(source, wavPath, language string) error
	// AutoTranscribe reports whether finished recordings are transcribed without asking.
	AutoTranscribe() bool
}

// ErrNoVoice: there is no transcription service on this install.
var ErrNoVoice = errors.New("Voice isn't set up: Settings → Voice")

// VoiceError is the transcriber's refusal (the API answers 503 with it).
type VoiceError struct{ Err error }

func (e VoiceError) Error() string { return e.Err.Error() }
func (e VoiceError) Unwrap() error { return e.Err }

// GapError: a chunk starts after the end of what is stored (Size bytes); the window must
// send from Size, or start a new segment.
type GapError struct {
	Size int64
	Msg  string
}

func (e *GapError) Error() string { return e.Msg }

type Options struct {
	Store *store.Store
	Dir   string // config.Paths.RecordingsDir()
	// Publish sends bus events (recording.updated, recording.removed); nil = none.
	Publish func(model.BusEvent)
	// Settings returns config.json "recorder"; nil = the defaults.
	Settings func() config.RecorderConfig
	Logger   *slog.Logger
	Now      func() time.Time // tests
	// DiskFree is the free bytes of the volume holding a path; nil = sysinfo.DiskSpace.
	DiskFree   func(path string) (uint64, error)
	StaleAfter time.Duration // 0 = StaleAfter
	// OutageGrace: a browser window's recording without chunks or heartbeats for this long is interrupted; 0 = OutageGrace
	OutageGrace time.Duration
}

type Service struct {
	o   Options
	key []byte // signs playback tickets; new on every start

	mu       sync.Mutex
	live     *session // the one recording being captured; nil when none
	tr       Transcriber
	finished func(model.Recording) // SetFinished; nil = none

	stop chan struct{}
	done chan struct{}
}

// session is the live recording's state in memory; the files are the truth for sizes.
type session struct {
	rec       model.Recording // segments hold file names, not paths
	segs      []*segState     // segs[i] is segment i+1
	last      time.Time       // last chunk or heartbeat
	diskAt    time.Time       // next free-disk check
	flushedAt time.Time       // last time the sizes went to the database
	nat       *nativeState    // native recordings only (native.go)
}

type segState struct {
	media, pcm int64     // bytes in the media file, PCM bytes after the WAV header
	clientMS   int64     // the window's own count of this segment's recorded time
	sum        hash.Hash // running SHA-256 of the media file
}

func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DiskFree == nil {
		o.DiskFree = func(p string) (uint64, error) { free, _, err := sysinfo.DiskSpace(p); return free, err }
	}
	if o.OutageGrace <= 0 {
		o.OutageGrace = OutageGrace
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = StaleAfter
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Service{o: o, key: key}
}

// SetTranscriber connects the transcription service (nil disconnects it).
func (s *Service) SetTranscriber(t Transcriber) {
	s.mu.Lock()
	s.tr = t
	s.mu.Unlock()
}

// SetFinished connects the hook that runs once a recording is done (internal/summarize: its
// transcription and its summary); nil disconnects it. With a hook the recorder queues no
// transcription by itself: the hook decides. It runs outside the recorder's lock and must
// return quickly.
func (s *Service) SetFinished(fn func(model.Recording)) {
	s.mu.Lock()
	s.finished = fn
	s.mu.Unlock()
}

// Settings are the recorder's limits and the capture settings the window uses.
func (s *Service) Settings() config.RecorderConfig {
	if s.o.Settings == nil {
		return (*config.RecorderConfig)(nil).Resolved()
	}
	c := s.o.Settings()
	return c.Resolved()
}

// Start picks up a recording that was live when the daemon stopped (the window may still be
// sending; if not, the sweep marks it interrupted) and starts the sweep.
func (s *Service) Start() error {
	if err := os.MkdirAll(s.o.Dir, 0o700); err != nil {
		return err
	}
	live, err := s.o.Store.LiveRecordings()
	if err != nil {
		return err
	}
	s.mu.Lock()
	for i, r := range live {
		if i > 0 { // never more than one; keep the newest
			s.finishStoredLocked(r, model.RecordingInterrupted, "another recording was running")
			continue
		}
		sess, err := s.reload(r)
		if err != nil {
			s.o.Logger.Warn("recorder: reload live recording", "id", r.ID, "err", err)
			s.finishStoredLocked(r, model.RecordingFailed, "its files could not be read after a restart: "+err.Error())
			continue
		}
		s.live = sess
	}
	s.mu.Unlock()
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go s.loop()
	return nil
}

// Close stops the sweep and saves the live recording's sizes (it continues after a restart).
func (s *Service) Close() {
	if s.stop != nil {
		close(s.stop)
		<-s.done
		s.stop = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != nil {
		s.flushLocked(s.live)
	}
}

func (s *Service) loop() {
	defer close(s.done)
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.Sweep()
		}
	}
}

// staleAfter is how long the live recording may go silent: the native app StaleAfter, a browser window OutageGrace.
func (s *Service) staleAfter(sess *session) time.Duration {
	if sess.nat != nil {
		return s.o.StaleAfter
	}
	return s.o.OutageGrace
}

// Sweep marks the live recording interrupted when nothing came for StaleAfter, and stops it
// at the longest allowed length.
func (s *Service) Sweep() {
	s.mu.Lock()
	sess := s.live
	if sess == nil {
		s.mu.Unlock()
		return
	}
	now := s.o.Now()
	var fin *model.Recording
	switch {
	case now.Sub(sess.last) > s.staleAfter(sess):
		r := s.finishLocked(sess, model.RecordingInterrupted,
			fmt.Sprintf("nothing arrived from the recording window for %s (was it closed?); what came before is kept", s.staleAfter(sess)))
		fin = &r
	case sess.nat != nil && s.nativeStopLate(sess, now):
		r := s.finishLocked(sess, model.RecordingDone, joinNote(sess.nat.stopNote, "the app did not finish its last part in time"))
		fin = &r
	case sess.nat != nil && s.overLongLocked(sess, now):
		if !sess.nat.stopping() { // the app finishes its last part, then the recording ends
			s.askStopLocked(sess, s.tooLongNote())
			s.flushLocked(sess)
			s.publish(model.EvRecordingUpdated, s.view(s.liveRecLocked(sess)))
		}
	case s.overLongLocked(sess, now):
		r := s.finishLocked(sess, model.RecordingDone, s.tooLongNote())
		fin = &r
	}
	s.mu.Unlock()
	if fin != nil {
		s.afterFinish(*fin)
	}
}

// BeginRequest starts a recording; the owner's window sends it once capture is running.
type BeginRequest struct {
	Kind model.RecordingKind `json:"kind"` // screen | window | audio | camera
	Mic  bool                `json:"mic"`  // the microphone is recorded (always for audio)
	// SystemAudio: the screen's capture came with the device's own sound (screen and window
	// only), mixed with the microphone into the one audio track and the WAV.
	SystemAudio bool   `json:"system_audio,omitempty"`
	Mime        string `json:"mime"` // the MediaRecorder's type, e.g. video/mp4;codecs=avc1
	Title       string `json:"title,omitempty"`
	// Source: web (the default; chunks) or native (the macOS app writes the files; native.go).
	Source model.RecordingSource `json:"source,omitempty"`
	// Sensitivity: model.SensitivityClinic for patient data (local models only), "" otherwise.
	Sensitivity string `json:"sensitivity,omitempty"`
}

var idRx = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// ValidID reports whether id has a recording id's form.
func ValidID(id string) bool { return idRx.MatchString(id) }

var mimeRx = regexp.MustCompile(`^(video|audio)/[a-z0-9.+-]+(;[a-z0-9 =,.+"-]*)?$`)

// Begin starts a recording. Only the owner's route calls it, after a click in their window.
func (s *Service) Begin(req BeginRequest) (model.Recording, error) {
	if req.Source == model.RecordingSourceNative && strings.TrimSpace(req.Mime) == "" {
		req.Mime = NativeMime
	}
	if err := validSensitivity(req.Sensitivity); err != nil {
		return model.Recording{}, err
	}
	mime := strings.ToLower(strings.TrimSpace(req.Mime))
	if len(mime) > 120 || !mimeRx.MatchString(mime) {
		return model.Recording{}, invalid("mime must be the recorder's audio/… or video/… type")
	}
	switch req.Kind {
	case model.RecordingScreen, model.RecordingWindow:
		if !strings.HasPrefix(mime, "video/") {
			return model.Recording{}, invalid("a screen recording needs a video/… type")
		}
	case model.RecordingCamera:
		if !strings.HasPrefix(mime, "video/") {
			return model.Recording{}, invalid("a camera clip needs a video/… type")
		}
	case model.RecordingAudio:
		if !req.Mic {
			return model.Recording{}, invalid("an audio recording records the microphone (mic: true)")
		}
		if !strings.HasPrefix(mime, "audio/") {
			return model.Recording{}, invalid("an audio recording needs an audio/… type")
		}
	default:
		return model.Recording{}, invalid("kind must be screen, window, audio or camera")
	}
	if req.SystemAudio && req.Kind != model.RecordingScreen && req.Kind != model.RecordingWindow {
		return model.Recording{}, invalid("only a screen or window recording has device audio")
	}
	title, err := cleanTitle(req.Title)
	if err != nil {
		return model.Recording{}, err
	}
	now := s.o.Now()
	if title == "" {
		title = defaultTitle(req.Kind, now)
	}
	source, err := checkSource(req, mime)
	if err != nil {
		return model.Recording{}, err
	}
	// A window recording silent for StaleAfter while a new one starts was most likely closed: with OutageGrace it
	// would block Record for 10 minutes, so the new one takes over and the old one ends interrupted, kept.
	s.mu.Lock()
	var old *model.Recording
	if l := s.live; l != nil && l.nat == nil && now.Sub(l.last) > s.o.StaleAfter {
		r := s.finishLocked(l, model.RecordingInterrupted, fmt.Sprintf(
			"a new recording was started after this one's window had been silent for %d s (was it closed?); what came before is kept",
			int(now.Sub(l.last).Seconds())))
		old = &r
	}
	s.mu.Unlock()
	if old != nil {
		s.afterFinish(*old)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != nil {
		return model.Recording{}, conflict("a recording is already running (%s); stop it first", s.live.rec.Title)
	}
	if err := os.MkdirAll(s.o.Dir, 0o700); err != nil {
		return model.Recording{}, err
	}
	if free, err := s.o.DiskFree(s.o.Dir); err == nil && free < minFreeStart {
		return model.Recording{}, conflict("only %s is free on the disk; a recording needs at least 1 GB", humanBytes(int64(free)))
	}
	ms := now.UnixMilli()
	r := model.Recording{ID: model.NewID(), Title: title, Kind: req.Kind, State: model.RecordingActive, Mime: mime,
		Mic: req.Mic, SystemAudio: req.SystemAudio, Sensitivity: req.Sensitivity, Segments: []model.RecordingSegment{}, ResumedAt: ms, StartedAt: ms, UpdatedAt: ms}
	if err := os.MkdirAll(s.recDir(r.ID), 0o700); err != nil {
		return model.Recording{}, err
	}
	r.Source = source
	if err := s.o.Store.InsertRecording(r); err != nil {
		_ = os.RemoveAll(s.recDir(r.ID))
		return model.Recording{}, err
	}
	s.live = &session{rec: r, last: now, diskAt: now.Add(diskEvery), flushedAt: now}
	if source == model.RecordingSourceNative {
		s.live.nat = newNativeState()
	}
	v := s.view(r)
	s.publish(model.EvRecordingUpdated, v)
	return v, nil
}

// Append stores one chunk of segment n's media or PCM at offset (the bytes already sent for
// that track). A chunk that was stored already is accepted again (retries); one that starts
// after the end is a *GapError. An empty chunk is a heartbeat. durMS is the window's count of
// the segment's recorded time so far. It returns the track's size afterwards.
func (s *Service) Append(id string, n int, track Track, offset, durMS int64, data []byte) (int64, error) {
	if track != TrackMedia && track != TrackPCM {
		return 0, invalid("track must be media or pcm")
	}
	if len(data) > MaxChunkBytes {
		return 0, invalid("a chunk is at most %d MB", MaxChunkBytes>>20)
	}
	if offset < 0 {
		return 0, invalid("offset must not be negative")
	}
	s.mu.Lock()
	sess, err := s.liveLocked(id)
	if err == nil && sess.nat != nil {
		err = conflict("the desktop app writes this recording's files itself")
	}
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	now := s.o.Now()
	sess.last = now
	size, created, err := s.appendLocked(sess, n, track, offset, durMS, data)
	closed := s.closedLocked(sess, n, created)
	if created {
		s.flushLocked(sess)
		s.publish(model.EvRecordingUpdated, s.view(s.liveRecLocked(sess)))
	} else if now.Sub(sess.flushedAt) >= flushEvery {
		s.flushLocked(sess)
	}
	var fin *model.Recording
	if err == nil {
		if note := s.limitLocked(sess, now); note != "" {
			r := s.finishLocked(sess, model.RecordingDone, note)
			fin = &r
		}
	}
	s.mu.Unlock()
	if closed != nil {
		s.transcribeClosed(*closed)
	}
	if fin != nil {
		s.afterFinish(*fin)
	}
	return size, err
}

// closedPart is a segment the window has moved on from: its WAV is complete (the window sends a
// segment's last PCM before the next segment's first chunk), so it can be transcribed already.
type closedPart struct {
	id  string
	n   int
	wav string // absolute path
	tr  Transcriber
}

// closedLocked returns segment n-1 when chunk n created segment n of a web recording with sound
// and automatic transcription, so the report is ready minutes after Stop and not a whole
// recording's transcription later (the owner: "chunks of 5 min"). nil otherwise.
func (s *Service) closedLocked(sess *session, n int, created bool) *closedPart {
	if !created || n < 2 || sess.nat != nil || s.tr == nil || !hasSound(sess.rec) {
		return nil
	}
	seg, g := sess.rec.Segments[n-2], sess.segs[n-2]
	if seg.WAV == "" || g.pcm == 0 {
		return nil
	}
	return &closedPart{id: sess.rec.ID, n: seg.N, wav: filepath.Join(s.recDir(sess.rec.ID), seg.WAV), tr: s.tr}
}

// transcribeClosed queues a closed segment's WAV (outside the lock). The summary's watcher
// finds it queued or done and queues only the segments still missing after Stop.
func (s *Service) transcribeClosed(p closedPart) {
	if !p.tr.AutoTranscribe() {
		return
	}
	if err := p.tr.Enqueue(Source(p.id, p.n), p.wav, ""); err != nil {
		s.o.Logger.Info("recorder: closed segment not transcribed yet", "id", p.id, "segment", p.n, "err", err)
	}
}

func (s *Service) appendLocked(sess *session, n int, track Track, offset, durMS int64, data []byte) (int64, bool, error) {
	if n < 1 || n > maxSegments {
		return 0, false, invalid("segment must be 1 to %d", maxSegments)
	}
	if track == TrackPCM && !hasSound(sess.rec) {
		return 0, false, invalid("this recording has no microphone or device audio track")
	}
	created := false
	switch {
	case n == len(sess.segs)+1:
		if err := s.newSegmentLocked(sess, n); err != nil {
			return 0, false, err
		}
		created = true
	case n > len(sess.segs)+1:
		return 0, false, conflict("segment %d cannot start before segment %d", n, len(sess.segs)+1)
	}
	g := sess.segs[n-1]
	if durMS > g.clientMS {
		g.clientMS = durMS
	}
	cur := g.media
	if track == TrackPCM {
		cur = g.pcm
	}
	if len(data) == 0 {
		return cur, created, nil
	}
	if offset > cur {
		return cur, created, &GapError{Size: cur, Msg: fmt.Sprintf("segment %d's %s has %d bytes; this chunk starts at %d", n, track, cur, offset)}
	}
	end := offset + int64(len(data))
	if end <= cur {
		return cur, created, nil // stored already (a retry)
	}
	data = data[cur-offset:]
	if end > MaxSegmentBytes {
		return cur, created, errf(fleet.KindInvalid, "segment %d would be larger than %d MB; start a new segment", n, MaxSegmentBytes>>20)
	}
	seg := sess.rec.Segments[n-1]
	if track == TrackMedia {
		if err := appendFile(filepath.Join(s.recDir(sess.rec.ID), seg.File), data); err != nil {
			return cur, created, err
		}
		g.sum.Write(data)
		g.media = end
		sess.rec.Bytes += int64(len(data))
	} else {
		if cur%2 != 0 || len(data)%2 != 0 {
			return cur, created, invalid("PCM chunks are whole 16-bit samples")
		}
		if err := appendPCM(filepath.Join(s.recDir(sess.rec.ID), seg.WAV), cur, data); err != nil {
			return cur, created, err
		}
		g.pcm = end
	}
	return end, created, nil
}

func (s *Service) newSegmentLocked(sess *session, n int) error {
	base := fmt.Sprintf("seg-%04d", n)
	g := model.RecordingSegment{N: n, File: base + mediaExt(sess.rec.Mime), StartedAt: s.o.Now().UnixMilli()}
	dir := s.recDir(sess.rec.ID)
	if err := createEmpty(filepath.Join(dir, g.File)); err != nil {
		return err
	}
	if hasSound(sess.rec) {
		g.WAV = base + ".wav"
		if err := createWAV(filepath.Join(dir, g.WAV)); err != nil {
			return err
		}
	}
	if err := s.o.Store.PutRecordingSegment(sess.rec.ID, g); err != nil {
		return err
	}
	sess.rec.Segments = append(sess.rec.Segments, g)
	sess.segs = append(sess.segs, &segState{sum: newSum()})
	return nil
}

// limitLocked returns why the live recording must stop now ("" = it may go on).
func (s *Service) limitLocked(sess *session, now time.Time) string {
	if s.overLongLocked(sess, now) {
		return s.tooLongNote()
	}
	if now.After(sess.diskAt) {
		sess.diskAt = now.Add(diskEvery)
		if free, err := s.o.DiskFree(s.o.Dir); err == nil && free < minFreeKeep {
			return "stopped: the disk is almost full (" + humanBytes(int64(free)) + " left)"
		}
	}
	return ""
}

func (s *Service) overLongLocked(sess *session, now time.Time) bool {
	max := s.Settings().MaxMinutes
	return max > 0 && elapsed(sess.rec, now) >= int64(max)*60_000 // 0 = no limit
}

func (s *Service) tooLongNote() string {
	return fmt.Sprintf("stopped at the longest recording length (%s; config.json recorder.max_minutes)",
		(time.Duration(s.Settings().MaxMinutes) * time.Minute).String())
}

// elapsed is the recorded time so far (pauses excluded), by the daemon's clock.
func elapsed(r model.Recording, now time.Time) int64 {
	ms := r.DurationMS
	if r.State == model.RecordingActive && r.ResumedAt > 0 {
		ms += now.UnixMilli() - r.ResumedAt
	}
	return ms
}

// SetState pauses (paused), resumes (recording) or stops (stopped) the live recording;
// heartbeat (the window's, every few seconds) only says the window is still there. Stopping a
// finished recording returns it unchanged.
func (s *Service) SetState(id, state string) (model.Recording, error) {
	s.mu.Lock()
	sess, err := s.liveLocked(id)
	if err != nil {
		s.mu.Unlock()
		if state == "stopped" && fleet.KindOf(err) == fleet.KindConflict {
			return s.Get(id)
		}
		return model.Recording{}, err
	}
	now := s.o.Now()
	sess.last = now
	r := &sess.rec
	ms := now.UnixMilli()
	if sess.nat.stopping() && state != "heartbeat" { // the app is finishing its last part
		state = "heartbeat"
	}
	if sess.nat != nil && state == "stopped" { // asked elsewhere: the app follows and reports its last part
		s.askStopLocked(sess, "")
		s.flushLocked(sess)
		s.publish(model.EvRecordingUpdated, s.view(s.liveRecLocked(sess)))
		state = "heartbeat"
	}
	switch state {
	case "heartbeat":
	case string(model.RecordingPaused):
		if r.State == model.RecordingActive {
			r.DurationMS = elapsed(*r, now)
			r.ResumedAt, r.State, r.UpdatedAt = 0, model.RecordingPaused, ms
			s.flushLocked(sess)
			s.publish(model.EvRecordingUpdated, s.view(s.liveRecLocked(sess)))
		}
	case string(model.RecordingActive):
		if r.State == model.RecordingPaused {
			r.ResumedAt, r.State, r.UpdatedAt = ms, model.RecordingActive, ms
			s.flushLocked(sess)
			s.publish(model.EvRecordingUpdated, s.view(s.liveRecLocked(sess)))
		}
	case "stopped":
		fin := s.finishLocked(sess, model.RecordingDone, "")
		s.mu.Unlock()
		s.afterFinish(fin)
		return fin, nil
	default:
		s.mu.Unlock()
		return model.Recording{}, invalid("state must be paused, recording, stopped or heartbeat")
	}
	v := s.view(s.liveRecLocked(sess))
	s.mu.Unlock()
	return v, nil
}

// finishLocked ends the live recording with durations and checksums.
func (s *Service) finishLocked(sess *session, state model.RecordingState, note string) model.Recording {
	if sess.nat != nil {
		note = s.nativeFinishLocked(sess, note)
	}
	now := s.o.Now()
	r := s.liveRecLocked(sess)
	counted := elapsed(r, now)
	var sum int64
	all := len(r.Segments) > 0
	for i := range r.Segments {
		g := sess.segs[i]
		seg := &r.Segments[i]
		if sess.nat == nil { // native: hashed from the file when it closed
			seg.SHA256 = hexSum(g.sum)
		}
		seg.DurationMS = segDuration(g)
		if seg.DurationMS == 0 {
			all = false
		}
		sum += seg.DurationMS
	}
	r.DurationMS = counted
	if all && sum > 0 {
		r.DurationMS = sum // the WAV's length is exact; the window's count is close
	}
	if r.Bytes == 0 && !hasPCM(sess) && state != model.RecordingFailed {
		state, note = model.RecordingFailed, "nothing was recorded"
	}
	ms := now.UnixMilli()
	r.State, r.Error, r.ResumedAt, r.EndedAt, r.UpdatedAt = state, note, 0, ms, ms
	sess.rec = r
	s.flushLocked(sess)
	s.live = nil
	return s.view(r)
}

// finishStoredLocked ends a recording that has no session (found live at start).
func (s *Service) finishStoredLocked(r model.Recording, state model.RecordingState, note string) {
	ms := s.o.Now().UnixMilli()
	r.State, r.Error, r.ResumedAt, r.EndedAt, r.UpdatedAt = state, note, 0, ms, ms
	if err := s.o.Store.UpdateRecording(r); err != nil {
		s.o.Logger.Warn("recorder: finish", "id", r.ID, "err", err)
	}
}

// afterFinish runs outside the lock: it calls the Finished hook for a done recording, or
// without one queues the transcription when that is automatic.
func (s *Service) afterFinish(r model.Recording) {
	s.publish(model.EvRecordingUpdated, r)
	s.mu.Lock()
	tr, fin := s.tr, s.finished
	s.mu.Unlock()
	if fin != nil {
		if r.State == model.RecordingDone {
			fin(r)
		}
		return
	}
	if tr == nil || !tr.AutoTranscribe() || (r.State != model.RecordingDone && r.State != model.RecordingInterrupted) {
		return
	}
	if _, err := s.enqueue(tr, r, ""); err != nil {
		s.o.Logger.Info("recorder: automatic transcription not queued", "id", r.ID, "err", err)
	}
}

// liveRecLocked is the live recording with its current segment sizes (names, not paths).
func (s *Service) liveRecLocked(sess *session) model.Recording {
	r := sess.rec
	r.Stopping = sess.nat.stopping()
	r.Segments = append([]model.RecordingSegment(nil), sess.rec.Segments...)
	for i := range r.Segments {
		g := sess.segs[i]
		r.Segments[i].Bytes, r.Segments[i].WAVBytes = g.media, g.pcm
		if r.Segments[i].WAV != "" {
			r.Segments[i].WAVBytes += wavHeaderSize
		}
		r.Segments[i].DurationMS = segDuration(g)
	}
	return r
}

func hasPCM(sess *session) bool {
	for _, g := range sess.segs {
		if g.pcm > 0 {
			return true
		}
	}
	return false
}

func segDuration(g *segState) int64 {
	if g.pcm > 0 {
		return g.pcm / pcmBytesPerMS
	}
	return g.clientMS
}

// flushLocked writes the live recording's row and segments to the database.
func (s *Service) flushLocked(sess *session) {
	r := s.liveRecLocked(sess)
	sess.flushedAt = s.o.Now()
	if err := s.o.Store.UpdateRecording(r); err != nil {
		s.o.Logger.Warn("recorder: save", "id", r.ID, "err", err)
	}
	for _, g := range r.Segments {
		if err := s.o.Store.PutRecordingSegment(r.ID, g); err != nil {
			s.o.Logger.Warn("recorder: save segment", "id", r.ID, "n", g.N, "err", err)
		}
	}
}

// liveLocked returns the session of id; a finished recording is a conflict.
func (s *Service) liveLocked(id string) (*session, error) {
	if s.live != nil && s.live.rec.ID == id {
		return s.live, nil
	}
	r, err := s.o.Store.GetRecording(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("no recording %q", id)
	}
	if err != nil {
		return nil, err
	}
	return nil, conflict("this recording is %s; nothing more can be added to it", r.State)
}

// reload rebuilds the session of a recording that was live when the daemon stopped.
func (s *Service) reload(r model.Recording) (*session, error) {
	if r.Source == model.RecordingSourceNative {
		return s.reloadNative(r), nil
	}
	now := s.o.Now()
	sess := &session{rec: r, last: now, diskAt: now.Add(diskEvery), flushedAt: now}
	dir := s.recDir(r.ID)
	sess.rec.Bytes = 0 // the files say how much arrived
	for _, seg := range r.Segments {
		g := &segState{clientMS: seg.DurationMS, sum: newSum()}
		n, err := hashFile(filepath.Join(dir, seg.File), g.sum)
		if err != nil {
			return nil, err
		}
		g.media = n
		sess.rec.Bytes += n
		if seg.WAV != "" {
			st, err := os.Stat(filepath.Join(dir, seg.WAV))
			if err != nil {
				return nil, err
			}
			g.pcm = max(0, st.Size()-wavHeaderSize) &^ 1
		}
		sess.segs = append(sess.segs, g)
	}
	return sess, nil
}

// ---- reading

// Get returns a recording with its files' absolute paths.
func (s *Service) Get(id string) (model.Recording, error) {
	s.mu.Lock()
	if s.live != nil && s.live.rec.ID == id {
		v := s.view(s.liveRecLocked(s.live))
		s.mu.Unlock()
		return v, nil
	}
	s.mu.Unlock()
	r, err := s.o.Store.GetRecording(id)
	if errors.Is(err, store.ErrNotFound) {
		return r, notFound("no recording %q", id)
	}
	if err != nil {
		return r, err
	}
	return s.view(r), nil
}

// List returns recordings newest first (limit <= 0: all).
func (s *Service) List(limit int) ([]model.Recording, error) {
	list, err := s.o.Store.ListRecordings(limit)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Recording, 0, len(list))
	for _, r := range list {
		if s.live != nil && s.live.rec.ID == r.ID {
			r = s.liveRecLocked(s.live)
		}
		out = append(out, s.view(r))
	}
	return out, nil
}

// Active returns the live recording, or nil.
func (s *Service) Active() *model.Recording {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live == nil {
		return nil
	}
	v := s.view(s.liveRecLocked(s.live))
	return &v
}

// Busy names the live recording, so an update waits for it (the window would lose its daemon).
func (s *Service) Busy() []string {
	if r := s.Active(); r != nil {
		return []string{"a recording (" + r.Title + ")"}
	}
	return nil
}

// SegmentFile returns the path of segment n's media or WAV file and its content type.
func (s *Service) SegmentFile(id string, n int, track Track) (string, string, error) {
	r, err := s.Get(id)
	if err != nil {
		return "", "", err
	}
	if n < 1 || n > len(r.Segments) {
		return "", "", notFound("recording %s has no segment %d", id, n)
	}
	g := r.Segments[n-1]
	switch track {
	case TrackMedia, "":
		return g.File, baseType(r.Mime), nil
	case TrackPCM:
		if g.WAV == "" {
			return "", "", notFound("segment %d has no 16 kHz WAV", n)
		}
		return g.WAV, "audio/wav", nil
	}
	return "", "", invalid("track must be media or pcm")
}

// ---- changing

// Rename sets a recording's title.
func (s *Service) Rename(id, title string) (model.Recording, error) {
	t, err := cleanTitle(title)
	if err != nil {
		return model.Recording{}, err
	}
	if t == "" {
		return model.Recording{}, invalid("the title is empty")
	}
	return s.update(id, func(r *model.Recording) { r.Title = t })
}

// SetSensitivity tags a recording: model.SensitivityClinic (patient data: its report is written by local models
// only) or "" (any model).
func (s *Service) SetSensitivity(id, v string) (model.Recording, error) {
	if err := validSensitivity(v); err != nil {
		return model.Recording{}, err
	}
	return s.update(id, func(r *model.Recording) { r.Sensitivity = v })
}

func validSensitivity(v string) error {
	if v != "" && v != model.SensitivityClinic {
		return invalid("sensitivity is %q or empty, not %q", model.SensitivityClinic, v)
	}
	return nil
}

// update changes a recording, live or stored, and announces it.
func (s *Service) update(id string, fn func(*model.Recording)) (model.Recording, error) {
	s.mu.Lock()
	if s.live != nil && s.live.rec.ID == id {
		fn(&s.live.rec)
		s.live.rec.UpdatedAt = s.o.Now().UnixMilli()
		s.flushLocked(s.live)
		v := s.view(s.liveRecLocked(s.live))
		s.mu.Unlock()
		s.publish(model.EvRecordingUpdated, v)
		return v, nil
	}
	s.mu.Unlock()
	r, err := s.o.Store.GetRecording(id)
	if errors.Is(err, store.ErrNotFound) {
		return r, notFound("no recording %q", id)
	}
	if err != nil {
		return r, err
	}
	fn(&r)
	r.UpdatedAt = s.o.Now().UnixMilli()
	if err := s.o.Store.UpdateRecording(r); err != nil {
		return r, err
	}
	v := s.view(r)
	s.publish(model.EvRecordingUpdated, v)
	return v, nil
}

// Delete removes a finished recording and its files.
func (s *Service) Delete(id string) error {
	if !idRx.MatchString(id) { // the id names a folder
		return notFound("no recording %q", id)
	}
	s.mu.Lock()
	if s.live != nil && s.live.rec.ID == id {
		s.mu.Unlock()
		return conflict("stop the recording before deleting it")
	}
	s.mu.Unlock()
	if _, err := s.o.Store.GetRecording(id); errors.Is(err, store.ErrNotFound) {
		return notFound("no recording %q", id)
	} else if err != nil {
		return err
	}
	if err := os.RemoveAll(s.recDir(id)); err != nil {
		return fmt.Errorf("delete the recording's files: %w", err)
	}
	if err := s.o.Store.DeleteRecording(id); err != nil {
		return err
	}
	s.publish(model.EvRecordingRemoved, map[string]string{"id": id})
	return nil
}

// Transcribe queues every segment's WAV of a finished recording with the transcriber; it
// returns how many were queued.
func (s *Service) Transcribe(id, language string) (int, error) {
	r, err := s.Get(id)
	if err != nil {
		return 0, err
	}
	if r.State.Live() {
		return 0, conflict("the recording is still running; stop it first")
	}
	s.mu.Lock()
	tr := s.tr
	s.mu.Unlock()
	if tr == nil {
		return 0, ErrNoVoice
	}
	return s.enqueue(tr, r, strings.TrimSpace(language))
}

func (s *Service) enqueue(tr Transcriber, r model.Recording, language string) (int, error) {
	n := 0
	for _, g := range r.Segments {
		if g.WAV == "" || g.WAVBytes <= wavHeaderSize {
			continue
		}
		if err := tr.Enqueue(Source(r.ID, g.N), g.WAV, language); err != nil {
			return n, VoiceError{err}
		}
		n++
	}
	if n == 0 {
		return 0, invalid("this recording has no sound to transcribe")
	}
	return n, nil
}

// Source is the transcription source key of segment n: "recording:<id>/<n>".
func Source(id string, n int) string { return fmt.Sprintf("recording:%s/%d", id, n) }

// ---- helpers

func (s *Service) recDir(id string) string { return filepath.Join(s.o.Dir, id) }

// view returns r with absolute paths (the store and the session keep names).
func (s *Service) view(r model.Recording) model.Recording {
	dir := s.recDir(r.ID)
	r.Dir = dir
	segs := make([]model.RecordingSegment, len(r.Segments))
	for i, g := range r.Segments {
		if g.File != "" && !filepath.IsAbs(g.File) {
			g.File = filepath.Join(dir, g.File)
		}
		if g.WAV != "" && !filepath.IsAbs(g.WAV) {
			g.WAV = filepath.Join(dir, g.WAV)
		}
		segs[i] = g
	}
	r.Segments = segs
	r.Frames = s.frames(r.ID)
	return r
}

// hasSound reports whether r has a 16 kHz track (the microphone, device audio, or both mixed).
func hasSound(r model.Recording) bool { return r.Mic || r.SystemAudio }

func (s *Service) publish(typ string, data any) {
	if s.o.Publish != nil {
		s.o.Publish(model.BusEvent{Type: typ, Data: data, At: model.Now()})
	}
}

func cleanTitle(t string) (string, error) {
	t = strings.Join(strings.Fields(t), " ")
	if utf8.RuneCountInString(t) > MaxTitle {
		return "", invalid("the title is longer than %d characters", MaxTitle)
	}
	return t, nil
}

func defaultTitle(k model.RecordingKind, now time.Time) string {
	what := map[model.RecordingKind]string{model.RecordingScreen: "Screen recording", model.RecordingWindow: "Window recording",
		model.RecordingAudio: "Voice recording", model.RecordingCamera: "Camera clip"}[k]
	return what + " " + now.Format("2 Jan 15:04")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KB", n>>10)
	}
}

func errf(k fleet.Kind, format string, args ...any) error {
	return &fleet.Error{Kind: k, Msg: fmt.Sprintf(format, args...)}
}
func invalid(format string, args ...any) error  { return errf(fleet.KindInvalid, format, args...) }
func notFound(format string, args ...any) error { return errf(fleet.KindNotFound, format, args...) }
func conflict(format string, args ...any) error { return errf(fleet.KindConflict, format, args...) }
