package summarize

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// fakeOllama answers /api/generate; with noVision it refuses any request that carries images.
func fakeOllama(t *testing.T, noVision bool) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	got := &[]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		*got = append(*got, body)
		mu.Unlock()
		if _, has := body["images"]; has && noVision {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"this model does not support images"}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":"{\"summary\":\"Budget CHF 48,500 reviewed\"}"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// localService prefers the local model (summaries.backend "local") unless cfg names a backend.
func localService(cfg config.SummaryConfig) *Service {
	if cfg.Backend == "" {
		cfg.Backend = "local"
	}
	return New(Options{Settings: func() config.SummaryConfig { return cfg }})
}

func framesAsk(t *testing.T, n int) ask {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, FramesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	a := ask{dir: dir, system: "sys", prompt: "Transcript: hello", frames: n, model: "x"}
	for i := range n {
		p := FramesDir + "/f-" + strings.Repeat("0", 8) + string(rune('0'+i)) + ".jpg"
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte{0xff, 0xd8, 0xff, byte(i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		a.paths = append(a.paths, p)
	}
	return a
}

// The local model writes the report on this machine: Ollama on loopback, the frames as images, JSON asked for.
func TestLocalSendsFrames(t *testing.T) {
	srv, got := fakeOllama(t, false)
	s := localService(config.SummaryConfig{LocalURL: srv.URL})
	text, err := s.run(t.Context(), framesAsk(t, 9))
	if err != nil {
		t.Fatal(err)
	}
	if sum, err := parseAnswer(text); err != nil || !strings.Contains(sum.Summary, "48,500") {
		t.Fatalf("answer %q: %+v %v", text, sum, err)
	}
	b := (*got)[0]
	if b["model"] != DefaultLocalModel || b["format"] != "json" || b["system"] != "sys" {
		t.Fatalf("request %+v", b)
	}
	if imgs, _ := b["images"].([]any); len(imgs) != LocalFrames {
		t.Fatalf("images: %d, want %d spread over 9 frames", len(imgs), LocalFrames)
	}
}

// A model without vision still writes the report, from the transcript alone.
func TestLocalFallsBackToTextWithoutVision(t *testing.T) {
	srv, got := fakeOllama(t, true)
	s := localService(config.SummaryConfig{Backend: "local", LocalURL: srv.URL, LocalModel: "qwen3:14b"})
	if _, err := s.run(t.Context(), framesAsk(t, 2)); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 || (*got)[1]["images"] != nil || (*got)[1]["model"] != "qwen3:14b" {
		t.Fatalf("requests %+v", *got)
	}
}

// The local model is this machine's: a non-loopback address is refused before anything is sent (a clinic ask, so
// nothing falls back to the cloud model).
func TestLocalRefusesARemoteAddress(t *testing.T) {
	for _, u := range []string{"http://localhost:11434", "http://192.0.2.10:11434", "https://127.0.0.1:11434", "http://example.com", "ftp://127.0.0.1"} {
		s := localService(config.SummaryConfig{LocalURL: u})
		if _, err := s.run(t.Context(), ask{dir: t.TempDir(), prompt: "p", clinic: true}); err == nil || !strings.Contains(err.Error(), "local_url") {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if s := localService(config.SummaryConfig{Backend: "claude"}); s.local() {
		t.Fatal("backend claude is not the local path")
	}
}

// triedClaude reports whether err comes from the cloud path (claude was looked for or refused to start).
func triedClaude(err error) bool { return strings.Contains(strings.ToLower(err.Error()), "claude") }

// noClaude points the cloud path at a claude that does not exist: its error shows whether it was tried.
func noClaude(t *testing.T, s *Service) { s.o.Claude = filepath.Join(t.TempDir(), "no-claude") }

// The cheap cloud model writes reports by default (the owner, 3 Nov: "we care only about performance"); "local"
// prefers the local model.
func TestTheCloudModelIsTheDefault(t *testing.T) {
	for b, want := range map[string]bool{"": false, "claude": false, " Local ": true, "local": true} {
		if s := New(Options{Settings: func() config.SummaryConfig { return config.SummaryConfig{Backend: b} }}); s.local() != want {
			t.Fatalf("backend %q: local %v", b, s.local())
		}
	}
	s := New(Options{Settings: func() config.SummaryConfig { return config.SummaryConfig{} }})
	noClaude(t, s)
	if _, err := s.run(t.Context(), ask{dir: t.TempDir(), prompt: "p"}); err == nil || !triedClaude(err) {
		t.Fatalf("the default goes to the cloud model: %v", err)
	}
}

// Without a local route, "local" falls back to the cloud model: a missing route never fails a report by itself.
func TestLocalFallsBackToTheCloudModel(t *testing.T) {
	s := localService(config.SummaryConfig{LocalURL: "http://127.0.0.1:1"})
	noClaude(t, s)
	_, err := s.run(t.Context(), ask{dir: t.TempDir(), prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "does not answer") || !strings.Contains(err.Error(), "; and the cloud model: ") || !triedClaude(err) {
		t.Fatalf("want the local error and then the cloud model tried: %v", err)
	}
}

// A clinic recording (patient data, nLPD) is written by local models only, whatever the backend: with one it is
// the local model that writes, without one it fails with the reason and the cloud model is never tried.
func TestClinicIsLocalOnly(t *testing.T) {
	srv, got := fakeOllama(t, false)
	for _, b := range []string{"", "claude", "local"} {
		s := New(Options{Settings: func() config.SummaryConfig { return config.SummaryConfig{Backend: b, LocalURL: srv.URL} }})
		noClaude(t, s)
		n := len(*got)
		if _, err := s.run(t.Context(), ask{dir: t.TempDir(), prompt: "p", clinic: true}); err != nil || len(*got) != n+1 {
			t.Fatalf("backend %q: %v (ollama asked %d times)", b, err, len(*got)-n)
		}
		if m := s.takeWrote(""); m != DefaultLocalModel {
			t.Fatalf("backend %q: written by %q", b, m)
		}
		down := New(Options{Settings: func() config.SummaryConfig { return config.SummaryConfig{Backend: b, LocalURL: "http://127.0.0.1:1"} }})
		noClaude(t, down)
		_, err := down.run(t.Context(), ask{dir: t.TempDir(), prompt: "p", clinic: true})
		if err == nil || !strings.Contains(err.Error(), "clinic recording is summarized by local models only") || triedClaude(err) {
			t.Fatalf("backend %q without a local route: %v", b, err)
		}
	}
}

// A redirect from whatever answers on the port is never followed: the transcript and frames stay on this machine.
func TestLocalNeverFollowsARedirect(t *testing.T) {
	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request followed a redirect")
	}))
	t.Cleanup(away.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, away.URL+"/api/generate", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	s := localService(config.SummaryConfig{LocalURL: srv.URL})
	if _, err := s.run(t.Context(), ask{dir: t.TempDir(), prompt: "p", clinic: true}); err == nil { // local only: no cloud fallback
		t.Fatal("a redirect answer was taken as a summary")
	}
}

// The cloud model by default, but when it is not available (no claude on this machine) a configured local model
// writes the report instead of failing it.
func TestTheCloudFallsBackToAConfiguredLocalModel(t *testing.T) {
	srv, got := fakeOllama(t, false)
	s := New(Options{Settings: func() config.SummaryConfig { return config.SummaryConfig{LocalURL: srv.URL} }})
	noClaude(t, s)
	text, err := s.run(t.Context(), ask{dir: t.TempDir(), prompt: "p"})
	if err != nil || text == "" || len(*got) != 1 || s.takeWrote("") != DefaultLocalModel {
		t.Fatalf("fallback: %q %v (ollama asked %d times)", text, err, len(*got))
	}
	none := New(Options{Settings: func() config.SummaryConfig { return config.SummaryConfig{} }}) // nothing configured: the cloud error
	noClaude(t, none)
	if _, err := none.run(t.Context(), ask{dir: t.TempDir(), prompt: "p"}); err == nil || strings.Contains(err.Error(), "local model") {
		t.Fatalf("without a configured local model: %v", err)
	}
}
