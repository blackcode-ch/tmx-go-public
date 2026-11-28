package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestQuotaWindowRestartPreservesProofOfEarlyReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-windows.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	r1 := start.Add(3 * time.Hour).UnixMilli()
	if ok, err := s.ObserveAccountQuotaWindow("weekly", start.UnixMilli(), r1, 93); !ok || err != nil {
		t.Fatalf("initial observation: %v %v", ok, err)
	}
	for _, key := range []string{"weekly/80", "weekly/90"} {
		m := model.Message{ID: key, From: model.WatchParty, To: "head", Body: key, State: model.MsgQueued}
		if ok, err := s.QueueAccountUsageNotice(key, start.UnixMilli(), r1, m); !ok || err != nil {
			t.Fatalf("initial mail: %v %v", ok, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reset := start.Add(10 * time.Minute)
	r2 := reset.Add(7 * 24 * time.Hour).UnixMilli()
	if ok, err := s.ObserveAccountQuotaWindow("weekly", reset.UnixMilli(), r2, 3); !ok || err != nil {
		t.Fatalf("post-reset observation: %v %v", ok, err)
	}
	for _, key := range []string{"weekly/80", "weekly/90"} {
		m := model.Message{ID: key + "-new", From: model.WatchParty, To: "head", Body: key, State: model.MsgQueued}
		if ok, err := s.QueueAccountUsageNotice(key, reset.Add(time.Minute).UnixMilli(), r2, m); !ok || err != nil {
			t.Fatalf("missing new-window mail: %v %v", ok, err)
		}
	}
}

func TestAccountUsageNoticeClaimSurvivesRestartAndIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := model.Message{ID: "first", From: model.WatchParty, To: "head", Body: "80%", State: model.MsgQueued}
	if got, err := s.QueueAccountUsageNotice("80", 10, 20, m); !got || err != nil {
		t.Fatalf("initial notice: %v %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m.ID = "second"
	if got, err := s.QueueAccountUsageNotice("80", 15, 22, m); got || err != nil {
		t.Fatalf("duplicate after reopen: %v %v", got, err)
	}
	if got, err := s.QueueAccountUsageNotice("80", 21, 22, m); got || err != nil {
		t.Fatalf("duplicate after deadline correction: %v %v", got, err)
	}
	m.ID = "first"
	if _, err := s.QueueAccountUsageNotice("80", 23, 30, m); err == nil {
		t.Fatal("expected conflicting message ID to reject both writes")
	}
	m.ID = "third"
	if got, err := s.QueueAccountUsageNotice("80", 23, 30, m); !got || err != nil {
		t.Fatalf("could not retry failed enqueue: %v %v", got, err)
	}
	for _, tc := range []struct {
		id           string
		now, expires int64
		want         bool
	}{
		{"unknown-first", 30, 0, true},
		{"unknown-again", 31, 0, false},
		{"known-current", 32, 40, false},
		{"missing-next", 41, 0, false},
		{"known-next", 42, 50, true},
	} {
		m.ID = tc.id
		if got, err := s.QueueAccountUsageNotice("unknown-90", tc.now, tc.expires, m); got != tc.want || err != nil {
			t.Fatalf("%s: got %v want %v: %v", tc.id, got, tc.want, err)
		}
	}
}
