package summarize

// The local backend: the local Ollama (loopback only, summaries.local_url, default http://127.0.0.1:11434) with
// summaries.local_model (default gemma4:e4b, which reads the still frames as images), or a fleet peer's
// (summaries.via_peer, peer.go). It writes a summary when summaries.backend is "local" (falling back to the cloud
// model without a local route), and always for a clinic recording (patient data: local only, see run).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultLocalModel writes summaries with the local backend unless summaries.local_model names another.
const DefaultLocalModel = "gemma4:e4b"

// DefaultLocalURL is the local Ollama.
const DefaultLocalURL = "http://127.0.0.1:11434"

// LocalFrames is how many still frames go to the local model as images (a small vision model reads a few well).
const LocalFrames = 6

// localConfigured: this daemon names a local model to use (summaries.local_url or via_peer), not only the default.
func (s *Service) localConfigured() bool {
	c := s.settings()
	return strings.TrimSpace(c.LocalURL) != "" || strings.TrimSpace(c.ViaPeer) != ""
}

// local reports whether the local model is preferred (summaries.backend "local"); the default is the cloud model.
func (s *Service) local() bool {
	return strings.ToLower(strings.TrimSpace(s.settings().Backend)) == "local"
}

func (s *Service) localModel() string {
	if m := strings.TrimSpace(s.settings().LocalModel); m != "" {
		return m
	}
	return DefaultLocalModel
}

// localURL is the Ollama address; anything but a loopback http address is refused (client content never leaves).
func (s *Service) localURL() (string, error) {
	raw := strings.TrimSpace(s.settings().LocalURL)
	if raw == "" {
		raw = DefaultLocalURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return "", fmt.Errorf("summaries.local_url must be an http://127.0.0.1 address, not %q", raw)
	}
	// a loopback IP only: "localhost" would trust the hosts file (go-reviewer-o, PR #133)
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("summaries.local_url must be on this machine (loopback), not %q", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// localImages are up to LocalFrames of the ask's frames, spread evenly, as base64.
func localImages(a ask) []string {
	paths := a.paths
	if len(paths) > LocalFrames {
		picked := make([]string, 0, LocalFrames)
		for i := range LocalFrames {
			picked = append(picked, paths[i*(len(paths)-1)/(LocalFrames-1)])
		}
		paths = picked
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(a.dir, filepath.FromSlash(p)))
		if err != nil || len(b) > 4<<20 {
			continue
		}
		out = append(out, base64.StdEncoding.EncodeToString(b))
	}
	return out
}

// runLocal asks the local model once and returns its answer's text (the same JSON the Claude path is asked for).
func (s *Service) runLocal(ctx context.Context, a ask) (string, error) {
	base, err := s.localURL()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, s.o.Timeout)
	defer cancel()
	prompt := a.prompt
	images := localImages(a)
	if len(images) > 0 {
		prompt += "\n\nThe still frames of the screen are attached as images, in time order (" + strings.Join(a.paths, ", ") + ")."
	}
	body := map[string]any{"model": s.localModel(), "system": a.system, "prompt": prompt, "stream": false, "format": "json"}
	if len(images) > 0 {
		body["images"] = images
	}
	text, err := s.generate(ctx, base, body)
	if err != nil && len(images) > 0 { // a model without vision: the transcript alone
		delete(body, "images")
		body["prompt"] = a.prompt
		text, err = s.generate(ctx, base, body)
	}
	return text, err
}

func (s *Service) ollama(ctx context.Context, base string, body map[string]any) (string, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/generate", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// no proxy, and no redirect: a 307/308 from whatever answers on the port would resend the transcript and the
	// frames to another host (go-reviewer-o, PR #133)
	client := &http.Client{Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("the local model took longer than %s and was stopped", s.o.Timeout)
		}
		return "", fmt.Errorf("the local model (Ollama at %s) does not answer: %w", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var out struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		why := out.Error
		if why == "" {
			why = resp.Status
		}
		return "", fmt.Errorf("the local model refused: %s", clip(oneLine(why), 300))
	}
	if strings.TrimSpace(out.Response) == "" {
		return "", errors.New("the local model gave an empty answer")
	}
	return out.Response, nil
}

// wrote notes the model that wrote the latest answer (summaries run one at a time); make reports it.
func (s *Service) wrote(model string) {
	s.mu.Lock()
	s.wroteBy = model
	s.mu.Unlock()
}

// takeWrote returns (and clears) the model that wrote the answers since the last call; def when none did.
func (s *Service) takeWrote(def string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.wroteBy
	s.wroteBy = ""
	if m == "" {
		return def
	}
	return m
}
