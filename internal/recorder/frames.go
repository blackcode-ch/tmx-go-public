package recorder

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"image/jpeg"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Still frames for the summaries (DESIGN.md "Recorder"): a JPEG of the screen or camera at the
// start, every 30 s of recorded time and at stop, kept as recordings/<id>/frames/f-<ms>.jpg
// with the recorded time (pauses excluded) in 9 digits: f-000030000.jpg is at 0:30. The files
// are the list (no table), so a recorder that writes its own frames into the folder (the
// native one) needs nothing else; the window uploads them through PutFrame.
const (
	FramesDir     = "frames"
	MaxFrameBytes = 600 << 10 // one JPEG
	MaxFrames     = 400       // per recording
	MaxFrameSide  = 1280      // pixels, the longer side
	maxFrameMS    = 999_999_999
	// frameLead is how far a frame's time may be ahead of the daemon's own count of the
	// recorded time (the window's clock and the daemon's differ a little).
	frameLead = 2 * time.Minute
)

var (
	frameRx = regexp.MustCompile(`^f-(\d{9})\.jpg$`)
	keyRx   = regexp.MustCompile(`^f-(\d{9})\.key$`) // a key moment's marker (PutKeyFrame)
)

func keyName(ms int64) string { return fmt.Sprintf("f-%09d.key", ms) }

// FrameName is the file name of the frame at ms of recorded time: f-000030000.jpg.
func FrameName(ms int64) string { return fmt.Sprintf("f-%09d.jpg", ms) }

// PutFrame stores one JPEG of the live screen or camera recording id at ms of recorded time; a
// frame at the same time again (a retry) replaces it. It counts as a sign of life.
func (s *Service) PutFrame(id string, ms int64, data []byte) (model.RecordingFrame, error) {
	return s.PutKeyFrame(id, ms, data, false)
}

// PutKeyFrame is PutFrame for a key moment when key is set: the window saw the picture change
// (another page, another window). An empty f-<ms>.key next to the JPEG marks it, so the files
// stay the list; the report shows the key moments.
func (s *Service) PutKeyFrame(id string, ms int64, data []byte, key bool) (model.RecordingFrame, error) {
	if ms < 0 || ms > maxFrameMS {
		return model.RecordingFrame{}, invalid("a frame's time is 0 to %d ms", int64(maxFrameMS))
	}
	if len(data) > MaxFrameBytes {
		return model.RecordingFrame{}, invalid("a frame is at most %d KB", MaxFrameBytes>>10)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return model.RecordingFrame{}, invalid("a frame must be a JPEG")
	}
	if cfg.Width > MaxFrameSide || cfg.Height > MaxFrameSide || cfg.Width < 1 || cfg.Height < 1 {
		return model.RecordingFrame{}, invalid("a frame is at most %d pixels on its longer side (this one is %d×%d)", MaxFrameSide, cfg.Width, cfg.Height)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.liveLocked(id)
	if err != nil {
		return model.RecordingFrame{}, err
	}
	if sess.rec.Kind == model.RecordingAudio {
		return model.RecordingFrame{}, invalid("a voice recording has no frames")
	}
	now := s.o.Now()
	sess.last = now
	if ms > elapsed(sess.rec, now)+frameLead.Milliseconds() {
		return model.RecordingFrame{}, invalid("a frame at %d ms is ahead of the recording (%d ms so far)", ms, elapsed(sess.rec, now))
	}
	dir := filepath.Join(s.recDir(id), FramesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return model.RecordingFrame{}, err
	}
	path := filepath.Join(dir, FrameName(ms))
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if n := len(s.frames(id)); n >= MaxFrames {
			return model.RecordingFrame{}, conflict("this recording has its %d frames already", n)
		}
	}
	if err := writeFileAtomic(path, data); err != nil {
		return model.RecordingFrame{}, err
	}
	if key {
		if err := os.WriteFile(filepath.Join(dir, keyName(ms)), nil, 0o600); err != nil {
			return model.RecordingFrame{}, err
		}
	}
	return model.RecordingFrame{MS: ms, Path: path, Bytes: int64(len(data)), Key: key}, nil
}

// FrameFile returns the path of recording id's frame at ms.
func (s *Service) FrameFile(id string, ms int64) (string, error) {
	if !idRx.MatchString(id) {
		return "", notFound("no recording %q", id)
	}
	if ms < 0 || ms > maxFrameMS {
		return "", notFound("no frame at %d ms", ms)
	}
	s.mu.Lock()
	live := s.live != nil && s.live.rec.ID == id
	s.mu.Unlock()
	if !live {
		if _, err := s.o.Store.GetRecording(id); errors.Is(err, store.ErrNotFound) {
			return "", notFound("no recording %q", id)
		} else if err != nil {
			return "", err
		}
	}
	path := filepath.Join(s.recDir(id), FramesDir, FrameName(ms))
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return "", notFound("recording %s has no frame at %d ms", id, ms)
	}
	return path, nil
}

// frames lists the frames in recording id's folder, oldest first (nil when there are none).
func (s *Service) frames(id string) []model.RecordingFrame {
	dir := filepath.Join(s.recDir(id), FramesDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	keys := map[string]bool{}
	for _, e := range ents {
		if m := keyRx.FindStringSubmatch(e.Name()); m != nil {
			keys[m[1]] = true
		}
	}
	var out []model.RecordingFrame
	for _, e := range ents {
		m := frameRx.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue // a temporary file, or something else
		}
		ms, _ := strconv.ParseInt(m[1], 10, 64)
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		out = append(out, model.RecordingFrame{MS: ms, Path: filepath.Join(dir, e.Name()), Bytes: size, Key: keys[m[1]]})
	}
	slices.SortFunc(out, func(a, b model.RecordingFrame) int { return cmp.Compare(a.MS, b.MS) })
	return out
}

// writeFileAtomic writes data to a temporary file (0600) next to path and renames it there,
// so a reader never sees half a JPEG.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".frame-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
