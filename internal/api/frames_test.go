package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)), &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The still frames of a screen or camera recording: owner-only uploads (raw JPEG over http,
// base64 JSON over the bridge), checked for type, size, count and a live recording; files on
// disk; frames in the recording's JSON for the owner and agents; served with a ticket.
func TestRecordingFrames(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agentTok := d.AgentToken(t, "head")
	agent := client{t: t, base: d.URL, token: agentTok}

	var rec model.Recording
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "camera", "mic": true, "mime": "video/webm;codecs=vp8,opus"}, &rec); s != 201 || rec.Kind != model.RecordingCamera {
		t.Fatalf("begin camera: %d %+v", s, rec)
	}
	frames := d.URL + "/api/recordings/" + rec.ID + "/frames/"
	jpg := jpegOf(t, 640, 360)
	asJPEG := map[string]string{"Content-Type": "image/jpeg"}
	asJSON := map[string]string{"Content-Type": "application/json"}

	// owner-only
	if s, _, _ := raw(t, "PUT", frames+"0", agentTok, jpg, asJPEG); s != 403 {
		t.Fatalf("agent frame: %d", s)
	}
	if s, _, _ := raw(t, "PUT", frames+"0", "", jpg, asJPEG); s != 401 {
		t.Fatalf("no token: %d", s)
	}
	// raw JPEG (the http transport)
	var f model.RecordingFrame
	s, b, _ := raw(t, "PUT", frames+"0", d.OwnerToken, jpg, asJPEG)
	if s != 200 || json.Unmarshal(b, &f) != nil || f.MS != 0 || !strings.HasSuffix(f.Path, filepath.Join(rec.ID, "frames", "f-000000000.jpg")) {
		t.Fatalf("raw frame: %d %s", s, b)
	}
	if on, err := os.ReadFile(f.Path); err != nil || !bytes.Equal(on, jpg) {
		t.Fatalf("on disk: %v", err)
	}
	// base64 JSON (the desktop bridge sends everything as JSON)
	body, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(jpg)})
	if s, b, _ := raw(t, "PUT", frames+"1500", d.OwnerToken, body, asJSON); s != 200 || !strings.Contains(string(b), "f-000001500.jpg") {
		t.Fatalf("bridge frame: %d %s", s, b)
	}
	// a key moment (the screen changed): ?key=1, or {key: true} through the bridge; a retry replaces the JPEG
	if s, b, _ := raw(t, "PUT", frames+"1500?key=1", d.OwnerToken, jpg, asJPEG); s != 200 || !strings.Contains(string(b), `"key":true`) {
		t.Fatalf("key frame: %d %s", s, b)
	}
	body, _ = json.Marshal(map[string]any{"data": base64.StdEncoding.EncodeToString(jpg), "key": true})
	if s, b, _ := raw(t, "PUT", frames+"0", d.OwnerToken, body, asJSON); s != 200 || !strings.Contains(string(b), `"key":true`) {
		t.Fatalf("bridge key frame: %d %s", s, b)
	}
	var keyed model.Recording
	if s := owner.do("GET", "/api/recordings/"+rec.ID, nil, &keyed); s != 200 || len(keyed.Frames) != 2 || !keyed.Frames[0].Key || !keyed.Frames[1].Key {
		t.Fatalf("key frames in the recording: %d %+v", s, keyed.Frames)
	}
	// type and size
	for _, c := range []struct {
		name   string
		path   string
		body   []byte
		hdr    map[string]string
		status int
	}{
		{"not a jpeg", "2000", []byte("GIF89a..."), asJPEG, 400},
		{"png type", "2000", jpg, map[string]string{"Content-Type": "image/png"}, 415},
		{"too wide", "2000", jpegOf(t, 1920, 1080), asJPEG, 400},
		{"too large", "2000", append(append([]byte{}, jpg...), make([]byte, recorder.MaxFrameBytes)...), asJPEG, 413},
		{"bad base64", "2000", []byte(`{"data":"%%%"}`), asJSON, 400},
		{"bad time", "soon", jpg, asJPEG, 400},
		{"negative", "-5", jpg, asJPEG, 400},
	} {
		if s, b, _ := raw(t, "PUT", frames+c.path, d.OwnerToken, c.body, c.hdr); s != c.status || !strings.Contains(string(b), `"error"`) {
			t.Fatalf("%s: %d %s", c.name, s, b)
		}
	}
	// the count: at most 400 (the native recorder writes into the same folder, so files count)
	dir := filepath.Dir(f.Path)
	for ms := int64(2); ms < recorder.MaxFrames; ms++ {
		if err := os.WriteFile(filepath.Join(dir, recorder.FrameName(ms)), jpg, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if s, b, _ := raw(t, "PUT", frames+"3000", d.OwnerToken, jpg, asJPEG); s != 409 {
		t.Fatalf("frame 401: %d %s", s, b)
	}

	// frames are in the recording's JSON, for agents too
	var got model.Recording
	if s := agent.do("GET", "/api/recordings/"+rec.ID, nil, &got); s != 200 || len(got.Frames) != recorder.MaxFrames ||
		got.Frames[0].Path != f.Path || got.Frames[len(got.Frames)-1].MS != 1500 {
		t.Fatalf("agent get: %d %d frames", s, len(got.Frames))
	}

	// served to the owner, or with a ticket; never to an agent's token
	if s, b, h := raw(t, "GET", frames+"0", d.OwnerToken, nil, nil); s != 200 || !bytes.Equal(b, jpg) || h.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("owner get: %d %s", s, h)
	}
	if s, _, _ := raw(t, "GET", frames+"0", agentTok, nil, nil); s != 401 {
		t.Fatalf("agent get frame: %d", s)
	}
	var tk struct{ Ticket string }
	owner.do("POST", "/api/recordings/"+rec.ID+"/ticket", nil, &tk)
	if s, b, _ := raw(t, "GET", frames+"0?ticket="+url.QueryEscape(tk.Ticket), "", nil, nil); s != 200 || !bytes.Equal(b, jpg) {
		t.Fatalf("ticket get: %d", s)
	}
	if s, _, _ := raw(t, "GET", frames+"0?ticket=nope.bad", "", nil, nil); s != 401 {
		t.Fatalf("bad ticket: %d", s)
	}
	var data struct {
		Type string
		Data string
	}
	if s := owner.do("GET", "/api/recordings/"+rec.ID+"/frames/0?data=1", nil, &data); s != 200 || data.Type != "image/jpeg" ||
		data.Data != base64.StdEncoding.EncodeToString(jpg) {
		t.Fatalf("data=1: %d %s", s, data.Type)
	}
	if s, _, _ := raw(t, "GET", frames+"4000", d.OwnerToken, nil, nil); s != 404 {
		t.Fatalf("missing frame: %d", s)
	}

	// only while live
	owner.do("POST", "/api/recordings/"+rec.ID+"/state", map[string]string{"state": "stopped"}, &rec)
	if len(rec.Frames) != recorder.MaxFrames {
		t.Fatalf("stopped with %d frames", len(rec.Frames))
	}
	if s, b, _ := raw(t, "PUT", frames+"0", d.OwnerToken, jpg, asJPEG); s != 409 {
		t.Fatalf("frame after stop: %d %s", s, b)
	}
	// device audio is reported as the window says
	var scr model.Recording
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "screen", "mic": true, "system_audio": true, "mime": "video/webm"}, &scr); s != 201 || !scr.SystemAudio {
		t.Fatalf("screen with device audio: %d %+v", s, scr)
	}
}
