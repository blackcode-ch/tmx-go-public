package recorder

// Native recordings (DESIGN.md "Recorder", native capture). The macOS app captures with
// ScreenCaptureKit in its own window process and writes the files into the recording's folder
// itself: seg-NNNN.mp4 (H.264 + AAC, finished part by part), seg-NNNN.wav (16 kHz mono PCM16 of
// the microphone and device audio, mixed) and frames/f-<ms>.jpg. It tells the daemon what it
// wrote through Native (POST /api/recordings/{id}/native); every report is also its heartbeat.
// The daemon still owns the row and its states, the indicator, the sweep and finishing: sizes
// and SHA-256 come from the files, durations from the WAVs.
//
// A stop asked for anywhere but the app (another window, a limit) does not end a native
// recording at once: the app is still writing its last part, and an MP4 plays only once it is
// finished. The recording says stopping, the app follows, closes the part and reports it with
// last; if it doesn't within StaleAfter, the sweep ends the recording with what is closed.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// NativeMime is a native recording's media type: SCRecordingOutput writes H.264 and AAC in MP4.
const NativeMime = "video/mp4;codecs=avc1,mp4a.40.2"

const (
	nativeMaxFrames     = 400       // frames per recording
	nativeMaxFrameBytes = 600 << 10 // one frame's JPEG
	nativeMaxReport     = 100       // frames in one report
)

// NativeReport is what the desktop app tells the daemon about a native recording's files.
type NativeReport struct {
	// Segment is the part the report is about (0: none, e.g. while paused). One more than the
	// parts so far opens it; the app names its files seg-NNNN.mp4 and seg-NNNN.wav.
	Segment int `json:"segment,omitempty"`
	// DurationMS is the segment's recorded time so far, by the app's count.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Closed: the segment's files are complete (the MP4 is finished, the WAV written).
	Closed bool `json:"closed,omitempty"`
	// Last: the recording ends here (the app stopped); with Closed for its last part.
	Last bool `json:"last,omitempty"`
	// Frames are the offsets (ms of recorded time) of frames/f-<ms>.jpg written since the last report.
	Frames []int64 `json:"frames,omitempty"`
	// Error says why the app stopped by itself (the capture failed or was ended by macOS); with Last.
	Error string `json:"error,omitempty"`
}

// nativeState is the part of a live session only native recordings have.
type nativeState struct {
	stopAsked time.Time      // a stop was asked for outside the app; zero = none
	stopNote  string         // why (a limit), kept as the recording's note
	closed    map[int]bool   // segments whose files are complete
	frames    map[int64]bool // frames reported and checked
}

func newNativeState() *nativeState {
	return &nativeState{closed: map[int]bool{}, frames: map[int64]bool{}}
}

func (n *nativeState) stopping() bool { return n != nil && !n.stopAsked.IsZero() }

// checkSource validates a BeginRequest's source (mime is already lower case and checked).
func checkSource(req BeginRequest, mime string) (model.RecordingSource, error) {
	switch req.Source {
	case "", model.RecordingSourceWeb:
		return model.RecordingSourceWeb, nil
	case model.RecordingSourceNative:
		if (req.Kind != model.RecordingScreen && req.Kind != model.RecordingWindow) || baseType(mime) != "video/mp4" {
			return "", invalid("a native recording is a screen or a window, in video/mp4")
		}
		return model.RecordingSourceNative, nil
	}
	return "", invalid("source must be web or native")
}

// Native takes the app's report about a native recording and returns the recording as the
// daemon sees it (state, stopping), which the app follows.
func (s *Service) Native(id string, rep NativeReport) (model.Recording, error) {
	switch {
	case rep.Segment < 0 || rep.Segment > maxSegments:
		return model.Recording{}, invalid("segment must be 1 to %d", maxSegments)
	case rep.DurationMS < 0 || rep.DurationMS > 24*3600_000:
		return model.Recording{}, invalid("duration_ms is out of range")
	case rep.Closed && rep.Segment == 0:
		return model.Recording{}, invalid("closed needs the segment")
	case len(rep.Frames) > nativeMaxReport:
		return model.Recording{}, invalid("at most %d frames in one report", nativeMaxReport)
	}
	if len(rep.Error) > 500 {
		rep.Error = rep.Error[:500]
	}
	// A closing part's MP4 (up to segment_mb MB) is hashed before taking the lock.
	sum := ""
	if rep.Closed {
		if p := s.nativeClosingPath(id, rep.Segment); p != "" {
			sum, _ = hashPath(p)
		}
	}
	s.mu.Lock()
	sess, err := s.liveLocked(id)
	if err != nil {
		s.mu.Unlock()
		return model.Recording{}, err
	}
	if sess.nat == nil {
		s.mu.Unlock()
		return model.Recording{}, conflict("this recording comes in chunks from the window, not from the desktop app")
	}
	now := s.o.Now()
	sess.last = now
	changed := false
	if rep.Segment > 0 {
		if changed, err = s.nativeSegmentLocked(sess, rep, sum); err != nil {
			s.mu.Unlock()
			return model.Recording{}, err
		}
	}
	for _, ms := range rep.Frames {
		s.nativeFrameLocked(sess, ms)
	}
	if rep.Last {
		note := sess.nat.stopNote
		if e := strings.TrimSpace(rep.Error); e != "" {
			note = joinNote(note, e)
		}
		fin := s.finishLocked(sess, model.RecordingDone, note)
		s.mu.Unlock()
		s.afterFinish(fin)
		return fin, nil
	}
	if note := s.limitLocked(sess, now); note != "" && !sess.nat.stopping() {
		s.askStopLocked(sess, note)
		changed = true
	}
	if changed {
		s.flushLocked(sess)
		s.publish(model.EvRecordingUpdated, s.view(s.liveRecLocked(sess)))
	} else if now.Sub(sess.flushedAt) >= flushEvery {
		s.flushLocked(sess)
	}
	v := s.view(s.liveRecLocked(sess))
	s.mu.Unlock()
	return v, nil
}

// nativeClosingPath is the media file of segment n of live native recording id, when it is
// being closed for the first time ("" otherwise).
func (s *Service) nativeClosingPath(id string, n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.live
	if sess == nil || sess.rec.ID != id || sess.nat == nil || sess.nat.closed[n] || n > len(sess.segs)+1 {
		return ""
	}
	return filepath.Join(s.recDir(id), nativeBase(n)+mediaExt(sess.rec.Mime))
}

func nativeBase(n int) string { return fmt.Sprintf("seg-%04d", n) }

// NativeFiles are the names of a native recording's segment n in its folder: the MP4 and the WAV
// (seg-0001.mp4, seg-0001.wav). The app writes them; the daemon names the same files.
func NativeFiles(n int) (media, wav string) {
	return nativeBase(n) + mediaExt(NativeMime), nativeBase(n) + ".wav"
}

// NativeFrameFile is where the frame at ms of recorded time goes, relative to the recording's
// folder: frames/f-000030000.jpg.
func NativeFrameFile(ms int64) string { return filepath.Join("frames", nativeFrameName(ms)) }

// WAVHeader is the 44-byte header of a 16 kHz mono PCM16 WAV with n data bytes.
func WAVHeader(n int64) []byte { return wavHeader(n) }

// nativeSegmentLocked opens segment rep.Segment (the next one) or updates it from its files,
// and closes it when the report says so; sum is its MP4's SHA-256 when known.
func (s *Service) nativeSegmentLocked(sess *session, rep NativeReport, sum string) (bool, error) {
	n, changed := rep.Segment, false
	switch {
	case n == len(sess.segs)+1:
		base := nativeBase(n)
		g := model.RecordingSegment{N: n, File: base + mediaExt(sess.rec.Mime), StartedAt: s.o.Now().UnixMilli()}
		if hasSound(sess.rec) {
			g.WAV = base + ".wav"
		}
		if err := s.o.Store.PutRecordingSegment(sess.rec.ID, g); err != nil {
			return false, err
		}
		sess.rec.Segments = append(sess.rec.Segments, g)
		sess.segs = append(sess.segs, &segState{sum: newSum()})
		changed = true
	case n > len(sess.segs)+1:
		return false, conflict("segment %d cannot start before segment %d", n, len(sess.segs)+1)
	}
	g := sess.segs[n-1]
	if rep.DurationMS > g.clientMS {
		g.clientMS = rep.DurationMS
	}
	s.statNativeLocked(sess, n-1)
	if rep.Closed && !sess.nat.closed[n] {
		dir := s.recDir(sess.rec.ID)
		seg := &sess.rec.Segments[n-1]
		media := filepath.Join(dir, seg.File)
		for _, p := range []string{media, filepath.Join(dir, seg.WAV)} {
			if p != dir {
				_ = os.Chmod(p, 0o600) // the app made them; keep them as private as the daemon's own
			}
		}
		// An MP4 without its index does not play (the recording output failed): it stays open,
		// and finishing says its video is lost.
		if g.media > 0 && !mp4Finished(media) {
			return true, nil
		}
		if sum == "" && g.media > 0 {
			sum, _ = hashPath(media)
		}
		if g.media > 0 {
			seg.SHA256 = sum
		}
		sess.nat.closed[n] = true
		changed = true
	}
	return changed, nil
}

// statNativeLocked reads segment i's sizes from its files (the app writes them).
func (s *Service) statNativeLocked(sess *session, i int) {
	dir := s.recDir(sess.rec.ID)
	seg, g := sess.rec.Segments[i], sess.segs[i]
	size := fileSize(filepath.Join(dir, seg.File))
	sess.rec.Bytes += size - g.media
	g.media = size
	if seg.WAV != "" {
		g.pcm = max(0, fileSize(filepath.Join(dir, seg.WAV))-wavHeaderSize) &^ 1
	}
}

// nativeFrameLocked checks a frame the app wrote: a JPEG of at most 600 KB, at most 400 per
// recording. One that breaks the rules is removed.
func (s *Service) nativeFrameLocked(sess *session, ms int64) {
	if ms < 0 || ms > 48*3600_000 || sess.nat.frames[ms] {
		return
	}
	p := filepath.Join(s.recDir(sess.rec.ID), "frames", nativeFrameName(ms))
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		s.o.Logger.Info("recorder: a reported frame is missing", "id", sess.rec.ID, "ms", ms)
		return
	}
	why := ""
	switch {
	case st.Size() > nativeMaxFrameBytes:
		why = "larger than 600 KB"
	case len(sess.nat.frames) >= nativeMaxFrames:
		why = "more than 400 frames"
	case !isJPEG(p):
		why = "not a JPEG"
	}
	if why != "" {
		s.o.Logger.Warn("recorder: frame removed", "id", sess.rec.ID, "ms", ms, "why", why)
		_ = os.Remove(p)
		return
	}
	_ = os.Chmod(p, 0o600)
	sess.nat.frames[ms] = true
}

// nativeFrameName is the file name of the frame at ms of recorded time: f-000030000.jpg.
func nativeFrameName(ms int64) string { return fmt.Sprintf("f-%09d.jpg", ms) }

var nativeFrameRx = regexp.MustCompile(`^f-(\d{9})\.jpg$`)

// askStopLocked asks the app to finish a native recording: the clock stops now, the recording
// says stopping, and it ends when the app reports its last part (or after StaleAfter).
func (s *Service) askStopLocked(sess *session, note string) {
	if sess.nat.stopping() {
		return
	}
	now := s.o.Now()
	r := &sess.rec
	if r.State == model.RecordingActive {
		r.DurationMS = elapsed(*r, now)
	}
	r.ResumedAt, r.UpdatedAt = 0, now.UnixMilli()
	sess.nat.stopAsked, sess.nat.stopNote = now, note
}

// nativeStopLate reports whether a native recording asked to stop has waited too long for the app.
func (s *Service) nativeStopLate(sess *session, now time.Time) bool {
	return sess.nat.stopping() && now.Sub(sess.nat.stopAsked) > s.o.StaleAfter
}

// nativeFinishLocked brings a native recording's segments up to date from the files before it
// finishes: sizes, WAV headers (the app may have died between two header updates), the SHA-256
// of a part that was finished but not reported, and no media for a part whose MP4 was never
// finished (it would not play). It returns the note, with that loss added.
func (s *Service) nativeFinishLocked(sess *session, note string) string {
	dir := s.recDir(sess.rec.ID)
	var lost []string
	for i := range sess.rec.Segments {
		seg := &sess.rec.Segments[i]
		s.statNativeLocked(sess, i)
		g := sess.segs[i]
		if seg.WAV != "" {
			_ = fixWAVHeader(filepath.Join(dir, seg.WAV))
		}
		if sess.nat.closed[seg.N] || g.media == 0 {
			continue
		}
		path := filepath.Join(dir, seg.File)
		if mp4Finished(path) {
			seg.SHA256, _ = hashPath(path)
			continue
		}
		what := fmt.Sprintf("the video of part %d is lost (its file was never finished)", seg.N)
		if g.pcm > 0 {
			what += "; its sound is kept"
		}
		lost = append(lost, what)
		sess.rec.Bytes -= g.media
		g.media = 0
	}
	if sess.nat.stopping() && note == "" {
		note = sess.nat.stopNote
	}
	for _, l := range lost {
		note = joinNote(note, l)
	}
	return note
}

// reloadNative rebuilds a native recording's session after a daemon restart, from its rows
// (a segment with a SHA-256 is closed) and its files.
func (s *Service) reloadNative(r model.Recording) *session {
	now := s.o.Now()
	sess := &session{rec: r, last: now, diskAt: now.Add(diskEvery), flushedAt: now, nat: newNativeState()}
	sess.rec.Bytes = 0
	for _, seg := range r.Segments {
		sess.segs = append(sess.segs, &segState{clientMS: seg.DurationMS, sum: newSum()})
		s.statNativeLocked(sess, len(sess.segs)-1)
		if seg.SHA256 != "" {
			sess.nat.closed[seg.N] = true
		}
	}
	if list, err := os.ReadDir(filepath.Join(s.recDir(r.ID), "frames")); err == nil {
		for _, e := range list {
			if m := nativeFrameRx.FindStringSubmatch(e.Name()); m != nil {
				ms, _ := strconv.ParseInt(m[1], 10, 64)
				sess.nat.frames[ms] = true
			}
		}
	}
	return sess
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// ---- files

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func hashPath(p string) (string, error) {
	h := newSum()
	if _, err := hashFile(p, h); err != nil {
		return "", err
	}
	return hexSum(h), nil
}

func isJPEG(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 3)
	_, err = io.ReadFull(f, b)
	return err == nil && bytes.Equal(b, []byte{0xff, 0xd8, 0xff})
}

// fixWAVHeader makes a 16 kHz WAV's header match the samples in the file.
func fixWAVHeader(p string) error {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < wavHeaderSize {
		return err
	}
	_, err = f.WriteAt(wavHeader((st.Size()-wavHeaderSize)&^1), 0)
	return err
}

// mp4Finished reports whether an MP4 has its index (the moov box), which a recording output
// writes when it finishes the file: without it the file does not play.
func mp4Finished(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	var off int64
	hdr := make([]byte, 16)
	for i := 0; i < 1000 && off+8 <= st.Size(); i++ {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return false
		}
		size, typ := int64(binary.BigEndian.Uint32(hdr)), string(hdr[4:8])
		switch size {
		case 0: // to the end of the file
			size = st.Size() - off
		case 1: // a 64-bit size follows
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return false
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
		}
		if typ == "moov" {
			return off+size <= st.Size()
		}
		if size < 8 {
			return false
		}
		off += size
	}
	return false
}
