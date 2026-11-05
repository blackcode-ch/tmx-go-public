package transcribe

import (
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestCloseStopsPendingPublication(t *testing.T) {
	s := New(Options{Publish: func(model.BusEvent) { t.Error("published after Close") }})
	s.lastPub = time.Now()
	s.publish(false)
	s.Close()
	s.mu.Lock()
	pending := s.pubTimer != nil
	s.mu.Unlock()
	if pending {
		t.Fatal("Close left a pending publication timer")
	}
	s.publish(true)
}

func TestCloseWaitsForRunningPublication(t *testing.T) {
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := New(Options{Publish: func(model.BusEvent) {
		close(entered)
		<-release
	}})
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(release) })
	s.lastPub = time.Now()
	s.publish(false)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("delayed publication did not start")
	}
	go func() { s.Close(); close(closed) }()
	// Cancellation confirms Close has started while the callback is blocked.
	<-s.ctx.Done()
	select {
	case <-closed:
		t.Fatal("Close returned while publication was still running")
	case <-time.After(20 * time.Millisecond):
	}
}
