package transfers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/antechamber"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testPeer struct {
	name, known string
	data        []byte
	mu          sync.Mutex
	offers      []Record
	offset      int64
}

func (p *testPeer) Known(h string) bool {
	if p.known != "" {
		return h == p.known
	}
	return h == "source"
}
func (p *testPeer) Machine() string {
	if p.name != "" {
		return p.name
	}
	return "target"
}
func (p *testPeer) Call(ctx context.Context, h, m, path string, body, out any) error {
	if path == "/api/transfers/incoming" {
		b, _ := json.Marshal(body)
		var rec Record
		_ = json.Unmarshal(b, &rec)
		p.mu.Lock()
		p.offers = append(p.offers, rec)
		p.mu.Unlock()
		b = []byte(`{"id":"0123456789abcdef","state":"queued"}`)
		return json.Unmarshal(b, out)
	}
	return nil
}
func (p *testPeer) ExportStream(ctx context.Context, h, ticket string, offset int64) (*http.Response, error) {
	p.mu.Lock()
	p.offset = offset
	p.mu.Unlock()
	code := 200
	headers := http.Header{}
	if offset > 0 {
		code = 206
		headers.Set("Content-Range", "bytes "+fmtInt(offset)+"-"+fmtInt(int64(len(p.data))-1)+"/"+fmtInt(int64(len(p.data))))
	}
	return &http.Response{StatusCode: code, Header: headers, Body: io.NopCloser(bytes.NewReader(p.data[offset:]))}, nil
}
func fmtInt(n int64) string { return fmt.Sprintf("%d", n) }
func newTestService(t *testing.T, data []byte) (*Service, *testPeer) {
	t.Helper()
	home := t.TempDir()
	p := &testPeer{data: data}
	a, err := antechamber.New(filepath.Join(home, "antechamber"), "target")
	if err != nil {
		t.Fatal(err)
	}
	a.Isolate = false
	a.AV = antechamber.NoAV{}
	s, err := New(Options{Home: home, Install: t.TempDir(), Peers: p, Antechamber: a, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)), DiskSpace: func(string) (uint64, uint64, error) { return 1 << 40, 2 << 40, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, p
}
func offerFor(data []byte, dest string) Record {
	sum := sha256.Sum256(data)
	return Record{"from": "source", "ticket": "abcdefghijklmnopqrstuvwx", "name": "hello.txt", "kind": "file", "size": int64(len(data)), "sha256": hex.EncodeToString(sum[:]), "dest": dest, "force": false, "out_id": "fedcba9876543210", "backup": false, "trusted_backup": false}
}
func waitState(t *testing.T, s *Service, id string) Record {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		r, e := ReadRecord(s.Root, id)
		if e == nil && (done(r.str("state")) || r.str("state") == "waiting_approval" || r.str("state") == "offered") {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, _ := ReadRecord(s.Root, id)
	t.Fatalf("transfer did not settle: %v", r)
	return nil
}
func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Status != status {
		t.Fatalf("want %d, got %v", status, err)
	}
}

func TestReceiveAndForceKeepsOldCopy(t *testing.T) {
	data := []byte("hello from Python\n")
	s, _ := newTestService(t, data)
	dest := t.TempDir()
	path := filepath.Join(dest, "hello.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	b := offerFor(data, dest)
	_, err := s.Incoming(b, "source")
	requireStatus(t, err, 409)
	b["force"] = true
	r, err := s.Incoming(b, "source")
	if err != nil {
		t.Fatal(err)
	}
	r = waitState(t, s, r.str("id"))
	if r.str("state") != "placed" {
		t.Fatal(r)
	}
	got, _ := os.ReadFile(path)
	old, _ := os.ReadFile(r.str("replaced_to"))
	if !bytes.Equal(got, data) || string(old) != "old" {
		t.Fatalf("copy=%q old=%q", got, old)
	}
	if len(s.List()) != 1 {
		t.Fatal(s.List())
	}
}
func TestReceiveRefusalsAndHashMismatch(t *testing.T) {
	data := []byte("hello\n")
	s, _ := newTestService(t, data)
	for _, dest := range []string{s.Install, s.Home} {
		_, err := s.Incoming(offerFor(data, dest), "source")
		requireStatus(t, err, 403)
	}
	b := offerFor(data, t.TempDir())
	_, err := s.Incoming(b, "impostor")
	requireStatus(t, err, 403)
	b["size"] = int64(MaxFile + 1)
	_, err = s.Incoming(b, "source")
	requireStatus(t, err, 413)
	b["size"] = int64(-1)
	_, err = s.Incoming(b, "source")
	requireStatus(t, err, 400)
	b = offerFor(data, t.TempDir())
	b["sha256"] = strings.Repeat("0", 64)
	r, err := s.Incoming(b, "source")
	if err != nil {
		t.Fatal(err)
	}
	r = waitState(t, s, r.str("id"))
	if r.str("state") != "failed" || !strings.Contains(r.str("error"), "SHA-256") {
		t.Fatal(r)
	}
	if exists(filepath.Join(b.str("dest"), "hello.txt")) {
		t.Fatal("hash mismatch placed a file")
	}
}
func TestDiskReserveRefusesBeforePull(t *testing.T) {
	s, _ := newTestService(t, []byte("x"))
	s.DiskSpace = func(string) (uint64, uint64, error) { return 1 << 30, 10 << 30, nil }
	_, err := s.Incoming(offerFor([]byte("x"), t.TempDir()), "source")
	requireStatus(t, err, 507)
	if len(s.List()) != 0 {
		t.Fatal("record created despite reserve refusal")
	}
}
func TestSecretFolderSourceIsRefused(t *testing.T) {
	s, _ := newTestService(t, nil)
	secret := t.TempDir()
	s.SecretFolders = []string{secret}
	_, err := s.Start(context.Background(), Record{"src": secret, "to": "source", "dest": "/dest"}, "peer")
	requireStatus(t, err, 403)
}
func TestStartPythonShapeAndStatus(t *testing.T) {
	data := []byte("hello\n")
	s, p := newTestService(t, data)
	src := filepath.Join(t.TempDir(), "hello.txt")
	_ = os.WriteFile(src, data, 0600)
	r, err := s.Start(context.Background(), Record{"src": src, "to": "source", "dest": "/destination", "force": false}, "peer")
	if err != nil {
		t.Fatal(err)
	}
	r = waitState(t, s, r.str("id"))
	if r.str("state") != "offered" {
		t.Fatal(r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.offers) != 1 {
		t.Fatal(p.offers)
	}
	offer := p.offers[0]
	if offer.str("from") != "target" || offer.str("dest") != "/destination" || offer["backup"] != false || offer["trusted_backup"] != false || offer.str("out_id") != r.str("id") {
		t.Fatal(offer)
	}
}
func TestBackupModesAreSeparate(t *testing.T) {
	data := []byte("cold archive\n")
	s, _ := newTestService(t, data)
	b := offerFor(data, t.TempDir())
	b["trusted_backup"] = true
	_, err := s.Incoming(b, "source")
	requireStatus(t, err, 403)
	s.TrustedBackupRoots = []string{b.str("dest")}
	r, err := s.Incoming(b, "source")
	if err != nil {
		t.Fatal(err)
	}
	r = waitState(t, s, r.str("id"))
	if r.str("state") != "placed" || !strings.HasSuffix(r.str("placed"), ".held") {
		t.Fatal(r)
	}
	if !exists(filepath.Join(r.str("dest"), "files.jsonl")) || !exists(filepath.Join(r.str("dest"), "manifest.json")) {
		t.Fatal("missing cold archive audit")
	}
	if r.str("item") != "" {
		t.Fatal("cold backup went through release quarantine")
	}
}
func TestResumeRange(t *testing.T) {
	data := []byte("hello world\n")
	s, p := newTestService(t, data)
	dest := t.TempDir()
	b := offerFor(data, dest)
	r := clone(b)
	r["id"], r["dir"], r["state"], r["host"], r["created"] = "1122334455667788", "in", "pulling", "target", now()
	if err := s.save(r); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(s.part(r), data[:5], 0600)
	s.Resume()
	r = waitState(t, s, r.str("id"))
	if r.str("state") != "placed" {
		t.Fatal(r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.offset != 5 {
		t.Fatalf("resume offset %d", p.offset)
	}
}
