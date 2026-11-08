package peers

// Python v2 voice.py / daemon.py's owner voice protocol, over the existing
// paired fleet channel. This client is called only for owner-admitted recordings.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

const voiceChunk = 1 << 20

var voiceID = regexp.MustCompile(`^[a-f0-9]{16}$`)

// ValidateVoicePeer rejects arbitrary hosts and unpaired machines before audio is read.
func (c *Client) ValidateVoicePeer(host string) error {
	p, ok := c.peers[host]
	if !ok || !config.ValidHostName(host) {
		return fmt.Errorf("%w: transcription target is not a fleet peer", transcribe.ErrInvalid)
	}
	if _, err := config.PeerURL(p.URL); err != nil {
		return fmt.Errorf("%w: invalid fleet peer URL", transcribe.ErrInvalid)
	}
	if c.tokens.For(host) == "" {
		return fmt.Errorf("%w: transcription peer is not paired", transcribe.ErrInvalid)
	}
	return nil
}

func (c *Client) voiceCall(ctx context.Context, host, method, path string, body, out any) error {
	if err := c.ValidateVoicePeer(host); err != nil {
		return err
	}
	token := c.tokens.For(host)
	if token == "" {
		return fmt.Errorf("%w: transcription peer is no longer paired", transcribe.ErrInvalid)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	timeout := 5 * time.Minute
	if path == "/api/voice/review" {
		timeout = 12 * time.Minute
	} // Python's local review model allows ten minutes
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.peers[host].URL+path, rd)
	if err != nil {
		return err
	}
	req.GetBody = nil // no implicit POST replay
	req.Header.Set(HeaderToken, token)
	req.Header.Set(HeaderFrom, c.machine)
	req.Header.Set(HeaderOnBehalf, "operator") // work admitted by this daemon's existing owner routes
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s is unreachable", transcribe.ErrPending, host)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxReply+1))
	if err != nil || len(b) > maxReply {
		return fmt.Errorf("%w: unreadable response from %s", transcribe.ErrPending, host)
	}
	if res.StatusCode >= 500 || res.StatusCode == 429 {
		return fmt.Errorf("%w: %s returned HTTP %d", transcribe.ErrPending, host, res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		var reply struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Detail  string `json:"detail"`
		}
		_ = json.Unmarshal(b, &reply)
		msg := reply.Error
		if msg == "" {
			msg = reply.Message
		}
		if msg == "" {
			msg = reply.Detail
		}
		if msg == "" {
			msg = http.StatusText(res.StatusCode)
		}
		return Refused{res.StatusCode, host + ": " + msg + "; audio retained locally"}
	}
	if json.Unmarshal(b, out) != nil {
		return fmt.Errorf("%w: invalid response from %s", transcribe.ErrPending, host)
	}
	return nil
}

// TranscribePeer advances a resumable upload, then polls once. Every acknowledged
// chunk is checkpointed before the next request. Python's recording receipt also
// covers an acknowledgement lost between the remote write and our checkpoint.
func (c *Client) TranscribePeer(ctx context.Context, host, audio string, st *transcribe.PeerUpload, save func() error) (transcribe.Result, error) {
	if err := c.ValidateVoicePeer(host); err != nil {
		return transcribe.Result{}, err
	}
	f, err := os.Open(audio)
	if err != nil {
		return transcribe.Result{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return transcribe.Result{}, err
	}
	if st.Job == "" {
		var limits struct {
			MaxMB float64 `json:"max_mb"`
		}
		err = c.voiceCall(ctx, host, "GET", "/api/voice", nil, &limits)
		var ref Refused
		// Older peers may not expose limits. Their upload refusal is still terminal.
		if err != nil && !(errors.As(err, &ref) && ref.Status == 404) {
			return transcribe.Result{}, err
		}
		if limits.MaxMB > 0 && float64(info.Size()) > limits.MaxMB*(1<<20) {
			return transcribe.Result{}, Refused{413, fmt.Sprintf("too long for %s: %.2f MB, limit %g MB; audio retained locally", host, float64(info.Size())/(1<<20), limits.MaxMB)}
		}
	}
	if st.SHA256 == "" {
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			return transcribe.Result{}, err
		}
		st.SHA256 = hex.EncodeToString(h.Sum(nil))
		st.Size = info.Size()
		if err = save(); err != nil {
			return transcribe.Result{}, err
		}
	}
	if st.Size != info.Size() || st.Offset < 0 || st.Offset > st.Size {
		return transcribe.Result{}, errors.New("queued audio changed; original retained")
	}
	type answer struct {
		Upload   string `json:"upload"`
		Received int64  `json:"received"`
		ID       string `json:"id"`
		State    string `json:"state"`
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	var a answer
	for st.Job == "" {
		if _, err = f.Seek(st.Offset, io.SeekStart); err != nil {
			return transcribe.Result{}, err
		}
		data := make([]byte, min(int64(voiceChunk), st.Size-st.Offset))
		if _, err = io.ReadFull(f, data); err != nil {
			return transcribe.Result{}, err
		}
		body := map[string]any{"recording_id": st.ID, "sha256": st.SHA256, "size": st.Size,
			"ext": strings.TrimPrefix(filepath.Ext(audio), "."), "offset": st.Offset,
			"data": base64.StdEncoding.EncodeToString(data), "final": st.Offset+int64(len(data)) == st.Size}
		if st.Upload != "" {
			body["upload"] = st.Upload
		}
		a = answer{}
		err = c.voiceCall(ctx, host, "POST", "/api/voice/upload", body, &a)
		if err != nil {
			var ref Refused
			if errors.As(err, &ref) && ref.Status == 404 && !st.Reset {
				st.Reset = true
				// Expired partial: a new receipt identity avoids replaying a stale
				// first-chunk acknowledgement from Python's persistent receipt cache.
				st.ID = model.NewID()
				st.Upload = ""
				st.Offset = 0
				if e := save(); e != nil {
					return transcribe.Result{}, e
				}
				return transcribe.Result{}, transcribe.ErrPending
			}
			return transcribe.Result{}, err
		}
		if a.ID != "" {
			if !voiceID.MatchString(a.ID) {
				return transcribe.Result{}, transcribe.ErrPending
			}
			st.Job = a.ID
		} else {
			if !voiceID.MatchString(a.Upload) || a.Received != st.Offset+int64(len(data)) || a.Received >= st.Size {
				return transcribe.Result{}, transcribe.ErrPending
			}
			st.Upload = a.Upload
			st.Offset = a.Received
		}
		if err = save(); err != nil {
			return transcribe.Result{}, err
		}
	}
	if a.ID == "" {
		err = c.voiceCall(ctx, host, "GET", "/api/voice/jobs/"+st.Job, nil, &a)
		if err != nil {
			var ref Refused
			if errors.As(err, &ref) && ref.Status == 404 && !st.Reset {
				st.Reset = true
				st.Job = ""
				st.Upload = ""
				st.Offset = 0
				if e := save(); e != nil {
					return transcribe.Result{}, e
				}
				return transcribe.Result{}, transcribe.ErrPending
			}
			return transcribe.Result{}, err
		}
	}
	switch a.State {
	case "done":
		if strings.TrimSpace(a.Text) == "" {
			return transcribe.Result{}, transcribe.ErrNoSpeech
		}
		return transcribe.Result{Text: a.Text, Language: a.Language, Segments: []model.TranscriptSegment{{Text: a.Text}}}, nil
	case "error", "cancelled":
		return transcribe.Result{}, errors.New("fleet transcription failed; audio retained locally")
	default:
		return transcribe.Result{}, transcribe.ErrPending
	}
}

// ReviewPeer uses Python's local-only review generator, never its cloud backends.
// The returned report's escaped <pre> is decoded as text, never served as active HTML.
func (c *Client) ReviewPeer(ctx context.Context, host, id, text string) (string, error) {
	h := sha256.Sum256([]byte(c.machine + ":" + id + ":" + text))
	rid := "20000101000000-" + hex.EncodeToString(h[:6])
	// A final response may have been lost. Read an already completed report
	// before asking the local model to generate again.
	var existing struct {
		HTML string `json:"html"`
	}
	if err := c.voiceCall(ctx, host, "GET", "/api/voice/review/"+rid, nil, &existing); err == nil {
		if report, ok := reviewText(existing.HTML); ok {
			return report, nil
		}
	} else {
		var ref Refused
		if !errors.As(err, &ref) || ref.Status != 404 {
			return "", err
		}
	}
	var part struct {
		OK bool `json:"ok"`
	}
	if err := c.voiceCall(ctx, host, "POST", "/api/voice/review", map[string]any{"review": rid, "part": 0, "text": text, "seconds": 0}, &part); err != nil {
		return "", err
	}
	if !part.OK {
		return "", transcribe.ErrPending
	}
	var final struct {
		OK      bool    `json:"ok"`
		Pending bool    `json:"summary_pending"`
		Summary *string `json:"summary"`
	}
	if err := c.voiceCall(ctx, host, "POST", "/api/voice/review", map[string]any{"review": rid, "final": true}, &final); err != nil {
		return "", err
	}
	if !final.OK || final.Pending || final.Summary == nil {
		return "", transcribe.ErrPending
	}
	var report struct {
		HTML string `json:"html"`
	}
	if err := c.voiceCall(ctx, host, "GET", "/api/voice/review/"+rid, nil, &report); err != nil {
		return "", err
	}
	value, ok := reviewText(report.HTML)
	if !ok {
		return "", transcribe.ErrPending
	}
	return value, nil
}

func reviewText(page string) (string, bool) {
	_, tail, ok := strings.Cut(page, "<h2>Report</h2><pre>")
	value, _, end := strings.Cut(tail, "</pre>")
	return html.UnescapeString(value), ok && end && strings.TrimSpace(value) != ""
}
