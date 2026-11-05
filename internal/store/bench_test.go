package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func benchmarkStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := s.Close(); err != nil {
			b.Error(err)
		}
	})
	return s
}

// BenchmarkRSIMessageRoundTrip measures the durable storage leg, not network or
// harness delivery. Reuse 32 rows so calibration cannot grow the database forever.
func BenchmarkRSIMessageRoundTrip(b *testing.B) {
	s := benchmarkStore(b)
	messages := make([]model.Message, 32)
	for i := range messages {
		messages[i] = model.Message{ID: fmt.Sprintf("bench-%d", i), From: "sender", To: "receiver",
			Body: strings.Repeat("x", 256), State: model.MsgQueued, CreatedAt: int64(i + 1)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := messages[i%len(messages)]
		if err := s.PutMessage(m); err != nil {
			b.Fatal(err)
		}
		got, err := s.GetMessage(m.ID)
		if err != nil || got != m {
			b.Fatalf("message round trip mismatch: %v", err)
		}
	}
}

// BenchmarkRSIIdleInboxPoll measures the storage cost of checking an empty inbox.
// Whole-daemon idle CPU/RSS requires a separate disposable-runner harness (TODO).
func BenchmarkRSIIdleInboxPoll(b *testing.B) {
	s := benchmarkStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		messages, err := s.ListMessages(MessageFilter{To: "receiver", State: model.MsgQueued})
		if err != nil || len(messages) != 0 {
			b.Fatalf("empty inbox poll failed: %v", err)
		}
	}
}
