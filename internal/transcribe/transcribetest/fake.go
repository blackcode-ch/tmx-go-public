// Package transcribetest installs a fake voice engine and model into a throwaway tmx home, for
// tests of the API, the CLI and MCP, and for trying the UI on a throwaway daemon. The engine is a
// shell script that answers like whisper-cli (--version, and -oj -of OUT writing OUT.json with
// one segment of the given text); the models are sparse files of the catalog's sizes. Unix only.
package transcribetest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

const script = `#!/bin/sh
# A fake whisper-cli (internal/transcribe/transcribetest).
out=""
while [ $# -gt 0 ]; do
	case "$1" in
	--version) echo "whisper.cpp version: VERSION"; exit 0 ;;
	-of) out="$2"; shift ;;
	esac
	shift
done
[ -n "$out" ] || { echo "error: no output file" >&2; exit 1; }
sleep "${TMXG_FAKE_WHISPER_DELAY:-0}"
printf '%s\n' '{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":1500},"text":" TEXT"}]}' > "$out.json"
`

// Supported reports whether the fake runs here (it is a shell script).
func Supported() bool { return runtime.GOOS != "windows" }

// Install puts the fake engine, the default model and the VAD model into home's voice folder; every
// transcription then says text. It skips the test on Windows.
func Install(t testing.TB, home, text string) {
	t.Helper()
	if !Supported() {
		t.Skip("the fake voice engine is a shell script")
	}
	if err := InstallFiles(home, text); err != nil {
		t.Fatal(err)
	}
}

// InstallFiles is Install outside a test (a throwaway daemon for trying the UI).
func InstallFiles(home, text string) error {
	voice := config.Paths{Home: home}.VoiceDir()
	eng := filepath.Join(voice, "engine", transcribe.EngineTag, "whisper-cli")
	if err := os.MkdirAll(filepath.Dir(eng), 0o700); err != nil {
		return err
	}
	body := strings.NewReplacer("VERSION", strings.TrimPrefix(transcribe.EngineTag, "v"), "TEXT", strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(text)).Replace(script)
	if err := os.WriteFile(eng, []byte(body), 0o755); err != nil {
		return err
	}
	m, _ := transcribe.FindModel(transcribe.DefaultModel)
	if err := sparse(filepath.Join(voice, "models", m.File), m.Size); err != nil {
		return err
	}
	return sparse(filepath.Join(voice, "models", transcribe.VADModel.File), transcribe.VADModel.Size)
}

// sparse makes a file of size bytes that takes (almost) no disk space.
func sparse(path string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
