package summarize

// The local model on another machine of our own fleet (G5, tmx-lead 3 Nov): a daemon without room for Ollama
// (summaries.via_peer = "hosta") has its fleet peer's daemon run the generate for it, over that peer's pair-token
// route POST /api/peer/summarize-local. The serving daemon opts in per peer (summaries.serve_peers), takes only its
// own local model, caps the size, runs one at a time and forwards only to its own loopback Ollama. Without the
// route the cloud model writes the summary, except for a clinic recording, which then fails with the reason (run).

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// PeerGenerate is the body of POST /api/peer/summarize-local: one Ollama generate, as runLocal builds it.
type PeerGenerate struct {
	Model  string   `json:"model"`
	System string   `json:"system"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images,omitempty"` // base64
}

// PeerAnswer is its answer.
type PeerAnswer struct {
	Response string `json:"response"`
}

// Limits of what a peer may ask for.
const (
	PeerMaxPrompt = 1 << 20 // the transcript of a long recording
	PeerMaxImage  = 6 << 20 // base64 of a 4 MiB still (localImages' own cap)
	// PeerMaxBody bounds the request body the route reads.
	PeerMaxBody = PeerMaxPrompt + LocalFrames*PeerMaxImage + 64<<10
)

// Peer sends a generate to a fleet peer's daemon (*peers.Client).
type Peer interface {
	SummarizeLocal(ctx context.Context, host string, in PeerGenerate) (string, error)
}

// ErrPeerForbidden: the asking peer is not in summaries.serve_peers.
var ErrPeerForbidden = errors.New("this daemon does not run its local model for that peer (summaries.serve_peers)")

// ErrPeerInvalid wraps a request the route refuses (model, size).
var ErrPeerInvalid = errors.New("invalid summary request")

func (s *Service) viaPeer() string { return strings.ToLower(strings.TrimSpace(s.settings().ViaPeer)) }

// generate runs one Ollama generate here or, with summaries.via_peer, on that peer.
func (s *Service) generate(ctx context.Context, base string, body map[string]any) (string, error) {
	via := s.viaPeer()
	if via == "" {
		return s.ollama(ctx, base, body)
	}
	if s.o.Peer == nil {
		return "", fmt.Errorf("summaries.via_peer is %q but this daemon has no fleet peers (config \"peers\"); no summary without a local model", via)
	}
	in := PeerGenerate{}
	in.Model, _ = body["model"].(string)
	in.System, _ = body["system"].(string)
	in.Prompt, _ = body["prompt"].(string)
	in.Images, _ = body["images"].([]string)
	text, err := s.o.Peer.SummarizeLocal(ctx, via, in)
	if err != nil {
		return "", fmt.Errorf("the local model on %s: %w", via, err)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("the local model on %s gave an empty answer", via)
	}
	return text, nil
}

// ServePeer is POST /api/peer/summarize-local: peer (authenticated by its pair token) has this daemon's own local
// model run one generate. Only peers in summaries.serve_peers, only this daemon's local model, within the limits,
// one at a time, and only to this machine's loopback Ollama (localURL).
func (s *Service) ServePeer(ctx context.Context, peer string, in PeerGenerate) (string, error) {
	c := s.settings()
	if peer == "" || !slices.ContainsFunc(c.ServePeers, func(p string) bool { return strings.EqualFold(strings.TrimSpace(p), peer) }) {
		return "", ErrPeerForbidden
	}
	if in.Model != s.localModel() {
		return "", fmt.Errorf("%w: this daemon runs %s only, not %q", ErrPeerInvalid, s.localModel(), in.Model)
	}
	if len(in.Prompt) == 0 || len(in.Prompt) > PeerMaxPrompt || len(in.System) > PeerMaxPrompt {
		return "", fmt.Errorf("%w: the prompt is empty or over %d bytes", ErrPeerInvalid, PeerMaxPrompt)
	}
	if len(in.Images) > LocalFrames {
		return "", fmt.Errorf("%w: at most %d images", ErrPeerInvalid, LocalFrames)
	}
	for _, im := range in.Images {
		if len(im) > PeerMaxImage {
			return "", fmt.Errorf("%w: an image is over %d bytes", ErrPeerInvalid, PeerMaxImage)
		}
	}
	base, err := s.localURL() // loopback only
	if err != nil {
		return "", err
	}
	select { // peers' requests one at a time (this daemon's own summaries already run one at a time)
	case s.peerSlot <- struct{}{}:
		defer func() { <-s.peerSlot }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, s.o.Timeout)
	defer cancel()
	body := map[string]any{"model": in.Model, "system": in.System, "prompt": in.Prompt, "stream": false, "format": "json"}
	if len(in.Images) > 0 {
		body["images"] = in.Images
	}
	return s.ollama(ctx, base, body)
}

// SetPeer connects the fleet peers (summaries.via_peer); call it before Start.
func (s *Service) SetPeer(p Peer) { s.o.Peer = p }
