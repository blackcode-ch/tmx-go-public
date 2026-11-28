package accounts

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsAgyBackgroundSuppressionIsLive(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":"fixture@example.invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	s := &scope{Service: &Service{log: slog.New(slog.DiscardHandler), o: Options{Dir: home, AgyBackgroundUsage: func() bool { return enabled }}}, home: []string{"USERPROFILE=" + home}}
	if s.agyQuotaOnDemand() {
		t.Fatal("automatic quota should start enabled")
	}
	enabled = false
	path := filepath.Join(home, "fixture-not-executable.exe")
	s.agyProbe(path, func(p *agyProbe) { p.authChecked = true })
	got := agyCheck(context.Background(), s, path)
	if !got.connected || got.err != "" || got.note != "Quota not measured automatically." {
		t.Fatalf("suppressed poll did not preserve sign-in state: %+v", got)
	}
	if !s.agyQuotaOnDemand() {
		t.Fatal("disabled background setting did not enable manual measurement")
	}
}
