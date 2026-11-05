package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/transcribe/transcribetest"
)

// testWAV is secs seconds of 16 kHz mono PCM16 synthetic signal.
func testWAV(secs int) []byte {
	n := secs * 32000
	b := make([]byte, 44+n)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n))
	for i := 44; i+1 < len(b); i += 2 {
		binary.LittleEndian.PutUint16(b[i:], 3000)
	}
	return b
}

func TestVoiceRoutesNeedTheOwner(t *testing.T) {
	d := apitest.New(t)
	owner := client{t, d.URL, d.OwnerToken}
	agent := client{t, d.URL, d.AgentToken(t, "head")}
	anon := client{t, d.URL, ""}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/voice"}, {"PUT", "/api/voice"}, {"POST", "/api/voice/setup"},
		{"POST", "/api/voice/models/base-q5_1"}, {"DELETE", "/api/voice/models/base-q5_1"},
		{"POST", "/api/transcribe"}, {"DELETE", "/api/transcripts/x"},
	} {
		if st := agent.do(r.method, r.path, map[string]any{}, nil); st != http.StatusForbidden {
			t.Errorf("agent %s %s: %d", r.method, r.path, st)
		}
		if st := anon.do(r.method, r.path, map[string]any{}, nil); st != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s: %d", r.method, r.path, st)
		}
	}
	var list []model.Transcript
	if st := agent.do("GET", "/api/transcripts", nil, &list); st != http.StatusOK || list == nil {
		t.Fatalf("agents read transcripts: %d %v", st, list)
	}
	var st struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}
	if code := owner.do("GET", "/api/voice", nil, &st); code != http.StatusOK || st.Ready || st.Reason == "" {
		t.Fatalf("status: %d %+v", code, st)
	}
	// Not set up: dictation explains.
	body := map[string]string{"data": base64.StdEncoding.EncodeToString(testWAV(1))}
	req, _ := json.Marshal(body)
	resp := rawPost(t, d, "application/json", bytes.NewReader(req))
	if resp.code != http.StatusConflict || !strings.Contains(resp.body, "isn't set up") {
		t.Fatalf("not set up: %d %s", resp.code, resp.body)
	}
}

type rawResp struct {
	code int
	body string
}

func rawPost(t *testing.T, d *apitest.Daemon, ctype string, body io.Reader) rawResp {
	t.Helper()
	req, _ := http.NewRequest("POST", d.URL+"/api/transcribe?language=de", body)
	req.Header.Set("Authorization", "Bearer "+d.OwnerToken)
	req.Header.Set("Content-Type", ctype)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return rawResp{resp.StatusCode, string(b)}
}

func TestDictationAndFiles(t *testing.T) {
	d := apitest.New(t)
	transcribetest.Install(t, d.Home, "hello from the fake engine")
	owner := client{t, d.URL, d.OwnerToken}
	var res struct {
		Text     string                    `json:"text"`
		Language string                    `json:"language"`
		Segments []model.TranscriptSegment `json:"segments"`
	}
	if st := owner.do("POST", "/api/transcribe", map[string]string{"data": base64.StdEncoding.EncodeToString(testWAV(2))}, &res); st != http.StatusOK {
		t.Fatalf("dictation: %d", st)
	}
	if res.Text != "hello from the fake engine" || res.Language != "en" || len(res.Segments) != 1 || res.Segments[0].ToMS != 1500 {
		t.Fatalf("%+v", res)
	}
	if st := owner.do("POST", "/api/transcribe", map[string]string{"data": "not base64!"}, nil); st != http.StatusBadRequest {
		t.Fatalf("bad base64: %d", st)
	}
	if st := owner.do("POST", "/api/transcribe", map[string]string{"data": base64.StdEncoding.EncodeToString([]byte("hello"))}, nil); st != http.StatusBadRequest {
		t.Fatalf("not a wav: %d", st)
	}
	if st := owner.do("POST", "/api/transcribe", map[string]string{"data": base64.StdEncoding.EncodeToString(testWAV(130))}, nil); st != http.StatusBadRequest {
		t.Fatalf("too long: %d", st)
	}
	// A file (tmx transcribe FILE): the raw body.
	r := rawPost(t, d, "audio/mpeg", strings.NewReader("ID3\x04\x00\x00\x00\x00\x00\x00 frames"))
	if r.code != http.StatusOK || !strings.Contains(r.body, "hello from the fake engine") {
		t.Fatalf("file: %d %s", r.code, r.body)
	}
	if r := rawPost(t, d, "application/octet-stream", strings.NewReader("<html>")); r.code != http.StatusBadRequest {
		t.Fatalf("garbage file: %d %s", r.code, r.body)
	}
	var list []model.Transcript
	owner.do("GET", "/api/transcripts", nil, &list)
	if len(list) != 0 {
		t.Fatalf("dictation was stored: %+v", list)
	}
}

func TestTranscriptsAPI(t *testing.T) {
	d := apitest.New(t)
	transcribetest.Install(t, d.Home, "budget meeting notes")
	owner := client{t, d.URL, d.OwnerToken}
	agent := client{t, d.URL, d.AgentToken(t, "head")}
	wav := filepath.Join(d.Home, "recordings", "r1", "seg-0001.wav")
	os.MkdirAll(filepath.Dir(wav), 0o700)
	os.WriteFile(wav, testWAV(1), 0o600)
	if err := d.Voice.Enqueue("recording:r1/1", wav, ""); err != nil {
		t.Fatal(err)
	}
	var list []model.Transcript
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		owner.do("GET", "/api/transcripts?q=budg&source_prefix=recording:r1/", nil, &list)
		if len(list) == 1 && list[0].State == model.TranscriptDone {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if len(list) != 1 || list[0].Text != "budget meeting notes" {
		t.Fatalf("search: %+v", list)
	}
	id := list[0].ID

	// The transcript's JSON, exactly (the recorder's page reads it).
	req, _ := http.NewRequest("GET", d.URL+"/api/transcripts/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+agent.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("agent read: %v %v", resp, err)
	}
	var raw map[string]json.RawMessage
	json.NewDecoder(resp.Body).Decode(&raw)
	resp.Body.Close()
	var keys []string
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if got := strings.Join(keys, ","); got != "created_at,error,id,language,model,segments,source,state,text,updated_at" {
		t.Fatalf("keys: %s", got)
	}
	var segs []map[string]any
	json.Unmarshal(raw["segments"], &segs)
	if len(segs) != 1 || segs[0]["from_ms"] != 0.0 || segs[0]["to_ms"] != 1500.0 || segs[0]["text"] != "budget meeting notes" {
		t.Fatalf("segments: %s", raw["segments"])
	}

	if st := owner.do("GET", "/api/transcripts?q=nothing+like+it", nil, &list); st != http.StatusOK || len(list) != 0 {
		t.Fatalf("no match: %d %+v", st, list)
	}
	if st := agent.do("DELETE", "/api/transcripts/"+id, nil, nil); st != http.StatusForbidden {
		t.Fatalf("agent delete: %d", st)
	}
	if st := owner.do("DELETE", "/api/transcripts/"+id, nil, nil); st != http.StatusOK {
		t.Fatalf("delete: %d", st)
	}
	if st := owner.do("GET", "/api/transcripts/"+id, nil, nil); st != http.StatusNotFound {
		t.Fatalf("after delete: %d", st)
	}
}

func TestVoiceSettingsAPI(t *testing.T) {
	d := apitest.New(t)
	owner := client{t, d.URL, d.OwnerToken}
	if st := owner.do("PUT", "/api/voice", map[string]any{"language": "klingon"}, nil); st != http.StatusBadRequest {
		t.Fatalf("bad language: %d", st)
	}
	if st := owner.do("POST", "/api/voice/models/huge", nil, nil); st != http.StatusBadRequest {
		t.Fatalf("unknown model: %d", st)
	}
	var st struct {
		Settings struct {
			Language       string `json:"language"`
			AutoTranscribe bool   `json:"auto_transcribe"`
		} `json:"settings"`
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if code := owner.do("PUT", "/api/voice", map[string]any{"language": "de", "auto_transcribe": true}, &st); code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	if st.Settings.Language != "de" || !st.Settings.AutoTranscribe || !d.Voice.AutoTranscribe() {
		t.Fatalf("%+v", st.Settings)
	}
	if len(st.Models) != 3 || st.Models[0].Name != "base-q5_1" || st.Models[0].Size != 59707625 {
		t.Fatalf("models: %+v", st.Models)
	}
}

func TestSilentDictationSaysCouldNotHear(t *testing.T) {
	d := apitest.New(t)
	wav := testWAV(1)
	clear(wav[44:])
	body, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(wav)})
	r := rawPost(t, d, "application/json", bytes.NewReader(body))
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "couldn't hear you") {
		t.Fatalf("silent dictation: %d %s", r.code, r.body)
	}
}
