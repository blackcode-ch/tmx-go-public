package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/jobs"
)

type syntheticAudit struct {
	remaining int64
	maxRead   *atomic.Int64
}

func (r *syntheticAudit) Read(b []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(b)), r.remaining)
	r.remaining -= n
	r.maxRead.Store(max(r.maxRead.Load(), n))
	clear(b[:n])
	return int(n), nil
}
func (r *syntheticAudit) Close() error { return nil }

// Uses 3.18 GiB of synthetic bytes, never a large file or daemon process.
func TestJobsLargeAuditHealthAndGovernorDeath(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	m, err := jobs.New(jobs.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Fresh: healthy.Load, BytesPerSecond: 8 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		m.Close()
		select {
		case <-m.Done():
		case <-time.After(5 * time.Second):
			t.Error("job workers did not stop")
		}
	}()
	s := httptest.NewServer(api.New(api.Options{Fleet: &fleet.Fleet{}, OwnerToken: "test-owner", Jobs: m}).Handler())
	defer s.Close()
	var maxRead atomic.Int64
	const size = int64(318) * (1 << 30) / 100
	row, err := m.SubmitAudit(jobs.Identity{Caller: "owner", Owner: "owner", Key: "trusted-backup-fixture"}, true, size, func(context.Context) (io.ReadCloser, error) { return &syntheticAudit{size, &maxRead}, nil })
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	maxLatency := time.Duration(0)
	requests := 0
	deadline := time.Now().Add(60 * time.Second)
	for {
		start := time.Now()
		res, err := client.Get(s.URL + "/api/health")
		if err != nil {
			t.Fatal("health failed during audit", err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		elapsed := time.Since(start)
		maxLatency = max(maxLatency, elapsed)
		requests++
		if res.StatusCode != 200 || elapsed >= time.Second {
			t.Fatal("health SLA", res.StatusCode, elapsed)
		}
		r, err := m.Get(row.ID, "owner", true)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == "completed" {
			if r.Bytes != size {
				t.Fatal(r)
			}
			break
		}
		if r.State == "failed" || r.State == "quarantined" {
			t.Fatal(r)
		}
		if time.Now().After(deadline) {
			t.Fatal("audit timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if maxRead.Load() > jobs.ChunkSize || requests < 2 {
		t.Fatal("not streamed", maxRead.Load(), requests)
	}
	t.Logf("synthetic bytes=%d, health requests=%d, maximum latency=%s, buffer=%d", size, requests, maxLatency, maxRead.Load())

	// Terminate the independent governor goroutine while an audit is open.
	governorStop := make(chan struct{})
	governorDone := make(chan struct{})
	go func() { defer close(governorDone); defer healthy.Store(false); <-governorStop }()
	entered := make(chan struct{})
	unblock := make(chan struct{})
	for {
		row, err = m.SubmitAudit(jobs.Identity{Caller: "owner", Owner: "owner", Key: "mid-audit"}, true, 0, func(cx context.Context) (io.ReadCloser, error) {
			close(entered)
			select {
			case <-unblock:
			case <-cx.Done():
			}
			return nil, cx.Err()
		})
		if !errors.Is(err, jobs.ErrBusy) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	close(governorStop)
	<-governorDone
	for _, path := range []string{"/api/health", "/api/jobs/" + row.ID, "/api/jobs/" + row.ID + "/cancel"} {
		method := "GET"
		if path[len(path)-6:] == "cancel" {
			method = "POST"
		}
		req, _ := http.NewRequest(method, s.URL+path, nil)
		req.Header.Set("Authorization", "Bearer test-owner")
		started := time.Now()
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if res.StatusCode >= 300 || time.Since(started) >= time.Second {
			t.Fatal("dead governor control SLA", path, res.StatusCode)
		}
		if path == "/api/health" && body["governor_degraded"] != true {
			t.Fatal(body)
		}
	}
	close(unblock)
}
