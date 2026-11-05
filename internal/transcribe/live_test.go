package transcribe

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// Live tests with the real whisper-cli; CI never sets TMXG_LIVE_WHISPER.
//
//	TMXG_LIVE_WHISPER=1 TMXG_WHISPER_CLI=/path/whisper-cli TMXG_WHISPER_MODEL=/path/ggml-base-q5_1.bin \
//	  TMXG_WHISPER_AUDIO=/path/whisper.cpp/samples/jfk.wav [TMXG_WHISPER_VAD=/path/ggml-silero-v6.2.0.bin] \
//	  go test -run Live -v ./internal/transcribe/
//
// TMXG_WHISPER_EXPECT is a phrase the text must contain (default for jfk.wav: "ask not what
// your country can do for you"). TestLiveSetup instead downloads everything the way the daemon
// does (the signed engine release from GitHub, the pinned model from Hugging Face) into a
// throwaway home: TMXG_LIVE_WHISPER=1 TMXG_LIVE_WHISPER_SETUP=1 (needs gh auth or a token).

func liveOnly(t *testing.T) {
	if os.Getenv("TMXG_LIVE_WHISPER") != "1" {
		t.Skip("set TMXG_LIVE_WHISPER=1 to run the real engine")
	}
}

var wordsRx = regexp.MustCompile(`[^a-z0-9 ]+`)

func normalized(s string) string {
	return strings.Join(strings.Fields(wordsRx.ReplaceAllString(strings.ToLower(s), " ")), " ")
}

func expectPhrase(audio string) string {
	if e := os.Getenv("TMXG_WHISPER_EXPECT"); e != "" {
		return normalized(e)
	}
	if filepath.Base(audio) == "jfk.wav" {
		return "ask not what your country can do for you"
	}
	return ""
}

func TestLiveEngine(t *testing.T) {
	liveOnly(t)
	cli, mdl, audio := os.Getenv("TMXG_WHISPER_CLI"), os.Getenv("TMXG_WHISPER_MODEL"), os.Getenv("TMXG_WHISPER_AUDIO")
	if cli == "" || mdl == "" || audio == "" {
		t.Fatal("set TMXG_WHISPER_CLI, TMXG_WHISPER_MODEL and TMXG_WHISPER_AUDIO")
	}
	if err := probeEngine(context.Background(), cli); err != nil {
		t.Fatalf("probe: %v", err)
	}
	start := time.Now()
	r, err := runEngine(context.Background(), runSpec{Engine: cli, Model: mdl, VAD: os.Getenv("TMXG_WHISPER_VAD"), Input: audio,
		Language: "auto", Threads: 4, Timeout: 5 * time.Minute, LowPriority: true, TmpDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	text := joinText(r.Segments)
	t.Logf("%s, language %s, %d segments in %s: %s", filepath.Base(audio), r.Language, len(r.Segments), time.Since(start).Round(time.Millisecond), text)
	if len(r.Segments) == 0 || r.Segments[len(r.Segments)-1].ToMS <= 0 {
		t.Fatalf("no timed segments: %+v", r)
	}
	if want := expectPhrase(audio); want != "" && !strings.Contains(normalized(text), want) {
		t.Fatalf("the text lacks %q", want)
	}

	// The whole path: a service with this engine and model answering dictation.
	home := t.TempDir()
	s := New(Options{Paths: config.Paths{Home: home}})
	for src, dst := range map[string]string{cli: s.enginePath(), mdl: filepath.Join(s.modelsDir(), filepath.Base(mdl))} {
		os.MkdirAll(filepath.Dir(dst), 0o700)
		if err := os.Symlink(src, dst); err != nil {
			copyFile(t, src, dst)
		}
	}
	if m, ok := FindModel(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(mdl), "ggml-"), ".bin")); !ok || !s.modelInstalled(m) {
		t.Skipf("%s is not the catalog's file of that name; skipping the service part", filepath.Base(mdl))
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, err := os.ReadFile(audio)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readWAV(audio); err != nil {
		t.Skip("the service part needs a WAV")
	}
	d, err := s.Dictate(context.Background(), b, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dictation: %s", d.Text)
	if want := expectPhrase(audio); want != "" && !strings.Contains(normalized(d.Text), want) {
		t.Fatalf("dictation lacks %q", want)
	}
}

func TestLiveSetup(t *testing.T) {
	liveOnly(t)
	if os.Getenv("TMXG_LIVE_WHISPER_SETUP") != "1" {
		t.Skip("set TMXG_LIVE_WHISPER_SETUP=1 to download the engine and the default model (about 65 MB)")
	}
	home := t.TempDir()
	paths := config.Paths{Home: home}
	s := New(Options{Paths: paths, Version: "live-test", Releases: update.New(update.Options{Paths: paths, Version: "live-test"})})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Setup(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Minute)
	var st Status
	for time.Now().Before(deadline) {
		if st = s.Status(); !st.Busy {
			break
		}
		time.Sleep(time.Second)
	}
	if !st.Ready {
		t.Fatalf("not ready after setup: engine %+v, model %+v, reason %s", st.Engine, st.Models[0], st.Reason)
	}
	t.Logf("engine %s (%d bytes), %s, VAD %v", st.Engine.Version, st.Engine.Size, st.Models[0].Name, st.VAD.Installed)
	if audio := os.Getenv("TMXG_WHISPER_AUDIO"); audio != "" {
		b, err := os.ReadFile(audio)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.Dictate(context.Background(), b, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("dictation: %s", r.Text)
		if want := expectPhrase(audio); want != "" && !strings.Contains(normalized(r.Text), want) {
			t.Fatalf("dictation lacks %q", want)
		}
	}
}
