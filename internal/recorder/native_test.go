package recorder

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// What the macOS app writes, made by hand: an MP4 is a list of boxes, and a finished one has
// its index (moov).
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:], typ)
	return append(b, payload...)
}

func mp4(finished bool, media int) []byte {
	b := append(box("ftyp", []byte("isom\x00\x00\x02\x00")), box("mdat", bytes.Repeat([]byte{7}, media))...)
	if finished {
		b = append(b, box("moov", box("mvhd", make([]byte, 100)))...)
	}
	return b
}

// appWAV is a 16 kHz WAV of ms of sound; stale leaves the header at 0 samples (the app died
// between two header updates).
func appWAV(ms int, stale bool) []byte {
	data := pcm(ms)
	n := int64(len(data))
	if stale {
		n = 0
	}
	return append(wavHeader(n), data...)
}

func write(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil { // as the app's writer would, before the daemon tightens it
		t.Fatal(err)
	}
}

func beginNative(t *testing.T, s *Service, mic, sys bool) model.Recording {
	t.Helper()
	rec, err := s.Begin(BeginRequest{Kind: model.RecordingScreen, Source: model.RecordingSourceNative, Mic: mic, SystemAudio: sys})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func report(t *testing.T, s *Service, id string, rep NativeReport) model.Recording {
	t.Helper()
	r, err := s.Native(id, rep)
	if err != nil {
		t.Fatalf("report %+v: %v", rep, err)
	}
	return r
}

func TestNativeBeginValidates(t *testing.T) {
	r := newRig(t)
	for _, bad := range []BeginRequest{
		{Kind: model.RecordingAudio, Mic: true, Source: model.RecordingSourceNative},
		{Kind: model.RecordingScreen, Mime: "video/webm", Source: model.RecordingSourceNative},
		{Kind: model.RecordingScreen, Mime: "video/mp4", Source: "elsewhere"},
	} {
		if _, err := r.s.Begin(bad); fleet.KindOf(err) != fleet.KindInvalid {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	rec := beginNative(t, r.s, false, true)
	if rec.Source != model.RecordingSourceNative || rec.Mime != NativeMime || !rec.SystemAudio || rec.Mic || rec.Stopping {
		t.Fatalf("begin: %+v", rec)
	}
	stored, err := r.st.GetRecording(rec.ID)
	if err != nil || stored.Source != model.RecordingSourceNative || !stored.SystemAudio {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	// the app writes the files: chunks are refused, and nothing was created for it
	if _, err := r.s.Append(rec.ID, 1, TrackMedia, 0, 0, []byte("x")); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("chunk: %v", err)
	}
	if list, _ := os.ReadDir(rec.Dir); len(list) != 0 {
		t.Fatalf("files made for the app: %v", list)
	}
	// a web recording refuses native reports; a web one is "web"
	r2 := newRig(t)
	web := begin(t, r2.s, model.RecordingScreen, true)
	if web.Source != model.RecordingSourceWeb {
		t.Fatalf("web source %q", web.Source)
	}
	if _, err := r2.s.Native(web.ID, NativeReport{Segment: 1}); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("native report on a web recording: %v", err)
	}
	if _, err := r.s.Native(rec.ID, NativeReport{Closed: true}); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("closed without a segment: %v", err)
	}
	if _, err := r.s.Native(rec.ID, NativeReport{Segment: 2}); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("segment 2 first: %v", err)
	}
}

func TestNativeSegmentsFramesAndStop(t *testing.T) {
	r := newRig(t)
	rec := beginNative(t, r.s, true, true)
	v := report(t, r.s, rec.ID, NativeReport{Segment: 1}) // opens it; the app then writes the files
	seg := v.Segments[0]
	if len(v.Segments) != 1 || seg.File != filepath.Join(rec.Dir, "seg-0001.mp4") || seg.WAV != filepath.Join(rec.Dir, "seg-0001.wav") {
		t.Fatalf("open: %+v", v.Segments)
	}
	m1 := mp4(true, 5000)
	write(t, seg.File, m1[:3000]) // being written
	write(t, seg.WAV, appWAV(700, false))
	r.clk.add(2 * time.Second)
	v = report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 2000})
	if v.Bytes != 3000 || v.Segments[0].Bytes != 3000 || v.Segments[0].WAVBytes != wavHeaderSize+700*32 || v.Segments[0].SHA256 != "" {
		t.Fatalf("progress: %+v", v.Segments[0])
	}
	// frames: a good one is kept, one too large and one that isn't a JPEG are removed
	frames := filepath.Join(rec.Dir, "frames")
	write(t, filepath.Join(frames, "f-000000000.jpg"), append([]byte{0xff, 0xd8, 0xff, 0xe0}, make([]byte, 1000)...))
	write(t, filepath.Join(frames, "f-000030000.jpg"), append([]byte{0xff, 0xd8, 0xff}, make([]byte, nativeMaxFrameBytes)...))
	write(t, filepath.Join(frames, "f-000031000.jpg"), []byte("<html>"))
	report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 2100, Frames: []int64{0, 30000, 31000, 99000}})
	if list, _ := os.ReadDir(frames); len(list) != 1 || list[0].Name() != "f-000000000.jpg" {
		t.Fatalf("frames kept: %v", list)
	}
	// Windows does not expose POSIX permission bits; still verify the file type.
	if st, err := os.Stat(filepath.Join(frames, "f-000000000.jpg")); err != nil || !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("frame: %v %v", st, err)
	}
	// the app rotates: segment 1 is finished and closed, segment 2 opens
	write(t, seg.File, m1)
	write(t, seg.WAV, appWAV(1500, false))
	v = report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 1500, Closed: true})
	sum := sha256.Sum256(m1)
	if v.Segments[0].SHA256 != hex.EncodeToString(sum[:]) || v.Bytes != int64(len(m1)) {
		t.Fatalf("closed: %+v", v.Segments[0])
	}
	if st, err := os.Stat(seg.File); err != nil || !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("mp4: %v %v", st, err)
	}
	v = report(t, r.s, rec.ID, NativeReport{Segment: 2})
	m2 := mp4(true, 800)
	write(t, v.Segments[1].File, m2)
	write(t, v.Segments[1].WAV, appWAV(2000, false))
	r.clk.add(2 * time.Second)
	fin := report(t, r.s, rec.ID, NativeReport{Segment: 2, DurationMS: 2000, Closed: true, Last: true})
	if fin.State != model.RecordingDone || fin.Error != "" || len(fin.Segments) != 2 || fin.Bytes != int64(len(m1)+len(m2)) {
		t.Fatalf("finished: %+v", fin)
	}
	if fin.DurationMS != 3500 || fin.Segments[1].DurationMS != 2000 || fin.Segments[1].SHA256 == "" || fin.Stopping {
		t.Fatalf("durations and sums: %+v", fin)
	}
	if ev := r.lastEvent(t); ev.Data.(model.Recording).State != model.RecordingDone {
		t.Fatalf("event: %+v", ev)
	}
	if _, err := r.s.Native(rec.ID, NativeReport{}); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("report after the end: %v", err)
	}
	stored, _ := r.st.GetRecording(rec.ID)
	if stored.Segments[0].SHA256 != fin.Segments[0].SHA256 || stored.DurationMS != 3500 {
		t.Fatalf("stored: %+v", stored)
	}
}

func TestNativeStopAskedElsewhere(t *testing.T) {
	r := newRig(t)
	rec := beginNative(t, r.s, true, false)
	v := report(t, r.s, rec.ID, NativeReport{Segment: 1})
	write(t, v.Segments[0].File, mp4(false, 100))
	write(t, v.Segments[0].WAV, appWAV(3000, false))
	r.clk.add(3 * time.Second)
	// Stop in another window: the recording stops counting and says stopping, still live
	x, err := r.s.SetState(rec.ID, "stopped")
	if err != nil || x.State != model.RecordingActive || !x.Stopping || x.DurationMS != 3000 || x.ResumedAt != 0 {
		t.Fatalf("stop asked: %+v %v", x, err)
	}
	if ev := r.lastEvent(t); !ev.Data.(model.Recording).Stopping {
		t.Fatalf("event: %+v", ev)
	}
	r.clk.add(time.Second)
	if x, _ := r.s.SetState(rec.ID, "paused"); x.State != model.RecordingActive || x.DurationMS != 3000 { // ignored now
		t.Fatalf("pause while stopping: %+v", x)
	}
	if v := report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 3100}); !v.Stopping {
		t.Fatalf("the app's report must say stopping: %+v", v)
	}
	// the app finishes its last part and says so
	write(t, v.Segments[0].File, mp4(true, 100))
	fin := report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 3100, Closed: true, Last: true})
	if fin.State != model.RecordingDone || fin.Segments[0].Bytes == 0 || fin.Segments[0].SHA256 == "" || fin.DurationMS != 3000 {
		t.Fatalf("finished: %+v", fin)
	}

	// an app that never finishes: the sweep ends it after StaleAfter, with what is there
	rec = beginNative(t, r.s, true, false)
	v = report(t, r.s, rec.ID, NativeReport{Segment: 1})
	write(t, v.Segments[0].File, mp4(false, 100))
	write(t, v.Segments[0].WAV, appWAV(1000, false))
	r.s.SetState(rec.ID, "stopped")
	for range 3 {
		r.clk.add(8 * time.Second)
		report(t, r.s, rec.ID, NativeReport{Segment: 1}) // alive, but not finishing
		r.s.Sweep()
	}
	got, _ := r.s.Get(rec.ID)
	if got.State != model.RecordingDone || !strings.Contains(got.Error, "did not finish its last part") || !strings.Contains(got.Error, "video of part 1 is lost") ||
		got.Segments[0].Bytes != 0 || got.Segments[0].DurationMS != 1000 {
		t.Fatalf("late: %+v", got)
	}
}

func TestNativeInterruptedKeepsClosedParts(t *testing.T) {
	r := newRig(t)
	rec := beginNative(t, r.s, true, true)
	v := report(t, r.s, rec.ID, NativeReport{Segment: 1})
	write(t, v.Segments[0].File, mp4(true, 900))
	write(t, v.Segments[0].WAV, appWAV(1200, false))
	report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 1200, Closed: true})
	v = report(t, r.s, rec.ID, NativeReport{Segment: 2})
	write(t, v.Segments[1].File, mp4(false, 4000)) // the window process quit while writing it
	write(t, v.Segments[1].WAV, appWAV(800, true))
	r.clk.add(StaleAfter + time.Second)
	r.s.Sweep()
	got, _ := r.s.Get(rec.ID)
	if got.State != model.RecordingInterrupted || !strings.Contains(got.Error, "video of part 2 is lost") || !strings.Contains(got.Error, "its sound is kept") {
		t.Fatalf("interrupted: %+v", got)
	}
	if got.Segments[0].Bytes == 0 || got.Segments[0].SHA256 == "" || got.Segments[1].Bytes != 0 || got.Segments[1].SHA256 != "" {
		t.Fatalf("parts: %+v", got.Segments)
	}
	if got.DurationMS != 2000 || got.Bytes != got.Segments[0].Bytes {
		t.Fatalf("totals: %+v", got)
	}
	wav, _ := os.ReadFile(got.Segments[1].WAV)
	if n := binary.LittleEndian.Uint32(wav[40:]); n != 800*32 { // the header was fixed from the file
		t.Fatalf("wav data size %d", n)
	}
}

func TestNativeClosedButUnfinishedIsLost(t *testing.T) {
	r := newRig(t)
	rec := beginNative(t, r.s, true, false)
	v := report(t, r.s, rec.ID, NativeReport{Segment: 1})
	write(t, v.Segments[0].File, mp4(false, 500)) // the recording output failed: no index
	write(t, v.Segments[0].WAV, appWAV(900, false))
	if v := report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 900, Closed: true}); v.Segments[0].SHA256 != "" {
		t.Fatalf("an unfinished MP4 counted as closed: %+v", v.Segments[0])
	}
	fin := report(t, r.s, rec.ID, NativeReport{Last: true, Error: "the capture stopped: the disk is full"})
	if fin.State != model.RecordingDone || fin.Segments[0].Bytes != 0 || fin.DurationMS != 900 ||
		!strings.Contains(fin.Error, "disk is full") || !strings.Contains(fin.Error, "video of part 1 is lost") {
		t.Fatalf("finished: %+v", fin)
	}
}

func TestNativeLimitAsksTheAppToStop(t *testing.T) {
	r := newRig(t)
	r.cfg.MaxMinutes = 1
	rec := beginNative(t, r.s, false, true)
	v := report(t, r.s, rec.ID, NativeReport{Segment: 1})
	write(t, v.Segments[0].File, mp4(false, 100))
	r.clk.add(61 * time.Second)
	if v := report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 61000}); !v.Stopping || v.State != model.RecordingActive {
		t.Fatalf("over the limit: %+v", v)
	}
	write(t, v.Segments[0].File, mp4(true, 100))
	fin := report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 61000, Closed: true, Last: true})
	if fin.State != model.RecordingDone || !strings.Contains(fin.Error, "longest recording length") {
		t.Fatalf("finished: %+v", fin)
	}
	// the sweep asks as well, for an app that only sends heartbeats while paused
	rec = beginNative(t, r.s, false, true)
	r.clk.add(30 * time.Second)
	report(t, r.s, rec.ID, NativeReport{})
	r.clk.add(31 * time.Second)
	report(t, r.s, rec.ID, NativeReport{})
	r.s.Sweep()
	if got, _ := r.s.Get(rec.ID); !got.Stopping || !got.State.Live() {
		t.Fatalf("sweep over the limit: %+v", got)
	}
}

func TestNativePauseAndRestart(t *testing.T) {
	r := newRig(t)
	rec := beginNative(t, r.s, true, false)
	v := report(t, r.s, rec.ID, NativeReport{Segment: 1})
	write(t, v.Segments[0].File, mp4(true, 300))
	write(t, v.Segments[0].WAV, appWAV(1000, false))
	r.clk.add(time.Second)
	if x, err := r.s.SetState(rec.ID, "paused"); err != nil || x.State != model.RecordingPaused {
		t.Fatalf("pause: %+v %v", x, err)
	}
	report(t, r.s, rec.ID, NativeReport{Segment: 1, DurationMS: 1000, Closed: true}) // pause ends a part
	r.clk.add(time.Minute)
	report(t, r.s, rec.ID, NativeReport{}) // a heartbeat while paused
	if x, err := r.s.SetState(rec.ID, "recording"); err != nil || x.State != model.RecordingActive {
		t.Fatalf("resume: %+v %v", x, err)
	}
	v = report(t, r.s, rec.ID, NativeReport{Segment: 2})
	write(t, v.Segments[1].File, mp4(false, 300))
	write(t, v.Segments[1].WAV, appWAV(500, false))
	// the daemon restarts: the session comes back from the rows and the files
	r.s.Close()
	r.s = r.service()
	if err := r.s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.s.Close)
	live := r.s.Active()
	if live == nil || live.Source != model.RecordingSourceNative || live.Segments[0].SHA256 == "" || live.Segments[1].Bytes == 0 {
		t.Fatalf("reloaded: %+v", live)
	}
	write(t, v.Segments[1].File, mp4(true, 300))
	fin := report(t, r.s, rec.ID, NativeReport{Segment: 2, DurationMS: 500, Closed: true, Last: true})
	if fin.State != model.RecordingDone || fin.DurationMS != 1500 || fin.Segments[1].SHA256 == "" || fin.Error != "" {
		t.Fatalf("finished: %+v", fin)
	}
}
