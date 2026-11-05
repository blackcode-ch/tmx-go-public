//go:build darwin && cgo && !gatesim

package gate

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLocalAuthWithdrawn shows the real macOS prompt and withdraws it after two seconds. It
// puts a dialog on the screen, so it runs only with TMXG_LIVE_GATE=1.
func TestLocalAuthWithdrawn(t *testing.T) {
	if os.Getenv("TMXG_LIVE_GATE") != "1" {
		t.Skip("shows a system dialog; set TMXG_LIVE_GATE=1")
	}
	a := OS()
	if err := a.Available(); err != nil {
		t.Skipf("not available here: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := a.Authenticate(ctx, "check that tmx can ask you (test; it closes by itself)")
	if err == nil {
		t.Fatal("nobody answered, yet the prompt succeeded")
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("withdrawing took %s", d)
	}
	t.Logf("withdrawn after %s: %v", time.Since(start).Round(time.Millisecond), err)
}

func TestLocalAuthMetadata(t *testing.T) {
	a := OS()
	if a.Method() != "touchid" || a.Label() == "" {
		t.Fatal(a.Method())
	}
	_ = a.Available() // must not panic or prompt
}
