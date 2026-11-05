package peers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/summarize"
)

// summarizeTimeout bounds one generate on a peer: its model's own limit (summarize.RunTimeout) and a wait for its turn.
const summarizeTimeout = summarize.RunTimeout + 2*time.Minute

// SummarizeLocal has host's daemon run one generate on its own local model (summarize.Peer; summaries.via_peer).
// The transcript and frames go to that machine of our own fleet only, over its pair-token route.
func (c *Client) SummarizeLocal(ctx context.Context, host string, in summarize.PeerGenerate) (string, error) {
	peer, ok := c.peers[strings.ToLower(host)]
	if !ok {
		return "", fmt.Errorf("%s is not a fleet peer of this daemon (config \"peers\")", host)
	}
	var out summarize.PeerAnswer
	_, err := c.do(ctx, peer, http.MethodPost, "/api/peer/summarize-local", in, &out, summarizeTimeout)
	var ref Refused
	switch {
	case errors.As(err, &ref) && ref.Status == http.StatusNotFound:
		return "", fmt.Errorf("%s has no summary route yet (it runs it once its daemon is tmx-go)", host)
	case errors.As(err, &ref) && ref.Status == http.StatusForbidden:
		return "", fmt.Errorf("%s does not run its model for this machine: add it to %s's summaries.serve_peers (%s)", host, host, ref.Msg)
	case err != nil:
		return "", err
	}
	return out.Response, nil
}
