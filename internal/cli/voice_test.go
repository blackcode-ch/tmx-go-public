package cli

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/transcribe/transcribetest"
)

func silence(t *testing.T, path string, secs int) {
	t.Helper()
	n := secs * 32000
	b := make([]byte, 44+n)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceCommands(t *testing.T) {
	d := cliDaemon(t)
	// Nothing set up yet.
	out, _, code := tmx(t, "voice")
	if code != 0 || !strings.Contains(out, "voice: not ready") || !strings.Contains(out, "base-q5_1") {
		t.Fatalf("status: %d %s", code, out)
	}
	file := filepath.Join(t.TempDir(), "memo.wav")
	silence(t, file, 1)
	if _, errOut, code := tmx(t, "transcribe", file); code != 1 || !strings.Contains(errOut, "isn't set up") {
		t.Fatalf("not set up: %d %s", code, errOut)
	}

	transcribetest.Install(t, d.Home, "remember to buy milk")
	out, _, code = tmx(t, "voice", "setup") // everything is there: nothing to download
	if code != 0 || !strings.Contains(out, "voice: ready (base-q5_1, language auto)") || !strings.Contains(out, "* base-q5_1") {
		t.Fatalf("setup: %d %s", code, out)
	}
	out, _, code = tmx(t, "transcribe", file)
	if code != 0 || strings.TrimSpace(out) != "remember to buy milk" {
		t.Fatalf("transcribe: %d %q", code, out)
	}
	out, _, code = tmx(t, "transcribe", "--timestamps", file, "--language", "de")
	if code != 0 || strings.TrimSpace(out) != "[0:00] remember to buy milk" {
		t.Fatalf("timestamps: %d %q", code, out)
	}
	if out, _, code := tmx(t, "transcribe", file, "--json"); code != 0 || !strings.Contains(out, `"segments"`) || !strings.Contains(out, `"language": "en"`) {
		t.Fatalf("json: %d %s", code, out)
	}
	bad := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(bad, []byte("not audio"), 0o600)
	if _, errOut, code := tmx(t, "transcribe", bad); code != 1 || !strings.Contains(errOut, "not a WAV, MP3, FLAC or Ogg") {
		t.Fatalf("not audio: %d %s", code, errOut)
	}
	if _, _, code := tmx(t, "transcribe"); code != 2 {
		t.Fatalf("no file: %d", code)
	}

	out, _, code = tmx(t, "voice", "model", "small-q5_1")
	if code != 0 || !strings.Contains(out, "not ready: The Small model (small-q5_1) is not downloaded yet") || !strings.Contains(out, "* small-q5_1") {
		t.Fatalf("model: %d %s", code, out)
	}
	if _, errOut, code := tmx(t, "voice", "model", "huge"); code != 1 || !strings.Contains(errOut, `no model "huge"`) {
		t.Fatalf("unknown model: %d %s", code, errOut)
	}
	tmx(t, "voice", "model", "base-q5_1")
	if out, _, code := tmx(t, "voice", "language", "fr"); code != 0 || !strings.Contains(out, "language fr") {
		t.Fatalf("language: %d %s", code, out)
	}
	if out, _, code := tmx(t, "voice", "auto", "on"); code != 0 || !strings.Contains(out, "auto-transcribe recordings: on") || !d.Voice.AutoTranscribe() {
		t.Fatalf("auto: %d %s", code, out)
	}
	if _, _, code := tmx(t, "voice", "auto", "maybe"); code != 2 {
		t.Fatalf("auto maybe: %d", code)
	}
	out, _, code = tmx(t, "voice", "rm", "base-q5_1")
	if code != 0 || !strings.Contains(out, "not ready") {
		t.Fatalf("rm: %d %s", code, out)
	}
	if _, _, code := tmx(t, "voice", "frobnicate"); code != 2 {
		t.Fatalf("unknown subcommand: %d", code)
	}
}
