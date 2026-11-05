package cli

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/rollback"
)

func TestWatchProbation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	t0 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		body    string
		status  int
		confirm bool
	}{
		{"healthy", `{"ok":true,"governor_degraded":false}`, 200, true},
		{"degraded governor", `{"ok":true,"governor_degraded":true}`, 200, false},
		{"not ok", `{"ok":false}`, 200, false},
		{"api error", `{"ok":true}`, 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			dir := t.TempDir()
			if err := rollback.BeginProbation(dir, "fleet-v1.0.0", "fleet-v1.1.0", t0); err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64 // minutes after t0; each poll moves it on 4 minutes
			now := func() time.Time { return t0.Add(time.Duration(clock.Add(4)) * time.Minute) }
			var failed atomic.Bool
			done := make(chan struct{})
			go func() {
				watchProbation(context.Background(), log, dir, srv.URL, now, time.Millisecond, func() { failed.Store(true) })
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the watchdog never finished")
			}
			_, on, _ := rollback.OnProbation(dir)
			r, _ := rollback.LastProbation(dir)
			if tc.confirm {
				if on || failed.Load() || r.Outcome != "confirmed" {
					t.Fatalf("on=%v failed=%v result=%+v", on, failed.Load(), r)
				}
				return
			}
			// Never confirmed: at the deadline it asks for the restart; the probation stays for the
			// next start to restore.
			p, _, _ := rollback.OnProbation(dir)
			if !on || !failed.Load() || r.Outcome != "" || !strings.Contains(p.Failed, "not confirmed healthy") {
				t.Fatalf("on=%v failed=%v result=%+v probation=%+v", on, failed.Load(), r, p)
			}
		})
	}
}

func TestWatchProbationWithoutRecord(t *testing.T) {
	called := false
	watchProbation(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir(), "http://127.0.0.1:1/api/health", time.Now, time.Millisecond, func() { called = true })
	if called {
		t.Fatal("no probation, but the watchdog failed the update")
	}
}

// An armed drill fails the update on purpose as soon as it is healthy (it is never confirmed).
func TestWatchProbationDrill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"governor_degraded":false}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := rollback.ArmDrill(dir); err != nil {
		t.Fatal(err)
	}
	if err := rollback.BeginProbation(dir, "fleet-v1.0.0", "fleet-v1.1.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	failed := false
	watchProbation(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), dir, srv.URL, time.Now, time.Millisecond, func() { failed = true })
	p, on, _ := rollback.OnProbation(dir)
	if _, confirmed := rollback.LastProbation(dir); !failed || !on || confirmed || !strings.Contains(p.Failed, "drill") {
		t.Fatalf("failed=%v on=%v confirmed=%v probation=%+v", failed, on, confirmed, p)
	}
}
