package api_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

// A native recording through the routes: the app begins it, writes the files and reports them.
func TestNativeRecordingRoutes(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}

	var rec model.Recording
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "screen", "source": "native", "mic": true, "system_audio": true}, &rec); s != 201 ||
		rec.Source != model.RecordingSourceNative || !rec.SystemAudio || rec.Mime != recorder.NativeMime {
		t.Fatalf("begin: %d %+v", s, rec)
	}
	if s := agent.do("POST", "/api/recordings/"+rec.ID+"/native", map[string]any{"segment": 1}, nil); s != 403 {
		t.Fatalf("an agent reported: %d", s)
	}
	var v model.Recording
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/native", map[string]any{"segment": 1}, &v); s != 200 || len(v.Segments) != 1 {
		t.Fatalf("open: %d %+v", s, v)
	}
	media, wav := recorder.NativeFiles(1)
	if v.Segments[0].File != filepath.Join(rec.Dir, media) || v.Segments[0].WAV != filepath.Join(rec.Dir, wav) {
		t.Fatalf("names: %+v", v.Segments[0])
	}
	// what SCRecordingOutput leaves: boxes, the index (moov) last
	mp4 := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0, 0, 0, 0, 12, 'm', 'd', 'a', 't', 1, 2, 3, 4, 0, 0, 0, 8, 'm', 'o', 'o', 'v'}
	os.WriteFile(v.Segments[0].File, mp4, 0o600)
	pcm := make([]byte, 32000) // a second
	os.WriteFile(v.Segments[0].WAV, append(recorder.WAVHeader(int64(len(pcm))), pcm...), 0o600)
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/chunks", map[string]any{"segment": 1, "track": "media", "data": "AAAA"}, nil); s != 409 {
		t.Fatalf("chunks for a native recording: %d", s)
	}
	// stop in another window: still live, stopping, until the app reports its last part
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/state", map[string]any{"state": "stopped"}, &v); s != 200 || !v.Stopping || !v.State.Live() {
		t.Fatalf("stop asked: %d %+v", s, v)
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/native", "", &v); s != 200 || !v.Stopping { // an empty body is a heartbeat
		t.Fatalf("heartbeat: %d %+v", s, v)
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/native", map[string]any{"segment": 1, "duration_ms": 1000, "closed": true, "last": true}, &v); s != 200 ||
		v.State != model.RecordingDone || v.DurationMS != 1000 || v.Bytes != int64(len(mp4)) || v.Segments[0].SHA256 == "" {
		t.Fatalf("last: %d %+v", s, v)
	}
	if b, _ := os.ReadFile(v.Segments[0].WAV); binary.LittleEndian.Uint32(b[40:]) != 32000 {
		t.Fatal("wav header")
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/native", map[string]any{}, nil); s != 409 {
		t.Fatalf("report after the end: %d", s)
	}
}
