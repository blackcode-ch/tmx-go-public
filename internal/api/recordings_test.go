package api_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// raw sends body as raw bytes and returns the status and the answer.
func raw(t *testing.T, method, u, token string, body []byte, hdr map[string]string) (int, []byte, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, u, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func chunkURL(base, id string, n int, track string, off int64) string {
	return base + "/api/recordings/" + id + "/segments/" + strconv.Itoa(n) + "?track=" + track + "&offset=" + strconv.FormatInt(off, 10)
}

func TestRecordingRoutes(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}

	var rec model.Recording
	if s := agent.do("POST", "/api/recordings", map[string]any{"kind": "audio", "mic": true, "mime": "audio/webm"}, nil); s != 403 {
		t.Fatalf("an agent began a recording: %d", s)
	}
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "audio", "mic": true, "mime": "audio/webm;codecs=opus", "title": "Standup"}, &rec); s != 201 ||
		rec.State != model.RecordingActive || rec.Title != "Standup" {
		t.Fatalf("begin: %d %+v", s, rec)
	}
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "audio", "mic": true, "mime": "audio/webm"}, nil); s != 409 {
		t.Fatalf("second recording: %d", s)
	}
	var act struct {
		Recording *model.Recording
		Settings  struct{ FPS, VideoKbps int } `json:"settings"`
	}
	if s := owner.do("GET", "/api/recordings/active", nil, &act); s != 200 || act.Recording == nil || act.Recording.ID != rec.ID || act.Settings.FPS != 15 {
		t.Fatalf("active: %d %+v", s, act)
	}
	if s := agent.do("GET", "/api/recordings/active", nil, nil); s != 403 {
		t.Fatalf("agent active: %d", s)
	}

	// chunks: raw (http transport) and base64 (bridge), idempotent, gaps refused
	media := []byte("\x1a\x45\xdf\xa3 opus frames ...")
	if s, b, _ := raw(t, "PUT", chunkURL(d.URL, rec.ID, 1, "media", 0), d.OwnerToken, media, nil); s != 200 || !strings.Contains(string(b), `"size":`+strconv.Itoa(len(media))) {
		t.Fatalf("put: %d %s", s, b)
	}
	if s, _, _ := raw(t, "PUT", chunkURL(d.URL, rec.ID, 1, "media", 0), d.AgentToken(t, "head"), media, nil); s != 403 {
		t.Fatalf("agent chunk: %d", s)
	}
	if s, _, _ := raw(t, "PUT", chunkURL(d.URL, rec.ID, 1, "media", 0), "", media, nil); s != 401 {
		t.Fatalf("no token: %d", s)
	}
	if s, b, _ := raw(t, "PUT", chunkURL(d.URL, rec.ID, 1, "media", 999), d.OwnerToken, media, nil); s != 409 || !strings.Contains(string(b), `"size":`+strconv.Itoa(len(media))) {
		t.Fatalf("gap: %d %s", s, b)
	}
	pcm := bytes.Repeat([]byte{0, 1}, 1600) // 100 ms
	var sz struct{ Size int64 }
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/chunks", map[string]any{"segment": 1, "track": "pcm", "offset": 0,
		"data": base64.StdEncoding.EncodeToString(pcm)}, &sz); s != 200 || sz.Size != int64(len(pcm)) {
		t.Fatalf("bridge chunk: %d %+v", s, sz)
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/state", map[string]string{"state": "paused"}, &rec); s != 200 || rec.State != model.RecordingPaused {
		t.Fatalf("pause: %d %+v", s, rec)
	}
	if s := agent.do("POST", "/api/recordings/"+rec.ID+"/state", map[string]string{"state": "stopped"}, nil); s != 403 {
		t.Fatalf("agent stop: %d", s)
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/state", map[string]string{"state": "stopped"}, &rec); s != 200 || rec.State != model.RecordingDone ||
		rec.DurationMS != 100 || rec.Segments[0].SHA256 == "" {
		t.Fatalf("stop: %d %+v", s, rec)
	}
	if s := owner.do("GET", "/api/recordings/active", nil, &act); s != 200 || act.Recording != nil {
		t.Fatalf("active after stop: %+v", act)
	}

	// agents read the list and the paths (the owner's decision)
	var list []model.Recording
	if s := agent.do("GET", "/api/recordings", nil, &list); s != 200 || len(list) != 1 || !strings.HasSuffix(list[0].Segments[0].WAV, "seg-0001.wav") {
		t.Fatalf("agent list: %d %+v", s, list)
	}
	if s := agent.do("GET", "/api/recordings/"+rec.ID, nil, &rec); s != 200 {
		t.Fatalf("agent get: %d", s)
	}

	// playback: the owner's token, or a ticket for this recording; Range works
	seg := d.URL + "/api/recordings/" + rec.ID + "/segments/1"
	if s, b, h := raw(t, "GET", seg, d.OwnerToken, nil, nil); s != 200 || !bytes.Equal(b, media) || h.Get("Content-Type") != "audio/webm" || h.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("owner play: %d %q %v", s, b, h)
	}
	if s, _, _ := raw(t, "GET", seg, d.AgentToken(t, "head"), nil, nil); s != 401 {
		t.Fatalf("agent play: %d", s)
	}
	if s, _, _ := raw(t, "GET", seg, "", nil, nil); s != 401 {
		t.Fatalf("play without anything: %d", s)
	}
	var tk struct {
		Ticket    string
		ExpiresAt int64 `json:"expires_at"`
	}
	if s := agent.do("POST", "/api/recordings/"+rec.ID+"/ticket", nil, nil); s != 403 {
		t.Fatalf("agent ticket: %d", s)
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/ticket", nil, &tk); s != 200 || tk.Ticket == "" || tk.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatalf("ticket: %d %+v", s, tk)
	}
	withTicket := seg + "?ticket=" + url.QueryEscape(tk.Ticket)
	if s, b, h := raw(t, "GET", withTicket, "", nil, map[string]string{"Range": "bytes=4-8"}); s != 206 || string(b) != string(media[4:9]) ||
		h.Get("Content-Range") != "bytes 4-8/"+strconv.Itoa(len(media)) {
		t.Fatalf("range: %d %q %v", s, b, h)
	}
	if s, b, h := raw(t, "GET", withTicket+"&track=pcm", "", nil, nil); s != 200 || len(b) != 44+len(pcm) || h.Get("Content-Type") != "audio/wav" {
		t.Fatalf("wav: %d %d %v", s, len(b), h)
	}
	if s, _, _ := raw(t, "GET", seg+"?ticket=x"+url.QueryEscape(tk.Ticket), "", nil, nil); s != 401 {
		t.Fatalf("bad ticket: %d", s)
	}
	if s, _, _ := raw(t, "PUT", chunkURL(d.URL, rec.ID, 1, "media", 0)+"&ticket="+url.QueryEscape(tk.Ticket), "", media, nil); s != 401 {
		t.Fatalf("a ticket wrote: %d", s)
	}
	var b64 struct {
		Type string
		Data string
	}
	if s := owner.do("GET", "/api/recordings/"+rec.ID+"/segments/1?data=1", nil, &b64); s != 200 || b64.Data != base64.StdEncoding.EncodeToString(media) {
		t.Fatalf("bridge play: %d %+v", s, b64)
	}

	// transcription without a transcriber
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/transcribe", nil, nil); s != 503 {
		t.Fatalf("transcribe without voice: %d", s)
	}

	// rename, delete
	if s := agent.do("PATCH", "/api/recordings/"+rec.ID, map[string]string{"title": "x"}, nil); s != 403 {
		t.Fatalf("agent rename: %d", s)
	}
	if s := owner.do("PATCH", "/api/recordings/"+rec.ID, map[string]string{"title": "Retro"}, &rec); s != 200 || rec.Title != "Retro" {
		t.Fatalf("rename: %d %+v", s, rec)
	}
	if s := agent.do("DELETE", "/api/recordings/"+rec.ID, nil, nil); s != 403 {
		t.Fatalf("agent delete: %d", s)
	}
	if s := owner.do("DELETE", "/api/recordings/"+rec.ID, nil, nil); s != 200 {
		t.Fatalf("delete: %d", s)
	}
	if s, _, _ := raw(t, "GET", withTicket, "", nil, nil); s != 404 {
		t.Fatalf("play deleted: %d", s)
	}
}

type fakeVoice struct {
	mu   sync.Mutex
	jobs []string
}

func (f *fakeVoice) Enqueue(source, wav, lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, source+"|"+lang)
	return nil
}
func (f *fakeVoice) AutoTranscribe() bool { return false }

func TestRecordingTranscribeAndTicketExpiry(t *testing.T) {
	d := apitest.New(t)
	// a recorder whose clock the test moves, behind its own server
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	st, err := store.Open(filepath.Join(t.TempDir(), "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := recorder.New(recorder.Options{Store: st, Dir: t.TempDir(), Now: clock,
		DiskFree: func(string) (uint64, error) { return 1 << 40, nil }})
	srv := httptest.NewServer(api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken, Recorder: rec}).Handler())
	defer srv.Close()
	owner := client{t: t, base: srv.URL, token: d.OwnerToken}
	var r model.Recording
	owner.do("POST", "/api/recordings", map[string]any{"kind": "audio", "mic": true, "mime": "audio/mp4"}, &r)
	raw(t, "PUT", chunkURL(srv.URL, r.ID, 1, "media", 0), d.OwnerToken, []byte("m4a"), nil)
	raw(t, "PUT", chunkURL(srv.URL, r.ID, 1, "pcm", 0), d.OwnerToken, make([]byte, 320), nil)
	owner.do("POST", "/api/recordings/"+r.ID+"/state", map[string]string{"state": "stopped"}, &r)
	if !strings.HasSuffix(r.Segments[0].File, "seg-0001.m4a") {
		t.Fatalf("file: %s", r.Segments[0].File)
	}

	fv := &fakeVoice{}
	rec.SetTranscriber(fv)
	var q struct {
		Queued int
		Prefix string `json:"source_prefix"`
	}
	if s := owner.do("POST", "/api/recordings/"+r.ID+"/transcribe", map[string]string{"language": "de"}, &q); s != 202 || q.Queued != 1 ||
		q.Prefix != "recording:"+r.ID+"/" || fv.jobs[0] != "recording:"+r.ID+"/1|de" {
		t.Fatalf("transcribe: %d %+v %v", s, q, fv.jobs)
	}

	var tk struct{ Ticket string }
	owner.do("POST", "/api/recordings/"+r.ID+"/ticket", nil, &tk)
	play := srv.URL + "/api/recordings/" + r.ID + "/segments/1?ticket=" + url.QueryEscape(tk.Ticket)
	if s, _, _ := raw(t, "GET", play, "", nil, nil); s != 200 {
		t.Fatalf("fresh ticket: %d", s)
	}
	mu.Lock()
	now = now.Add(recorder.TicketTTL + time.Second)
	mu.Unlock()
	if s, _, _ := raw(t, "GET", play, "", nil, nil); s != 401 {
		t.Fatalf("expired ticket: %d", s)
	}
}

func TestShareRecordingToChat(t *testing.T) {
	net := p2ptest.NewNetwork()
	a, b := apitest.NewWithNode(t, net.NewNode("alex")), apitest.NewWithNode(t, net.NewNode("bo"))
	alex := client{t: t, base: a.URL, token: a.OwnerToken}
	bo := client{t: t, base: b.URL, token: b.OwnerToken}
	var inv struct{ Link string }
	alex.do("POST", "/api/invites", map[string]string{"kind": "dm"}, &inv)
	var joined chat.JoinResult
	bo.do("POST", "/api/invites/join", map[string]string{"link": inv.Link}, &joined)
	dm := joined.Chat.ID
	waitFor(t, "alex's DM", func() bool {
		var c model.Chat
		return alex.do("GET", "/api/chats/"+dm, nil, &c) == 200 && c.Status == model.ChatActive
	})

	var r model.Recording
	alex.do("POST", "/api/recordings", map[string]any{"kind": "window", "mime": "video/webm;codecs=vp9", "title": "Bug repro"}, &r)
	raw(t, "PUT", chunkURL(a.URL, r.ID, 1, "media", 0), a.OwnerToken, []byte("\x1a\x45\xdf\xa3 part one"), nil)
	if s := alex.do("POST", "/api/recordings/"+r.ID+"/share", map[string]string{"chat_id": dm}, nil); s != 409 {
		t.Fatalf("share while live: %d", s)
	}
	raw(t, "PUT", chunkURL(a.URL, r.ID, 2, "media", 0), a.OwnerToken, []byte("\x1a\x45\xdf\xa3 part two"), nil)
	alex.do("POST", "/api/recordings/"+r.ID+"/state", map[string]string{"state": "stopped"}, &r)

	agent := client{t: t, base: a.URL, token: a.AgentToken(t, "head")}
	if s := agent.do("POST", "/api/recordings/"+r.ID+"/share", map[string]string{"chat_id": dm}, nil); s != 403 {
		t.Fatalf("agent share: %d", s)
	}
	if s := alex.do("POST", "/api/recordings/"+r.ID+"/share", map[string]any{"chat_id": dm, "segments": []int{3}}, nil); s != 400 {
		t.Fatalf("unknown segment: %d", s)
	}
	var m model.ChatMessage
	// (a message's files come back in id order, so the names say which part is which)
	if s := alex.do("POST", "/api/recordings/"+r.ID+"/share", map[string]string{"chat_id": dm}, &m); s != 201 || len(m.Files) != 2 ||
		m.Files[0].Name+"|"+m.Files[1].Name != "Bug repro (part 1 of 2).webm|Bug repro (part 2 of 2).webm" &&
			m.Files[1].Name+"|"+m.Files[0].Name != "Bug repro (part 1 of 2).webm|Bug repro (part 2 of 2).webm" ||
		m.Files[1].Type != "video/webm" || !strings.HasPrefix(m.Body, "Recording: Bug repro (") {
		t.Fatalf("share: %d %+v", s, m)
	}
	waitFor(t, "bo has both parts", func() bool {
		var p chat.Page
		if bo.do("GET", "/api/chats/"+dm+"/messages", nil, &p) != 200 {
			return false
		}
		for _, x := range p.Messages {
			if x.ID == m.ID && len(x.Files) == 2 && x.Files[0].State == model.ChatFileReady && x.Files[1].State == model.ChatFileReady {
				return true
			}
		}
		return false
	})

	// one segment more than a message carries is refused
	alex.do("POST", "/api/recordings", map[string]any{"kind": "audio", "mic": true, "mime": "audio/webm"}, &r)
	for n := 1; n <= chat.MaxFilesPerMessage+1; n++ {
		raw(t, "PUT", chunkURL(a.URL, r.ID, n, "media", 0), a.OwnerToken, []byte("opus"), nil)
	}
	alex.do("POST", "/api/recordings/"+r.ID+"/state", map[string]string{"state": "stopped"}, &r)
	if s := alex.do("POST", "/api/recordings/"+r.ID+"/share", map[string]string{"chat_id": dm}, nil); s != 400 {
		t.Fatalf("11 segments: %d", s)
	}
	if s := alex.do("POST", "/api/recordings/"+r.ID+"/share", map[string]any{"chat_id": dm, "segments": []int{1, 2}}, &m); s != 201 || len(m.Files) != 2 {
		t.Fatalf("two of eleven: %d %+v", s, m)
	}
}

func TestRecordingsOffWithoutRecorder(t *testing.T) {
	d := apitest.New(t)
	srv := httptest.NewServer(api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken}).Handler())
	defer srv.Close()
	owner := client{t: t, base: srv.URL, token: d.OwnerToken}
	if s := owner.do("GET", "/api/recordings", nil, nil); s != 501 {
		t.Fatalf("no recorder: %d", s)
	}
}

// A recording is tagged "clinic" (patient data: its report is written by local models only) when it begins or
// later; the tag is stored, and anything else is refused.
func TestRecordingSensitivity(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var rec model.Recording
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "audio", "mic": true, "mime": "audio/webm", "sensitivity": "secret"}, nil); s != 400 {
		t.Fatalf("an unknown sensitivity: %d", s)
	}
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "audio", "mic": true, "mime": "audio/webm", "sensitivity": "clinic"}, &rec); s != 201 || !rec.Clinic() {
		t.Fatalf("begin clinic: %d %+v", s, rec)
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/state", map[string]string{"state": "stopped"}, nil); s != 200 {
		t.Fatalf("stop: %d", s)
	}
	var got model.Recording
	if s := owner.do("GET", "/api/recordings/"+rec.ID, nil, &got); s != 200 || got.Sensitivity != model.SensitivityClinic {
		t.Fatalf("stored: %d %q", s, got.Sensitivity)
	}
	got = model.Recording{} // omitempty: a fresh value, or the old tag would stay
	if s := owner.do("PATCH", "/api/recordings/"+rec.ID, map[string]string{"sensitivity": ""}, &got); s != 200 || got.Clinic() || got.ID != rec.ID {
		t.Fatalf("untag: %d %+v", s, got)
	}
	if s := owner.do("PATCH", "/api/recordings/"+rec.ID, map[string]string{"sensitivity": "clinic", "title": "Consultation"}, &got); s != 200 || !got.Clinic() || got.Title != "Consultation" {
		t.Fatalf("tag and rename: %d %+v", s, got)
	}
	if s := owner.do("PATCH", "/api/recordings/"+rec.ID, map[string]string{"sensitivity": "public"}, nil); s != 400 {
		t.Fatalf("an unknown sensitivity: %d", s)
	}
	if s := (client{t: t, base: d.URL, token: d.AgentToken(t, "head")}).do("PATCH", "/api/recordings/"+rec.ID, map[string]string{"sensitivity": ""}, nil); s != 403 {
		t.Fatalf("an agent may not untag: %d", s)
	}
}
