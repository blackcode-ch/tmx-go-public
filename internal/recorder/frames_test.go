package recorder

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// testJPEG is a w×h JPEG.
func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, x%h, color.RGBA{200, 30, 30, 255})
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCameraAndDeviceAudio(t *testing.T) {
	r := newRig(t)
	// device audio without the microphone still gets the 16 kHz WAV
	rec, err := r.s.Begin(BeginRequest{Kind: model.RecordingScreen, SystemAudio: true, Mime: "video/webm;codecs=vp9,opus"})
	if err != nil || !rec.SystemAudio || rec.Mic {
		t.Fatalf("begin: %+v %v", rec, err)
	}
	mustAppend(t, r.s, rec.ID, 1, TrackMedia, 0, []byte("webm"))
	mustAppend(t, r.s, rec.ID, 1, TrackPCM, 0, pcm(500))
	fin, err := r.s.SetState(rec.ID, "stopped")
	if err != nil || fin.State != model.RecordingDone || fin.Segments[0].WAV == "" || fin.DurationMS != 500 || !fin.SystemAudio {
		t.Fatalf("stop: %+v %v", fin, err)
	}
	if got, _ := r.st.GetRecording(rec.ID); !got.SystemAudio {
		t.Fatal("system_audio was not stored")
	}
	// a camera clip
	cam, err := r.s.Begin(BeginRequest{Kind: model.RecordingCamera, Mic: true, Mime: "video/mp4;codecs=avc1,mp4a.40.2"})
	if err != nil || cam.Title != "Camera clip 28 Oct 14:03" || cam.SystemAudio {
		t.Fatalf("camera: %+v %v", cam, err)
	}
	mustAppend(t, r.s, cam.ID, 1, TrackMedia, 0, []byte("mp4"))
	if fin, _ := r.s.SetState(cam.ID, "stopped"); fin.Kind != model.RecordingCamera || fin.Segments[0].WAV == "" {
		t.Fatalf("camera stop: %+v", fin)
	}
	// without either there is no PCM track
	silent := begin(t, r.s, model.RecordingWindow, false)
	if _, err := r.s.Append(silent.ID, 1, TrackPCM, 0, 0, pcm(100)); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("pcm without sound: %v", err)
	}
}

func TestFrames(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingScreen, true)
	frame := testJPEG(t, 1280, 720)

	f, err := r.s.PutFrame(rec.ID, 0, frame)
	want := filepath.Join(rec.Dir, "frames", "f-000000000.jpg")
	if err != nil || f.Path != want || f.MS != 0 || f.Bytes != int64(len(frame)) {
		t.Fatalf("first frame: %+v %v", f, err)
	}
	// Windows does not expose POSIX permission bits; still verify the file type.
	if st, err := os.Stat(want); err != nil || !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("file: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Dir(want)); err != nil || !st.IsDir() || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o700) {
		t.Fatalf("folder: %v %v", st, err)
	}
	r.clk.add(30 * time.Second)
	if _, err := r.s.PutFrame(rec.ID, 30_000, testJPEG(t, 720, 1280)); err != nil { // portrait
		t.Fatal(err)
	}
	if _, err := r.s.PutFrame(rec.ID, 30_000, frame); err != nil { // a retry replaces it
		t.Fatal(err)
	}

	for _, bad := range []struct {
		ms   int64
		data []byte
	}{
		{1000, []byte("not a jpeg")},
		{1000, testJPEG(t, 1281, 720)},
		{1000, append([]byte{0xff, 0xd8, 0xff}, make([]byte, MaxFrameBytes)...)},
		{-1, frame},
		{1_000_000_000, frame},
		{10 * 60_000, frame}, // far ahead of the 30 s recorded so far
	} {
		if _, err := r.s.PutFrame(rec.ID, bad.ms, bad.data); fleet.KindOf(err) != fleet.KindInvalid {
			t.Fatalf("frame at %d (%d bytes): %v", bad.ms, len(bad.data), err)
		}
	}
	if _, err := r.s.PutFrame("nope", 0, frame); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("unknown recording: %v", err)
	}

	got, _ := r.s.Get(rec.ID)
	if len(got.Frames) != 2 || got.Frames[0].MS != 0 || got.Frames[1].MS != 30_000 || got.Frames[1].Path != filepath.Join(rec.Dir, "frames", "f-000030000.jpg") {
		t.Fatalf("frames: %+v", got.Frames)
	}
	if p, err := r.s.FrameFile(rec.ID, 30_000); err != nil || p != got.Frames[1].Path {
		t.Fatalf("frame file: %s %v", p, err)
	}
	if _, err := r.s.FrameFile(rec.ID, 1); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("missing frame: %v", err)
	}

	// a frame keeps the recording alive (like a heartbeat)
	r.clk.add(StaleAfter - time.Second)
	if _, err := r.s.PutFrame(rec.ID, 59_000, frame); err != nil {
		t.Fatal(err)
	}
	r.clk.add(StaleAfter - time.Second)
	r.s.Sweep()
	if a := r.s.Active(); a == nil {
		t.Fatal("swept although a frame arrived")
	}

	// the limit: at most MaxFrames files (a temporary or foreign file does not count)
	dir := filepath.Dir(want)
	os.WriteFile(filepath.Join(dir, ".frame-123"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	for ms := int64(3); len(r.s.frames(rec.ID)) < MaxFrames; ms++ {
		if err := os.WriteFile(filepath.Join(dir, FrameName(ms)), frame, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.s.PutFrame(rec.ID, 60_000, frame); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("frame %d: %v", MaxFrames+1, err)
	}
	if _, err := r.s.PutFrame(rec.ID, 30_000, frame); err != nil { // replacing one is fine
		t.Fatalf("replace at the limit: %v", err)
	}

	// finished: no more frames, but they stay readable
	fin, _ := r.s.SetState(rec.ID, "stopped")
	if len(fin.Frames) != MaxFrames {
		t.Fatalf("finished with %d frames", len(fin.Frames))
	}
	if _, err := r.s.PutFrame(rec.ID, 61_000, frame); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("frame after stop: %v", err)
	}
	if list, _ := r.s.List(0); len(list) != 1 || len(list[0].Frames) != MaxFrames {
		t.Fatalf("list: %+v", list)
	}

	// a voice recording has none
	voice := begin(t, r.s, model.RecordingAudio, true)
	if _, err := r.s.PutFrame(voice.ID, 0, frame); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("voice frame: %v", err)
	}
}

func TestFrameName(t *testing.T) {
	if n := FrameName(30_000); n != "f-000030000.jpg" {
		t.Fatal(n)
	}
	if n := FrameName(maxFrameMS); n != "f-999999999.jpg" || !frameRx.MatchString(n) {
		t.Fatal(n)
	}
}

// A key moment (the window saw the picture change) is marked next to its JPEG and listed as one.
func TestKeyFrames(t *testing.T) {
	r := newRig(t)
	rec := begin(t, r.s, model.RecordingScreen, true)
	frame := testJPEG(t, 640, 360)
	if _, err := r.s.PutFrame(rec.ID, 0, frame); err != nil {
		t.Fatal(err)
	}
	r.clk.add(20 * time.Second)
	f, err := r.s.PutKeyFrame(rec.ID, 12_345, frame, true)
	if err != nil || !f.Key {
		t.Fatalf("key frame: %+v %v", f, err)
	}
	if _, err := os.Stat(filepath.Join(rec.Dir, "frames", "f-000012345.key")); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if _, err := r.s.PutFrame(rec.ID, 12_345, frame); err != nil { // a retry without the flag keeps it a key moment
		t.Fatal(err)
	}
	got, err := r.s.Get(rec.ID)
	if err != nil || len(got.Frames) != 2 || got.Frames[0].Key || !got.Frames[1].Key || got.Frames[1].MS != 12_345 {
		t.Fatalf("frames: %+v %v", got.Frames, err)
	}
	if _, err := r.s.FrameFile(rec.ID, 12_345); err != nil {
		t.Fatalf("the JPEG is served as before: %v", err)
	}
}
