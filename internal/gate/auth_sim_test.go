//go:build gatesim

package gate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func TestSimulatedApprovesOnlyAnOptedInThrowawayHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	t.Setenv(EnvGatesim, "")
	if err := OS().Authenticate(context.Background(), "pair a device"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("TMXG_HOME alone (a real install sets it too): got %v, want ErrUnavailable", err)
	}
	t.Setenv(EnvGatesim, t.TempDir())
	if OS().Available() == nil {
		t.Fatal("an opt-in naming another directory was accepted")
	}

	t.Setenv(EnvGatesim, home)
	a := OS()
	if a.Method() != MethodSimulated {
		t.Fatal("gatesim build did not select the simulated authenticator")
	}
	if err := a.Authenticate(context.Background(), "pair a device"); err != nil {
		t.Fatalf("opted-in throwaway home: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Authenticate(ctx, "pair a device"); err == nil {
		t.Fatal("a withdrawn request was approved")
	}
}

func TestSimulatedRefusesTheDefaultHome(t *testing.T) {
	def, err := config.OSDefaultHome()
	if err != nil {
		t.Skip(err)
	}
	if simulatedRefusal(def, def) == nil {
		t.Fatal("the per-OS default state directory was accepted")
	}
}

func TestSimulatedRefusesAHomeWithRealState(t *testing.T) {
	for _, f := range []string{"gate.json", "browser-devices.json"} {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, "secrets"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "secrets", f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if simulatedRefusal(home, home) == nil {
			t.Fatalf("a home holding %s was accepted", f)
		}
	}
	for _, cfg := range []string{`{"profile":"production"}`, `{not json`} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		if simulatedRefusal(home, home) == nil {
			t.Fatalf("a home with config.json %s was accepted", cfg)
		}
	}
	trial := t.TempDir()
	if err := os.WriteFile(filepath.Join(trial, "config.json"), []byte(`{"profile":"trial","port":8901}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := simulatedRefusal(trial, trial); err != nil {
		t.Fatalf("a fresh trial throwaway was refused: %v", err)
	}
	home := t.TempDir()
	if err := os.WriteFile(home+".gate-enrolled", []byte("on"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(home + ".gate-enrolled") })
	if simulatedRefusal(home, home) == nil {
		t.Fatal("a home with an enrolled gate marker was accepted")
	}
}
