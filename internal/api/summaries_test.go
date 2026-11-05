package api_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/summarize"
	"github.com/blackcode-ch/tmx-go/internal/summarize/summarizetest"
	"github.com/blackcode-ch/tmx-go/internal/transcribe/transcribetest"
)

// stoppedRecording makes a 1 s microphone recording through the routes and stops it.
func stoppedRecording(t *testing.T, d *apitest.Daemon, owner client) model.Recording {
	t.Helper()
	var rec model.Recording
	if s := owner.do("POST", "/api/recordings", map[string]any{"kind": "audio", "mic": true, "mime": "audio/webm", "title": "Standup"}, &rec); s != 201 {
		t.Fatalf("begin: %d", s)
	}
	if s, b, _ := raw(t, "PUT", chunkURL(d.URL, rec.ID, 1, "media", 0), d.OwnerToken, []byte("media"), nil); s != 200 {
		t.Fatalf("media: %d %s", s, b)
	}
	if s, b, _ := raw(t, "PUT", chunkURL(d.URL, rec.ID, 1, "pcm", 0), d.OwnerToken, bytes.Repeat([]byte{0, 1}, 16000), nil); s != 200 {
		t.Fatalf("pcm: %d %s", s, b)
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/state", map[string]string{"state": "stopped"}, &rec); s != 200 || rec.State != model.RecordingDone {
		t.Fatalf("stop: %d %+v", s, rec)
	}
	return rec
}

func waitSummary(t *testing.T, c client, id string, done func(model.RecordingSummary) bool) model.RecordingSummary {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var x model.RecordingSummary
	for time.Now().Before(deadline) {
		if s := c.do("GET", "/api/recordings/"+id+"/summary", nil, &x); s != 200 {
			t.Fatalf("summary: %d", s)
		}
		if done(x) {
			return x
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("summary: %+v", x)
	return x
}

func TestSummaryRoutes(t *testing.T) {
	d := apitest.New(t)
	summarizetest.Install(t, d.Home, "")
	transcribetest.Install(t, d.Home, "We ship on Thursday.")
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	on := true
	d.SetSummaries(config.SummaryConfig{Auto: &on})
	if s := owner.do("PUT", "/api/voice", map[string]any{"auto_transcribe": true}, nil); s != 200 {
		t.Fatalf("voice: %d", s)
	}

	rec := stoppedRecording(t, d, owner)
	x := waitSummary(t, owner, rec.ID, func(x model.RecordingSummary) bool { return !x.State.Pending() })
	if x.State != model.SummaryDone || x.Transcripts != 1 || x.Model != summarize.DefaultModel || len(x.Chapters) != 2 || x.Chapters[1].TMS != 999 || x.Chapters[1].Segment != 1 { // 0:02 is past the 1 s end
		t.Fatalf("summary: %+v", x)
	}
	if log := summarizetest.Log(d.Home); !strings.Contains(log, "--tools") || !strings.Contains(log, "--strict-mcp-config") {
		t.Fatalf("claude args: %s", log)
	}

	// lists and reads carry the summary, for the owner and agents
	for _, c := range []client{owner, agent} {
		var list []model.Recording
		if s := c.do("GET", "/api/recordings", nil, &list); s != 200 || len(list) != 1 || list[0].SummaryState != model.SummaryDone ||
			!strings.HasPrefix(list[0].Summary, "The owner walks through a checkout bug.") {
			t.Fatalf("list: %d %+v", s, list)
		}
		var one model.Recording
		if s := c.do("GET", "/api/recordings/"+rec.ID, nil, &one); s != 200 || one.Summary != list[0].Summary {
			t.Fatalf("get: %d %+v", s, one)
		}
	}
	if s := agent.do("GET", "/api/recordings/"+rec.ID+"/summary", nil, &x); s != 200 || x.State != model.SummaryDone {
		t.Fatalf("agent read: %d", s)
	}

	// Regenerate: owner only
	if s := agent.do("POST", "/api/recordings/"+rec.ID+"/summarize", nil, nil); s != 403 {
		t.Fatalf("agent regenerate: %d", s)
	}
	if s := owner.do("POST", "/api/recordings/"+rec.ID+"/summarize", nil, &x); s != 202 || x.State != model.SummaryTranscribing || x.Summary == "" {
		t.Fatalf("regenerate: %d %+v", s, x)
	}
	waitSummary(t, owner, rec.ID, func(x model.RecordingSummary) bool { return x.State == model.SummaryDone })
	if n := strings.Count(summarizetest.Log(d.Home), "-p --output-format json"); n != 2 {
		t.Fatalf("%d claude runs", n)
	}
	if s := owner.do("GET", "/api/recordings/nope/summary", nil, nil); s != 404 {
		t.Fatalf("unknown: %d", s)
	}
	if s := owner.do("POST", "/api/recordings/nope/summarize", nil, nil); s != 404 {
		t.Fatalf("unknown regenerate: %d", s)
	}

	// settings: owner only
	var st summarize.Settings
	if s := agent.do("GET", "/api/summaries/settings", nil, nil); s != 403 {
		t.Fatalf("agent settings: %d", s)
	}
	if s := owner.do("GET", "/api/summaries/settings", nil, &st); s != 200 || !st.Auto || st.Model != summarize.DefaultModel ||
		st.Claude != summarizetest.Path(d.Home) || !st.Voice || !st.AutoTranscribe {
		t.Fatalf("settings: %d %+v", s, st)
	}
	if s := owner.do("PUT", "/api/summaries/settings", map[string]any{"auto": false, "model": "claude-sonnet-5-5"}, &st); s != 200 || st.Auto || st.Model != "claude-sonnet-5-5" {
		t.Fatalf("put: %d %+v", s, st)
	}
	if s := owner.do("PUT", "/api/summaries/settings", map[string]any{"model": "$(reboot)"}, nil); s != 400 {
		t.Fatalf("bad model: %d", s)
	}

	// deleting the recording deletes its summary
	if s := owner.do("DELETE", "/api/recordings/"+rec.ID, nil, nil); s != 200 {
		t.Fatalf("delete: %d", s)
	}
	if s := owner.do("GET", "/api/recordings/"+rec.ID+"/summary", nil, nil); s != 404 {
		t.Fatalf("summary of a deleted recording: %d", s)
	}
}

func TestSummaryOffByDefaultHere(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	rec := stoppedRecording(t, d, owner)
	time.Sleep(150 * time.Millisecond)
	var x model.RecordingSummary
	if s := owner.do("GET", "/api/recordings/"+rec.ID+"/summary", nil, &x); s != 200 || x.State != model.SummaryNone || x.KeyPoints == nil {
		t.Fatalf("summary: %d %+v", s, x)
	}
	var list []model.Recording
	if s := owner.do("GET", "/api/recordings", nil, &list); s != 200 || list[0].SummaryState != "" || list[0].Summary != "" {
		t.Fatalf("list: %+v", list)
	}
}
