package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

var ErrPending = errors.New("transcription pending")

type localOnlyKey struct{}

// LocalOnly preserves the existing local-host fallback's no-forwarding contract.
func LocalOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, localOnlyKey{}, true)
}

// PendingError identifies retained audio; GET /api/transcripts/{id} follows it.
type PendingError struct{ ID string }

func (p PendingError) Error() string { return "transcription pending" }
func (p PendingError) Unwrap() error { return ErrPending }

// PeerUpload is persisted after each acknowledged upload chunk, without credentials.
type PeerUpload struct {
	ID     string `json:"id"`
	Reset  bool   `json:"reset,omitempty"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Upload string `json:"upload"`
	Offset int64  `json:"offset"`
	Job    string `json:"job"`
}

type VoicePeer interface {
	ValidateVoicePeer(string) error
	TranscribePeer(context.Context, string, string, *PeerUpload, func() error) (Result, error)
	ReviewPeer(context.Context, string, string, string) (string, error)
}

// SetPeer is startup wiring, before Start. No owner/auth surface is added.
func (s *Service) SetPeer(p VoicePeer) { s.o.Peer = p }
func (s *Service) ViaPeer() string     { return strings.ToLower(strings.TrimSpace(s.o.Settings().ViaPeer)) }
func (s *Service) peerDir() string     { return filepath.Join(s.voiceDir(), "pending") }

func (s *Service) diskCheck(dir string, bytes int64) error {
	free, _, err := sysinfo.DiskSpace(dir)
	if s.o.DiskFree != nil {
		free, err = s.o.DiskFree(dir)
	}
	if err != nil {
		return fmt.Errorf("cannot check recording disk: %w", err)
	}
	if bytes < 0 || free < uint64(bytes)+(5<<30) {
		return errors.New("recording needs more free disk space (5 GB reserve)")
	}
	return nil
}

// spoolPeer keeps direct requests outside tmp (which Start removes). The source
// row and file survive request cancellation, network loss, and daemon restart.
func (s *Service) spoolPeer(ctx context.Context, r io.Reader, kind, language string) (Result, error) {
	if ctx.Value(localOnlyKey{}) == true {
		return Result{}, fmt.Errorf("%w: fleet routing is disabled for a local-only request", ErrNotSetUp)
	}
	if language != "" && language != "auto" && !langRx.MatchString(language) {
		return Result{}, fmt.Errorf("%w: invalid language", ErrInvalid)
	}
	if s.o.Store == nil {
		return Result{}, errors.New("fleet transcription requires the transcript store")
	}
	if err := os.MkdirAll(s.peerDir(), 0700); err != nil {
		return Result{}, err
	}
	if err := s.diskCheck(s.peerDir(), MaxFileBytes); err != nil {
		return Result{}, err
	}
	id := model.NewID()
	audio := filepath.Join(s.peerDir(), id+"."+kind)
	f, err := os.OpenFile(audio, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Result{}, err
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxFileBytes+1))
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil || n > MaxFileBytes {
		os.Remove(audio)
		if err == nil {
			err = fmt.Errorf("%w: audio exceeds 512 MB", ErrInvalid)
		}
		return Result{}, err
	}
	t := model.Transcript{ID: id, Source: "upload:" + id, State: model.TranscriptQueued, Model: "peer:" + s.ViaPeer(), Language: language, Error: ErrPending.Error(), CreatedAt: model.Now(), UpdatedAt: model.Now()}
	if err = s.o.Store.PutTranscript(t, audio); err != nil {
		os.Remove(audio)
		return Result{}, err
	}
	j := &job{tid: id, source: t.Source, audio: audio, language: language, peer: s.ViaPeer(), label: t.Source, done: make(chan jobOut, 1), report: true}
	done := j.done
	s.push(j)
	select {
	case out := <-done:
		return out.r, out.err
	case <-ctx.Done():
		return Result{}, PendingError{ID: id}
	case <-s.ctx.Done():
		return Result{}, PendingError{ID: id}
	case <-time.After(2 * time.Second):
		return Result{}, PendingError{ID: id}
	}
}

type peerCheckpoint struct {
	Upload PeerUpload `json:"upload"`
	Result *Result    `json:"result,omitempty"`
}

func (s *Service) checkpoint(id string) string { return filepath.Join(s.peerDir(), id+".json") }
func (s *Service) peerTranscribe(ctx context.Context, j *job) (Result, string, error) {
	name := "peer:" + j.peer
	if s.o.Peer == nil {
		return Result{}, name, fmt.Errorf("%w: peer %s is unavailable", ErrPending, j.peer)
	}
	if err := s.diskCheck(filepath.Dir(j.audio), 0); err != nil {
		return Result{}, name, err
	}
	if err := os.MkdirAll(s.peerDir(), 0700); err != nil {
		return Result{}, name, err
	}
	var cp peerCheckpoint
	if b, err := s.readCheckpoint(j.tid); err == nil {
		if err = json.Unmarshal(b, &cp); err != nil {
			return Result{}, name, err
		}
	} else if !os.IsNotExist(err) {
		return Result{}, name, err
	}
	if cp.Upload.ID == "" {
		cp.Upload.ID = model.NewID()
	}
	save := func() error {
		s.peerFilesMu.Lock()
		defer s.peerFilesMu.Unlock()
		b, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		p := s.checkpoint(j.tid)
		if err = os.WriteFile(p+".tmp", b, 0600); err != nil {
			return err
		}
		return os.Rename(p+".tmp", p)
	}
	if err := save(); err != nil {
		return Result{}, name, err
	}
	if cp.Result == nil {
		r, err := s.o.Peer.TranscribePeer(ctx, j.peer, j.audio, &cp.Upload, save)
		if err != nil {
			return Result{}, name, err
		}
		cp.Result = &r
		if err = save(); err != nil {
			return Result{}, name, err
		}
	}
	if j.report && cp.Result.Report == "" {
		report, err := s.o.Peer.ReviewPeer(ctx, j.peer, j.tid, cp.Result.Text)
		if err != nil {
			return Result{}, name, err
		}
		cp.Result.Report = report
		if err = save(); err != nil {
			return Result{}, name, err
		}
	}
	return *cp.Result, name, nil
}

// FleetReport is used by the recording summary pipeline. Selected fleet work
// never falls through to Claude, even while the peer is offline.
func (s *Service) FleetReport(ctx context.Context, host, id, text string) (string, error) {
	if s.o.Peer == nil {
		return "", ErrPending
	}
	return s.o.Peer.ReviewPeer(ctx, host, id, text)
}

func (s *Service) fillPeerReport(t *model.Transcript) {
	if !strings.HasPrefix(t.Model, "peer:") {
		return
	}
	b, err := s.readCheckpoint(t.ID)
	if err != nil {
		return
	}
	var cp peerCheckpoint
	if json.Unmarshal(b, &cp) == nil && cp.Result != nil {
		t.Report = cp.Result.Report
	}
}

// retryPeer retains the existing queue state, with a bounded retry frequency.
func (s *Service) retryPeer(j *job, t model.Transcript, reason error) {
	delay := s.o.PeerRetry
	for i := 0; i < j.peerRetries && delay < 15*time.Minute; i++ {
		delay = min(delay*2, 15*time.Minute)
	}
	j.peerRetries++
	j.retryAt = time.Now().Add(delay)
	t.State, t.Error, t.UpdatedAt = model.TranscriptQueued, fmt.Sprintf("%s; retry in %s", reason, delay), model.Now()
	s.save(j, t)
	s.push(j)
}

func validatePeerName(name string) bool { return name == "" || config.ValidHostName(name) }

// Only our private spool files are removed. Native recordings remain owned by
// the recorder and follow its existing deletion/retention rules.
func (s *Service) removePeerFiles(id string, audio bool) {
	s.peerFilesMu.Lock()
	defer s.peerFilesMu.Unlock()
	if !config.ValidHostName(id) {
		return
	}
	exts := []string{"json", "json.tmp"}
	if audio {
		exts = append(exts, "wav", "mp3", "flac", "ogg")
	}
	for _, ext := range exts {
		_ = os.Remove(filepath.Join(s.peerDir(), id+"."+ext))
	}
}

func (s *Service) readCheckpoint(id string) ([]byte, error) {
	s.peerFilesMu.Lock()
	defer s.peerFilesMu.Unlock()
	return os.ReadFile(s.checkpoint(id))
}
