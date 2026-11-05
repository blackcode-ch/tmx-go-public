//go:build gatesim

package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// A gatesim build (go build -tags gatesim) replaces the OS authenticator with a simulated owner
// who approves host confirmations without a prompt, so a throwaway test daemon (a bake-off, a
// pairing walk-through) never opens the real Windows Hello or Touch ID on someone's desktop.
// It answers only when every check in simulatedRefusal passes, decided once at start; otherwise
// it is unavailable and the gate fails closed. Never ship it: the release job refuses any
// binary that reports MethodSimulated or carries GatesimBuildMarker.

// EnvGatesim must name the exact $TMXG_HOME, a positive opt-in separate from TMXG_HOME itself
// (real installs set TMXG_HOME too, e.g. the MCP companion entry).
const EnvGatesim = "TMXG_GATESIM"

// Simulated is the gatesim build's authenticator; refusal is nil only for a throwaway home.
type Simulated struct{ refusal error }

// OS returns the simulated authenticator for this process's state directory.
func OS() Authenticator {
	return Simulated{refusal: simulatedRefusal(os.Getenv(config.EnvHome), os.Getenv(EnvGatesim))}
}

func (Simulated) Method() string { return MethodSimulated }
func (Simulated) Label() string  { return "simulated owner (" + GatesimBuildMarker + ")" }

func (s Simulated) Available() error { return s.refusal }

func (s Simulated) Authenticate(ctx context.Context, reason string) error {
	if s.refusal != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, s.refusal)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s gate/simulated: approved without an OS prompt: %s\n", time.Now().Format("2006-01-02T15:04:05.000"), reason)
	return nil
}

// simulatedRefusal is nil only for an explicitly opted-in, fresh throwaway home: TMXG_HOME set,
// TMXG_GATESIM naming that same directory, not the per-OS default, not a production profile,
// and no trace of a real install there (gate.json, paired browser devices, an enrolled gate marker).
func simulatedRefusal(home, optIn string) error {
	if home == "" {
		return errors.New("gatesim: TMXG_HOME is not set")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("gatesim: %v", err)
	}
	if optIn == "" {
		return errors.New("gatesim: " + EnvGatesim + " is not set (it must name the throwaway TMXG_HOME)")
	}
	in, err := filepath.Abs(optIn)
	if err != nil || !samePath(in, abs) {
		return errors.New("gatesim: " + EnvGatesim + " does not name this TMXG_HOME")
	}
	if def, err := config.OSDefaultHome(); err != nil || samePath(def, abs) {
		return errors.New("gatesim: refusing the default state directory")
	}
	p := config.Paths{Home: abs}
	if b, err := os.ReadFile(p.ConfigFile()); err == nil {
		var c struct {
			Profile string `json:"profile"`
		}
		if json.Unmarshal(b, &c) != nil || c.Profile == config.ProfileProduction {
			return errors.New("gatesim: refusing a production (or unreadable) config.json")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gatesim: %v", err)
	}
	for _, f := range []string{"gate.json", "browser-devices.json"} {
		if _, err := os.Lstat(filepath.Join(p.SecretsDir(), f)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("gatesim: this home already holds " + f + " (not a fresh throwaway)")
		}
	}
	if absent, err := platformMarker(p).Absent(); err != nil || !absent {
		return errors.New("gatesim: this home has an owner-gate marker (not a fresh throwaway)")
	}
	return nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
