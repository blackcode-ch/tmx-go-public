package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

const transcriptionLocalOnly = "X-Transcription-Local-Only"

// Reuse the configured-host address and token boundary. Never use a proxy or follow
// redirects with a host's owner token. Each call also has a context deadline.
var voiceHostClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil, ResponseHeaderTimeout: 3 * time.Minute},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type voiceHostTarget struct {
	port  int
	token string
}
type configuredVoiceHosts struct {
	paths     config.Paths
	ownPort   int
	requestID string
	targets   map[string]voiceHostTarget
}

func (h *configuredVoiceHosts) Hosts(ctx context.Context) ([]transcribe.TranscriptionHost, error) {
	c, err := config.Load(h.paths)
	if err != nil {
		return nil, err
	}
	if c.Voice == nil || !c.Voice.FallbackHosts {
		return nil, nil
	}
	h.targets = make(map[string]voiceHostTarget)
	// One shared discovery deadline, independent of the number of configured hosts.
	ctx, cancel := context.WithTimeout(ctx, hostTimeout)
	defer cancel()
	var out []transcribe.TranscriptionHost
	seen := make(map[string]bool)
	for _, host := range c.Hosts {
		if ctx.Err() != nil {
			break
		}
		if !config.ValidHostName(host.Name) || !config.ValidHostPort(host.Port) || host.Port == h.ownPort {
			continue
		}
		if seen[host.Name] {
			continue
		}
		seen[host.Name] = true
		token, err := config.ReadSecret(h.paths.HostTokenFile(host.Name))
		if err != nil || token == "" {
			continue
		}
		target := voiceHostTarget{port: host.Port, token: token}
		var status transcribe.Status
		if err := h.call(ctx, target, "GET", "/api/voice", nil, &status); err != nil {
			continue
		}
		h.targets[host.Name] = target
		out = append(out, transcribe.TranscriptionHost{ID: host.Name, Ready: status.Ready, Busy: status.Busy || status.Job != nil || status.Queued > 0})
	}
	return out, nil
}

func (h *configuredVoiceHosts) Dictate(ctx context.Context, id string, wav []byte, language string) (transcribe.Result, error) {
	target, ok := h.targets[id]
	if !ok {
		return transcribe.Result{}, errors.New("host was not selected from configured hosts")
	}
	body, err := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(wav), "language": language})
	if err != nil {
		return transcribe.Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var result transcribe.Result
	err = h.call(ctx, target, "POST", "/api/transcribe", body, &result)
	return result, err
}

func (h *configuredVoiceHosts) call(ctx context.Context, target voiceHostTarget, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, configuredHostBase(target.port)+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+target.token)
	if method == "POST" {
		// Even with an idempotency key, do not allow Transport to replay the body.
		req.GetBody = nil
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", h.requestID)
		req.Header.Set(transcriptionLocalOnly, "true")
	}
	resp, err := voiceHostClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("configured transcription host did not answer")
	}
	defer resp.Body.Close()
	if method == http.MethodPost && resp.StatusCode == http.StatusUnprocessableEntity {
		return transcribe.ErrNoSpeech
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("configured transcription host rejected the request")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("invalid configured host response")
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("invalid configured host response")
	}
	return nil
}
