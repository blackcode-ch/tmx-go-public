package transcribe

import (
	"context"
	"errors"
	"strings"
)

// ConfiguredHosts is the integration seam for the configured host API. Implementations
// must return only explicitly configured, authorized hosts, never discovered peers.
// The API adapter uses the same configured local daemons as the fleet hosts view.
type ConfiguredHosts interface {
	Hosts(context.Context) ([]TranscriptionHost, error)
	Dictate(context.Context, string, []byte, string) (Result, error)
}

type TranscriptionHost struct {
	ID    string
	Ready bool
	Busy  bool
}

var ErrNoSpeech = errors.New("couldn't hear you")

// Digital silence can be rejected without starting an engine or sending audio.
func silentPCM(wav []byte, w wavInfo) bool {
	if !w.pcm16() {
		return false
	}
	for _, b := range wav[w.DataOffset : w.DataOffset+w.DataSize] {
		if b != 0 {
			return false
		}
	}
	return true
}

func (s *Service) dictationFallback(ctx context.Context, wav []byte, w wavInfo, language string, provider ConfiguredHosts) (Result, bool, error) {
	if provider == nil {
		return Result{}, false, nil
	}
	ready, _ := s.readiness()
	s.mu.Lock()
	busy := s.running != nil || len(s.queue) > 0
	s.mu.Unlock()
	if ready && !busy {
		return Result{}, false, nil
	}
	// Before any host lookup or audio transfer, reject known digital silence.
	// Compressed/other formats need local decoding; do not send them speculatively.
	if !w.pcm16() {
		return Result{}, false, nil
	}
	if w.DataSize%w.frameBytes() != 0 {
		return Result{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, true, err
	}
	hosts, err := provider.Hosts(ctx)
	if err != nil {
		return Result{}, false, nil
	} // retain local queue/not-set-up behavior
	for _, host := range hosts {
		if host.ID == "" || !host.Ready || host.Busy {
			continue
		}
		if language == "" {
			language = s.settings().Language
		}
		res, err := provider.Dictate(ctx, host.ID, wav, language)
		if err == nil && strings.TrimSpace(res.Text) == "" {
			return Result{}, true, ErrNoSpeech
		}
		// One attempt only: a transport error may mean the host already processed it.
		return res, true, err
	}
	return Result{}, false, nil
}
