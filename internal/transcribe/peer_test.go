package transcribe

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

type fakeVoicePeer struct {
	down  atomic.Bool
	calls atomic.Int32
}

func (p *fakeVoicePeer) ValidateVoicePeer(host string) error {
	if host != "hosta" {
		return ErrInvalid
	}
	return nil
}
func (p *fakeVoicePeer) TranscribePeer(ctx context.Context, host, audio string, state *PeerUpload, save func() error) (Result, error) {
	p.calls.Add(1)
	if _, err := os.Stat(audio); err != nil {
		return Result{}, err
	}
	state.Offset = 42
	if err := save(); err != nil {
		return Result{}, err
	}
	if p.down.Load() {
		return Result{}, ErrPending
	}
	return Result{Text: "Synthetic speech", Language: "en", Segments: []model.TranscriptSegment{{Text: "Synthetic speech"}}}, nil
}
func (p *fakeVoicePeer) ReviewPeer(context.Context, string, string, string) (string, error) {
	return "Synthetic report", nil
}

func TestPeerPendingSurvivesRestart(t *testing.T) {
	for _, kind := range []string{"dictation", "upload", "recording"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			st, err := store.Open(filepath.Join(home, "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			p := &fakeVoicePeer{}
			p.down.Store(true)
			o := Options{Paths: config.Paths{Home: home}, Store: st, Peer: p, PeerRetry: time.Hour, RecordingsDir: filepath.Join(home, "recordings"), Settings: func() config.VoiceConfig { return config.VoiceConfig{ViaPeer: "hosta"} }, DiskFree: func(string) (uint64, error) { return 20 << 30, nil }}
			s := New(o)
			if err = s.Start(); err != nil {
				t.Fatal(err)
			}
			wav := fallbackWAV(false)
			if kind == "recording" {
				os.MkdirAll(o.RecordingsDir, 0700)
				path := filepath.Join(o.RecordingsDir, "part.wav")
				os.WriteFile(path, wav, 0600)
				err = s.Enqueue("recording:one/1", path, "")
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for p.calls.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond * 10)
				}
			} else {
				if kind == "dictation" {
					_, err = s.Dictate(context.Background(), wav, "")
				} else {
					_, err = s.TranscribeFile(context.Background(), bytes.NewReader(wav), "")
				}
				var pending PendingError
				if !errors.As(err, &pending) || pending.ID == "" {
					t.Fatalf("no pending receipt: %v", err)
				}
			}
			s.Close()
			un, err := st.UnfinishedTranscripts()
			if err != nil || len(un) != 1 {
				t.Fatalf("retained: %+v %v", un, err)
			}
			if _, err = os.Stat(un[0].Audio); err != nil {
				t.Fatal("lost audio", err)
			}
			if !strings.HasPrefix(un[0].Transcript.Error, "transcription pending") {
				t.Fatal(un[0].Transcript)
			}
			p.down.Store(false)
			s = New(o)
			if err = s.Start(); err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				tr, err := s.Get(un[0].Transcript.ID)
				if err != nil {
					t.Fatal(err)
				}
				if tr.State == model.TranscriptDone {
					if tr.Text != "Synthetic speech" {
						t.Fatal(tr)
					}
					if kind != "recording" && tr.Report != "Synthetic report" {
						t.Fatal(tr)
					}
					return
				}
				time.Sleep(time.Millisecond * 10)
			}
			t.Fatal("restart did not retry")
		})
	}
}

func TestPeerSpoolChecksDiskBeforeSending(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &fakeVoicePeer{}
	s := New(Options{Paths: config.Paths{Home: t.TempDir()}, Store: st, Peer: p, Settings: func() config.VoiceConfig { return config.VoiceConfig{ViaPeer: "hosta"} }, DiskFree: func(string) (uint64, error) { return 1 << 30, nil }})
	_, err = s.Dictate(context.Background(), fallbackWAV(false), "")
	if err == nil || p.calls.Load() != 0 {
		t.Fatal("disk reserve ignored", err)
	}
}

func TestPeerRetriesWithoutRestartAndDeleteCleansSpool(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &fakeVoicePeer{}
	p.down.Store(true)
	s := New(Options{Paths: config.Paths{Home: home}, Store: st, Peer: p, PeerRetry: 10 * time.Millisecond, Settings: func() config.VoiceConfig { return config.VoiceConfig{ViaPeer: "hosta"} }, DiskFree: func(string) (uint64, error) { return 20 << 30, nil }})
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Dictate(context.Background(), fallbackWAV(false), "")
	var pending PendingError
	if !errors.As(err, &pending) {
		t.Fatal(err)
	}
	p.down.Store(false)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tr, _ := s.Get(pending.ID)
		if tr.State == model.TranscriptDone {
			if err = s.Delete(pending.ID); err != nil {
				t.Fatal(err)
			}
			files, _ := os.ReadDir(s.peerDir())
			if len(files) != 0 {
				t.Fatal("deleted upload left spool files", files)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no automatic retry")
}

func TestVoicePeerSettingRejectsHostsAndURLs(t *testing.T) {
	s := New(Options{Peer: &fakeVoicePeer{}})
	for _, bad := range []string{"unknown", "https://hosta.example.ts.net", "127.0.0.1", "100.64.1.2", "hosta:8777"} {
		if _, err := s.Configure(SettingsPatch{ViaPeer: &bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", bad, err)
		}
	}
	good := "hosta"
	if _, err := s.Configure(SettingsPatch{ViaPeer: &good}); err != nil {
		t.Fatal(err)
	}
}
