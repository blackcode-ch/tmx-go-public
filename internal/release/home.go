// Package release is the fleet release signer (`tmx release keygen|sign`, DESIGN.md "Updates",
// "Signing a release"): the Ed25519 fleet release key on the release machine, the gates a tag
// must pass before it is signed, the signing and the local release log.
package release

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// EnvHome overrides the release home in a Go test binary only (testing.Testing()); a release
// build ignores it, so the key and the release log (gate e) always live at the fixed path.
const EnvHome = "TMXG_RELEASE_HOME"

// Home is the release home: $TMXG_RELEASE_HOME, else %LOCALAPPDATA%\tmxg-release on Windows,
// ~/Library/Application Support/tmxg-release on macOS, $XDG_STATE_HOME/tmxg-release (default
// ~/.local/state) elsewhere; $TMXG_RELEASE_HOME only in tests. In a test binary a home outside os.TempDir() is refused.
func Home() (string, error) {
	h, err := defaultHome()
	if err != nil {
		return "", err
	}
	if err := config.CheckTestModePath(h); err != nil {
		return "", err
	}
	return h, nil
}

func defaultHome() (string, error) {
	if h := os.Getenv(EnvHome); h != "" && testing.Testing() {
		return filepath.Abs(h)
	}
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "tmxg-release"), nil
		}
		return "", errors.New("LOCALAPPDATA is not set")
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "tmxg-release"), nil
	default:
		if d := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
			return filepath.Join(d, "tmxg-release"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state", "tmxg-release"), nil
	}
}

// KeyFile is the private key file of key id in home.
func KeyFile(home, id string) string { return filepath.Join(home, "secrets", id+".key") }

// LogFile is the release log: one JSON line per signed release, append only.
func LogFile(home string) string { return filepath.Join(home, "signed.log") }
