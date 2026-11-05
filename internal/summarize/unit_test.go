package summarize

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
	"github.com/blackcode-ch/tmx-go/internal/transcribe/transcribetest"
)

func TestParseAnswer(t *testing.T) {
	ok := []struct {
		name, in string
		check    func(model.RecordingSummary) bool
	}{
		{"plain", cannedAnswer, func(x model.RecordingSummary) bool {
			return len(x.KeyPoints) == 2 && len(x.ActionItems) == 2 && x.Chapters[1].TMS == 3000
		}},
		{"fenced with prose", "Sure!\n```json\n" + cannedAnswer + "\n```", func(x model.RecordingSummary) bool { return len(x.Decisions) == 1 }},
		{"trailing commas", `{"summary": "One. Two. Three.", "key_points": ["a", "b",], "chapters": [],}`, func(x model.RecordingSummary) bool {
			return len(x.KeyPoints) == 2
		}},
		{"other names and shapes", `{"Summary": ["First sentence.", "Second sentence."], "keyPoints": "- one\n- two",
			"Action Items": ["call Bo", {"owner": "Sam", "task": "ship it"}, {"who": "unknown", "what": "check logs"}],
			"chapters": [{"time": "1:02:03", "name": "Late"}, {"t_ms": 61000, "title": "Ms"}, {"start": 30, "title": "Seconds"}, {"title": "No time"}]}`,
			func(x model.RecordingSummary) bool {
				return x.Summary == "First sentence. Second sentence." && strings.Join(x.KeyPoints, "|") == "one|two" &&
					x.ActionItems[0].What == "call Bo" && x.ActionItems[1].Who == "Sam" && x.ActionItems[1].What == "ship it" && x.ActionItems[2].Who == "" &&
					len(x.Chapters) == 3 && x.Chapters[0].TMS == 3723000 && x.Chapters[1].TMS == 61000 && x.Chapters[2].TMS == 30000
			}},
		{"braces in strings", `{"summary": "They typed {\"a\": 1} into the box.", "key_points": []}`, func(x model.RecordingSummary) bool {
			return x.Summary == `They typed {"a": 1} into the box.`
		}},
		{"a broken object first", `{oops} then {"summary": "The real one is here."}`, func(x model.RecordingSummary) bool { return x.Summary == "The real one is here." }},
		{"prose", "The owner reviews the settings page and explains the voice options to a colleague in detail.", func(x model.RecordingSummary) bool {
			return strings.HasPrefix(x.Summary, "The owner reviews") && x.KeyPoints != nil && x.Chapters != nil
		}},
	}
	for _, c := range ok {
		x, err := parseAnswer(c.in)
		if err != nil || !c.check(x) {
			t.Errorf("%s: %+v %v", c.name, x, err)
		}
	}
	for _, bad := range []string{"", "No.", `{"summary": "cut off`, `{"key_points": ["no summary"]}`, `{"summary": ""}`} {
		if x, err := parseAnswer(bad); err == nil {
			t.Errorf("%q: %+v", bad, x)
		}
	}
	// caps
	long := `{"summary": "` + strings.Repeat("word ", 1000) + `", "key_points": [` + strings.Repeat(`"p",`, 40) + `"p"]}`
	x, err := parseAnswer(long)
	if err != nil || len([]rune(x.Summary)) > maxSummaryRunes || len(x.KeyPoints) != maxListItems {
		t.Fatalf("caps: %d %d %v", len(x.Summary), len(x.KeyPoints), err)
	}
}

func TestParseResult(t *testing.T) {
	r, ok := parseResult([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"hi","num_turns":3}`))
	if !ok || r.Result != "hi" || r.NumTurns != 3 {
		t.Fatalf("%+v %v", r, ok)
	}
	r, ok = parseResult([]byte(`[{"type":"system"},{"type":"assistant"},{"type":"result","result":"from a list"}]`))
	if !ok || r.Result != "from a list" {
		t.Fatalf("list: %+v", r)
	}
	r, ok = parseResult([]byte("{\"type\":\"system\"}\n{\"type\":\"result\",\"result\":\"streamed\"}\n"))
	if !ok || r.Result != "streamed" {
		t.Fatalf("stream: %+v", r)
	}
	if _, ok := parseResult([]byte("plain text")); ok {
		t.Fatal("text taken for a result")
	}
}

func TestFramesPickedEvenly(t *testing.T) {
	var all []frame
	for i := range 50 {
		all = append(all, frame{ms: int64(i) * 30_000})
	}
	got := pickFrames(all, 20)
	if len(got) != 20 || got[0].ms != 0 || got[19].ms != 49*30_000 {
		t.Fatalf("ends: %+v", got)
	}
	for i := 1; i < len(got); i++ {
		step := got[i].ms - got[i-1].ms
		if step < 60_000 || step > 90_000 { // 49 steps over 19 gaps: 2 or 3 frames apart
			t.Fatalf("uneven at %d: %d", i, step)
		}
	}
	if len(pickFrames(all[:7], 20)) != 7 || len(pickFrames(all, 1)) != 1 || pickFrames(all, 0) != nil {
		t.Fatal("small cases")
	}

	dir := t.TempDir()
	fd := filepath.Join(dir, FramesDir)
	os.MkdirAll(filepath.Join(fd, "f-000000999.jpg"), 0o700) // a folder named like a frame
	for name, size := range map[string]int{"f-000060000.jpg": 10, "f-000000000.jpg": 10, "f-000030000.jpeg": 10, "notes.txt": 10,
		"f-000090000.jpg": 0, "f-000120000.jpg": maxFrameBytes + 1, "f-abc.jpg": 10} {
		os.WriteFile(filepath.Join(fd, name), make([]byte, size), 0o600)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(fd, "f-000150000.jpg")); err != nil {
		t.Log("no symlink:", err)
	}
	var names []string
	for _, f := range listFrames(dir) {
		names = append(names, fmt.Sprintf("%d=%s", f.ms, f.path))
	}
	if got := strings.Join(names, " "); got != "0=frames/f-000000000.jpg 30000=frames/f-000030000.jpeg 60000=frames/f-000060000.jpg" {
		t.Fatalf("frames: %s", got)
	}
	if listFrames(filepath.Join(dir, "missing")) != nil || listFrames("") != nil {
		t.Fatal("no folder")
	}
}

func TestArgs(t *testing.T) {
	got := strings.Join(args(ask{frames: 3, model: "m", system: "S"}, nil), " ")
	want := `-p --output-format json --model m --max-turns 6 --tools Read --permission-mode dontAsk --allowedTools=Read(./frames/**) ` +
		`--disallowedTools=Bash,Edit,Write,MultiEdit,NotebookEdit,WebFetch,WebSearch,Task,Agent --strict-mcp-config --mcp-config={"mcpServers":{}} ` +
		`--no-session-persistence --disable-slash-commands --restricted --system-prompt S`
	if got != want {
		t.Fatalf("args:\n%s\nwant\n%s", got, want)
	}
	a := args(ask{model: "m", system: partSystemPrompt}, map[string]bool{"--restricted": true})
	if problem := checkArgs(a); problem != "" {
		t.Fatal(problem)
	}
	if v := argValue(a, "--tools"); v != "" || argValue(a, "--max-turns") != "2" || strings.Contains(strings.Join(a, " "), " --allowedTools=") ||
		strings.Contains(strings.Join(a, " "), "--restricted") {
		t.Fatalf("no frames: %q", a)
	}
	if checkArgs(args(ask{frames: 1, model: "m", system: systemPrompt(1)}, nil)) != "" || checkArgs(args(ask{model: "m", system: partSystemPrompt}, nil)) != "" {
		t.Fatal("the real prompts fail the check")
	}
}

func TestSplitAndPlace(t *testing.T) {
	var lines []line
	for i := range 10 {
		lines = append(lines, line{at: int64(i) * 1000, text: strings.Repeat("x", 20)}) // 30 each with the stamp
	}
	parts, dropped := split(lines, 10_000, 90, 10)
	if len(parts) != 4 || dropped != -1 || parts[0].from != 0 || parts[0].to != 3000 || parts[3].to != 10_000 || len(parts[3].lines) != 1 {
		t.Fatalf("parts: %+v", parts)
	}
	parts, dropped = split(lines, 10_000, 90, 2)
	if len(parts) != 2 || dropped != 6000 || parts[1].to != 6000 {
		t.Fatalf("capped: %d %d", len(parts), dropped)
	}
	if p, d := split(nil, 0, 90, 2); len(p) != 0 || d != -1 {
		t.Fatal("empty")
	}
	r := model.Recording{DurationMS: 25_000, Segments: []model.RecordingSegment{{N: 1, DurationMS: 10_000}, {N: 2, DurationMS: 10_000}, {N: 3, DurationMS: 5_000}}}
	got := place([]model.SummaryChapter{{TMS: 21_000, Title: "c"}, {TMS: 0, Title: "a"}, {TMS: 0, Title: "dup"}, {TMS: 10_000, Title: "b"},
		{TMS: 26_000, Title: "end"}, {TMS: 99_000, Title: "made up"}}, r)
	var s []string
	for _, c := range got {
		s = append(s, fmt.Sprintf("%s@%d/%d", c.Title, c.Segment, c.OffsetMS))
	}
	if strings.Join(s, " ") != "a@1/0 b@2/0 c@3/1000 end@3/4999" {
		t.Fatalf("placed: %s", strings.Join(s, " "))
	}
	for in, want := range map[string]int64{"0:05": 5000, "12:03": 723000, "1:02:03": 3723000, "[3:00]": 180000, "0:07.5": 7000} {
		if got, ok := parseStamp(in); !ok || got != want {
			t.Errorf("%s: %d %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "5", "a:b", "1:2:3:4", "-1:00"} {
		if _, ok := parseStamp(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if stamp(3_723_000) != "1:02:03" || stamp(65_000) != "1:05" || stamp(-5) != "0:00" {
		t.Fatal("stamp")
	}
}

func TestParseSource(t *testing.T) {
	if id, n, ok := ParseSource("recording:abc/12"); !ok || id != "abc" || n != 12 {
		t.Fatal(id, n, ok)
	}
	for _, bad := range []string{"dictation", "recording:", "recording:abc", "recording:/1", "recording:abc/0", "recording:abc/x"} {
		if _, _, ok := ParseSource(bad); ok {
			t.Errorf("%q", bad)
		}
	}
}

// setupVoice is the real transcription service whose Setup installs the fake engine and model
// (transcribetest) instead of downloading them.
type setupVoice struct {
	*transcribe.Service
	home   string
	setups int
}

func (v *setupVoice) Setup() (transcribe.Status, error) {
	v.setups++
	if err := transcribetest.InstallFiles(v.home, "hello from the fake engine"); err != nil {
		return v.Status(), err
	}
	return v.Status(), nil
}

func TestAutoSetupWithTheRealVoiceService(t *testing.T) {
	if !transcribetest.Supported() {
		t.Skip("the fake voice engine is a shell script")
	}
	e := newEnv(t, "ok")
	paths := config.Paths{Home: e.home}
	tr := transcribe.New(transcribe.Options{Paths: paths, Store: e.st, Publish: e.publish, NoLowPriority: true,
		RecordingsDir: filepath.Join(e.home, "recordings"), Version: "test", NumCPU: 2, MemTotal: 16 << 30,
		Settings: func() config.VoiceConfig { return config.VoiceConfig{} }}) // auto_transcribe: the default (on)
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Close)
	if tr.Status().Ready || !tr.AutoTranscribe() {
		t.Fatalf("voice: %+v", tr.Status())
	}
	v := &setupVoice{Service: tr, home: e.home}
	e.opts = func(o *Options) { o.Voice = v }
	e.restart()
	r := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r.ID)
	x := e.wait(r.ID, model.SummaryDone, model.SummaryFailed, model.SummarySkipped)
	if x.State != model.SummaryDone || x.Transcripts != 1 || v.setups != 1 {
		t.Fatalf("summary: %+v (setups %d)", x, v.setups)
	}
	runs := e.runs()
	if len(runs) != 1 || !strings.Contains(runs[0].Stdin, "[0:00] hello from the fake engine") {
		t.Fatalf("prompt: %+v", runs)
	}
	deadline := time.Now().Add(time.Second)
	for !tr.Status().Ready && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !tr.Status().Ready {
		t.Fatal("voice is not ready after the setup")
	}
}
