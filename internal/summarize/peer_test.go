package summarize

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// servingPeer is the fleet peer as seen from the asking daemon: it hands the generate to the serving daemon's
// ServePeer as the peer named from (what the pair-token route does).
type servingPeer struct {
	from  string
	serve *Service
	calls []string
	err   error
}

func (p *servingPeer) SummarizeLocal(ctx context.Context, host string, in PeerGenerate) (string, error) {
	p.calls = append(p.calls, host)
	if p.err != nil {
		return "", p.err
	}
	return p.serve.ServePeer(ctx, p.from, in)
}

// A machine without Ollama (via_peer) gets its summary from the peer's own local model: hostb -> hosta ->
// hosta's loopback Ollama, with the frames, and hosta runs only its own model.
func TestViaPeerUsesThePeersLocalModel(t *testing.T) {
	srv, got := fakeOllama(t, false)
	hosta := localService(config.SummaryConfig{LocalURL: srv.URL, ServePeers: []string{"Hostb"}})
	peer := &servingPeer{from: "hostb", serve: hosta}
	hostb := localService(config.SummaryConfig{ViaPeer: "hosta"})
	hostb.SetPeer(peer)
	text, err := hostb.run(t.Context(), framesAsk(t, 2))
	if err != nil || !strings.Contains(text, "Budget CHF 48,500") {
		t.Fatalf("summary via hosta: %q %v", text, err)
	}
	if len(peer.calls) != 1 || peer.calls[0] != "hosta" || len(*got) != 1 {
		t.Fatalf("peer calls %v, ollama requests %d", peer.calls, len(*got))
	}
	b := (*got)[0]
	if b["model"] != DefaultLocalModel || b["format"] != "json" || b["stream"] != false || !strings.Contains(b["prompt"].(string), "Transcript: hello") {
		t.Fatalf("forwarded %+v", b)
	}
	if imgs, _ := b["images"].([]any); len(imgs) != 2 {
		t.Fatalf("images forwarded: %d", len(imgs))
	}
}

// A peer without the route (still the Python daemon): the cloud model writes the report instead, except for a
// clinic recording, which fails with the reason.
func TestViaPeerWithoutTheRoute(t *testing.T) {
	gone := errors.New("hosta has no summary route yet (it runs it once its daemon is tmx-go)")
	s := localService(config.SummaryConfig{ViaPeer: "hosta"})
	s.SetPeer(&servingPeer{err: gone})
	noClaude(t, s)
	_, err := s.run(t.Context(), framesAsk(t, 0))
	if err == nil || !strings.Contains(err.Error(), "no summary route yet") || !strings.Contains(err.Error(), "; and the cloud model: ") || !triedClaude(err) {
		t.Fatalf("falls back to the cloud model: %v", err)
	}
	a := framesAsk(t, 0)
	a.clinic = true
	_, err = s.run(t.Context(), a)
	if err == nil || !strings.Contains(err.Error(), "clinic recording is summarized by local models only (patient data, nLPD), and the local model on hosta: hosta has no summary route yet") || triedClaude(err) {
		t.Fatalf("clinic: %v", err)
	}
	none := localService(config.SummaryConfig{ViaPeer: "hosta"}) // no fleet peers at all
	noClaude(t, none)
	if _, err := none.run(t.Context(), a); err == nil || !strings.Contains(err.Error(), "no fleet peers") {
		t.Fatalf("clinic without peers: %v", err)
	}
}

// The serving side: only the peers it serves, only its own model, within the limits, only its loopback Ollama.
func TestServePeerRules(t *testing.T) {
	srv, got := fakeOllama(t, false)
	s := localService(config.SummaryConfig{LocalURL: srv.URL, ServePeers: []string{"hostb"}})
	ok := PeerGenerate{Model: DefaultLocalModel, Prompt: "Transcript: hi"}
	for name, c := range map[string]struct {
		peer string
		in   PeerGenerate
		want error
	}{
		"not served":      {"hostc", ok, ErrPeerForbidden},
		"no peer":         {"", ok, ErrPeerForbidden},
		"another model":   {"hostb", PeerGenerate{Model: "llama3:70b", Prompt: "p"}, ErrPeerInvalid},
		"empty prompt":    {"hostb", PeerGenerate{Model: DefaultLocalModel}, ErrPeerInvalid},
		"huge prompt":     {"hostb", PeerGenerate{Model: DefaultLocalModel, Prompt: strings.Repeat("a", PeerMaxPrompt+1)}, ErrPeerInvalid},
		"too many stills": {"hostb", PeerGenerate{Model: DefaultLocalModel, Prompt: "p", Images: make([]string, LocalFrames+1)}, ErrPeerInvalid},
		"huge still":      {"hostb", PeerGenerate{Model: DefaultLocalModel, Prompt: "p", Images: []string{strings.Repeat("A", PeerMaxImage+1)}}, ErrPeerInvalid},
	} {
		if _, err := s.ServePeer(t.Context(), c.peer, c.in); !errors.Is(err, c.want) {
			t.Fatalf("%s: want %v, got %v", name, c.want, err)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("a refused request reached Ollama: %d", len(*got))
	}
	if text, err := s.ServePeer(t.Context(), "hostb", ok); err != nil || text == "" || len(*got) != 1 {
		t.Fatalf("served: %q %v", text, err)
	}
	remote := localService(config.SummaryConfig{LocalURL: "http://192.0.2.10:11434", ServePeers: []string{"hostb"}})
	if _, err := remote.ServePeer(t.Context(), "hostb", ok); err == nil || !strings.Contains(err.Error(), "local_url") {
		t.Fatalf("a non-loopback Ollama: %v", err)
	}
}
