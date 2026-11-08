package peers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

type voiceTransport func(*http.Request) (*http.Response, error)

func (f voiceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func voiceResponse(v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}
}

func TestVoiceChunksResumeAndReport(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	c := New(config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{{Name: "hosta", URL: "https://hosta.example.ts.net"}}}, p, nil)
	tok := NewPairToken()
	if _, err := c.Tokens().Store("hosta", tok, "test"); err != nil {
		t.Fatal(err)
	}
	audio := bytes.Repeat([]byte{42}, voiceChunk*2+17)
	path := filepath.Join(t.TempDir(), "test.wav")
	if err := os.WriteFile(path, audio, 0600); err != nil {
		t.Fatal(err)
	}
	var received []byte
	chunks, checks := 0, 0
	down := true
	reportCalls := 0
	c.SetTransport(voiceTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/voice" {
			return voiceResponse(map[string]any{"max_mb": 300}), nil
		}
		if r.URL.Host != "hosta.example.ts.net" || r.Header.Get(HeaderToken) != tok || r.Header.Get(HeaderFrom) != "hostb" || r.Header.Get(HeaderOnBehalf) != "operator" {
			t.Fatalf("wrong peer identity or transport")
		}
		switch r.URL.Path {
		case "/api/voice/upload":
			var b struct {
				Data, Upload, RecordingID, SHA256 string
				Offset, Size                      int64
				Final                             bool
			}
			var raw map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatal(err)
			}
			packed, _ := json.Marshal(raw)
			_ = json.Unmarshal(packed, &b)
			var rid, sha string
			_ = json.Unmarshal(raw["recording_id"], &rid)
			_ = json.Unmarshal(raw["sha256"], &sha)
			if rid == "" || len(sha) != 64 {
				t.Fatal("missing idempotent recording identity")
			}
			if b.Offset == voiceChunk && down {
				down = false
				return nil, errors.New("peer offline")
			}
			if b.Offset != int64(len(received)) {
				t.Fatalf("resumed offset %d, received %d", b.Offset, len(received))
			}
			if b.Offset > 0 && b.Upload != "0123456789abcdef" {
				t.Fatal("lost upload id")
			}
			part, err := base64.StdEncoding.DecodeString(b.Data)
			if err != nil || len(part) > voiceChunk {
				t.Fatal("invalid chunk")
			}
			received = append(received, part...)
			chunks++
			if b.Final {
				return voiceResponse(map[string]any{"id": "abcdef0123456789", "state": "queued"}), nil
			}
			return voiceResponse(map[string]any{"upload": "0123456789abcdef", "received": len(received)}), nil
		case "/api/voice/jobs/abcdef0123456789":
			return voiceResponse(map[string]string{"id": "abcdef0123456789", "state": "done", "text": "Synthetic speech", "language": "en"}), nil
		case "/api/voice/review":
			reportCalls++
			return voiceResponse(map[string]any{"ok": true, "summary": "summary.md"}), nil
		default:
			if reportCalls == 0 {
				res := voiceResponse(map[string]string{"error": "no review yet"})
				res.StatusCode = 404
				return res, nil
			}
			return voiceResponse(map[string]string{"html": "<h2>Report</h2><pre>A &amp; B</pre><h2>Transcript</h2>"}), nil
		}
	}))
	st := transcribe.PeerUpload{ID: "recording-1"}
	var checkpoint []byte
	save := func() error { checks++; checkpoint, _ = json.Marshal(st); return nil }
	if _, err := c.TranscribePeer(context.Background(), "hosta", path, &st, save); !errors.Is(err, transcribe.ErrPending) {
		t.Fatalf("outage: %v", err)
	}
	if st.Offset != voiceChunk {
		t.Fatalf("checkpoint: %+v", st)
	}
	// Simulate a restart by reloading the last persisted acknowledgement.
	st = transcribe.PeerUpload{}
	if err := json.Unmarshal(checkpoint, &st); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TranscribePeer(context.Background(), "hosta", path, &st, save); !errors.Is(err, transcribe.ErrPending) {
		t.Fatalf("queued job: %v", err)
	}
	got, err := c.TranscribePeer(context.Background(), "hosta", path, &st, save)
	if err != nil || got.Text != "Synthetic speech" || chunks != 3 || checks < 4 || !bytes.Equal(received, audio) {
		t.Fatalf("result=%+v error=%v chunks=%d", got, err, chunks)
	}
	report, err := c.ReviewPeer(context.Background(), "hosta", st.ID, got.Text)
	if err != nil || report != "A & B" || reportCalls != 2 {
		t.Fatalf("report=%q error=%v", report, err)
	}
	if _, err = c.ReviewPeer(context.Background(), "hosta", st.ID, got.Text); err != nil || reportCalls != 2 {
		t.Fatal("completed report regenerated", err)
	}
}

func TestVoiceRejectsNonFleetTargets(t *testing.T) {
	for _, target := range []struct {
		name, url string
		paired    bool
	}{
		{"unknown", "https://hosta.example.ts.net", true},
		{"hosta", "https://hosta.example.ts.net", false},
		{"hosta", "http://hosta.example.ts.net", true},
		{"hosta", "https://100.64.1.2", true},
		{"hosta", "https://192.168.1.2:8777", true},
	} {
		t.Run(target.name+target.url, func(t *testing.T) {
			c := New(config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{{Name: "hosta", URL: target.url}}}, config.Paths{Home: t.TempDir()}, nil)
			if target.paired {
				_, _ = c.Tokens().Store("hosta", NewPairToken(), "test")
			}
			c.SetTransport(voiceTransport(func(*http.Request) (*http.Response, error) { t.Fatal("refused audio reached network"); return nil, nil }))
			_, err := c.TranscribePeer(context.Background(), target.name, "absent.wav", &transcribe.PeerUpload{}, func() error { return nil })
			if !errors.Is(err, transcribe.ErrInvalid) {
				t.Fatalf("target accepted: %v", err)
			}
		})
	}
}

func TestVoiceLostFinalAcknowledgementReplaysIdentity(t *testing.T) {
	c := New(config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{{Name: "hosta", URL: "https://hosta.example.ts.net"}}}, config.Paths{Home: t.TempDir()}, nil)
	_, _ = c.Tokens().Store("hosta", NewPairToken(), "test")
	path := filepath.Join(t.TempDir(), "test.wav")
	os.WriteFile(path, []byte("synthetic audio"), 0600)
	var original []byte
	c.SetTransport(voiceTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/voice" {
			return voiceResponse(map[string]any{"max_mb": 300}), nil
		}
		b, _ := io.ReadAll(r.Body)
		if original == nil {
			original = b
			return nil, errors.New("acknowledgement lost after remote commit")
		}
		if !bytes.Equal(b, original) {
			t.Fatal("retry changed the recording identity or chunk")
		}
		return voiceResponse(map[string]string{"id": "abcdef0123456789", "state": "done", "text": "Recovered"}), nil
	}))
	st := transcribe.PeerUpload{ID: "recording-1"}
	if _, err := c.TranscribePeer(context.Background(), "hosta", path, &st, func() error { return nil }); !errors.Is(err, transcribe.ErrPending) {
		t.Fatal(err)
	}
	got, err := c.TranscribePeer(context.Background(), "hosta", path, &st, func() error { return nil })
	if err != nil || got.Text != "Recovered" {
		t.Fatal(got, err)
	}
}

func TestVoiceRefusalsAndTransientRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		limit  float64
		want   string
	}{
		{"400", 400, 300, "Python refusal"}, {"403", 403, 300, "Python refusal"},
		{"413", 413, 300, "Python refusal"}, {"415", 415, 300, "Python refusal"},
		{"404-after-reset", 404, 300, "Python refusal"},
		{"503", 503, 300, "HTTP 503"}, {"429", 429, 300, "HTTP 429"},
		{"oversize", 200, 0.000001, "too long for hosta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			paths := config.Paths{Home: home}
			c := New(config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{{Name: "hosta", URL: "https://hosta.example.ts.net"}}}, paths, nil)
			if _, err := c.Tokens().Store("hosta", NewPairToken(), "test"); err != nil {
				t.Fatal(err)
			}
			var uploads atomic.Int32
			c.SetTransport(voiceTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/voice" {
					return voiceResponse(map[string]any{"max_mb": tc.limit}), nil
				}
				uploads.Add(1)
				res := voiceResponse(map[string]string{"error": "Python refusal: file too large; limit 300 MB"})
				res.StatusCode = tc.status
				return res, nil
			}))
			db, err := store.Open(filepath.Join(home, "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			svc := transcribe.New(transcribe.Options{Paths: paths, Store: db, Peer: c, PeerRetry: 20 * time.Millisecond, Settings: func() config.VoiceConfig { return config.VoiceConfig{ViaPeer: "hosta"} }, DiskFree: func(string) (uint64, error) { return 20 << 30, nil }})
			if err = svc.Start(); err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			audio := []byte("RIFF0000WAVEsynthetic")
			_, err = svc.TranscribeFile(context.Background(), bytes.NewReader(audio), "")
			transient := tc.status == 503 || tc.status == 429
			if tc.status != 404 && errors.Is(err, transcribe.ErrPending) != transient {
				t.Fatalf("unexpected caller result: %v", err)
			}
			deadline := time.Now().Add(4 * time.Second)
			var tr model.Transcript
			for time.Now().Before(deadline) {
				rows, e := db.ListTranscripts(store.TranscriptQuery{})
				if e != nil {
					t.Fatal(e)
				}
				if len(rows) == 1 {
					tr = rows[0]
				}
				if (transient && uploads.Load() >= 2 && tr.State == model.TranscriptQueued) || (!transient && tr.State == model.TranscriptFailed) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			wantState := model.TranscriptFailed
			if transient {
				wantState = model.TranscriptQueued
			}
			if tr.State != wantState || !strings.Contains(tr.Error, tc.want) {
				t.Fatalf("state/message: %+v", tr)
			}
			if transient && !strings.Contains(tr.Error, "retry in 40ms") {
				t.Fatalf("no increasing backoff: %s", tr.Error)
			}
			kept, err := os.ReadFile(filepath.Join(paths.VoiceDir(), "pending", tr.ID+".wav"))
			if err != nil || !bytes.Equal(kept, audio) {
				t.Fatalf("audio not retained: %v", err)
			}
			if !transient {
				count := uploads.Load()
				time.Sleep(1100 * time.Millisecond)
				if uploads.Load() != count {
					t.Fatal("permanent refusal retried")
				}
				expected := int32(1)
				if tc.name == "oversize" {
					expected = 0
				}
				if tc.status == 404 {
					expected = 2
				}
				if count != expected {
					t.Fatalf("upload attempts %d, want %d", count, expected)
				}
			}
		})
	}
}
