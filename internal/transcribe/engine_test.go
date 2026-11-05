package transcribe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// v195Output is whisper-cli -oj output in exactly the shape examples/cli/cli.cpp writes at
// v1.9.5 (output_json): tabs, the systeminfo/model/params blocks, offsets in milliseconds.
const v195Output = `{
	"systeminfo": "WHISPER : COREML = 0 | OPENVINO = 0 | Metal : EMBED_LIBRARY = 1 | CPU : NEON = 1 | ARM_FMA = 1 | ACCELERATE = 1 | REPACK = 1 |",
	"model": {
		"type": "base",
		"multilingual": true,
		"vocab": 51865,
		"audio": {
			"ctx": 1500,
			"state": 512,
			"head": 8,
			"layer": 6
		},
		"text": {
			"ctx": 448,
			"state": 512,
			"head": 8,
			"layer": 6
		},
		"mels": 80,
		"ftype": 9
	},
	"params": {
		"model": "/x/ggml-base-q5_1.bin",
		"language": "auto",
		"translate": false
	},
	"result": {
		"language": "en"
	},
	"transcription": [
		{
			"timestamps": {
				"from": "00:00:00,000",
				"to": "00:00:07,600"
			},
			"offsets": {
				"from": 0,
				"to": 7600
			},
			"text": " And so my fellow Americans, ask not what your country can do for you,"
		},
		{
			"timestamps": {
				"from": "00:00:07,600",
				"to": "00:00:10,600"
			},
			"offsets": {
				"from": 7600,
				"to": 10600
			},
			"text": " ask what you can do for your country."
		},
		{
			"timestamps": {
				"from": "00:00:10,600",
				"to": "00:00:11,000"
			},
			"offsets": {
				"from": 10600,
				"to": 11000
			},
			"text": " [BLANK_AUDIO]"
		}
	]
}
`

func TestParseOutputV195(t *testing.T) {
	r, err := parseOutput([]byte(v195Output))
	if err != nil {
		t.Fatal(err)
	}
	if r.Language != "en" || len(r.Segments) != 2 {
		t.Fatalf("%+v", r)
	}
	if s := r.Segments[1]; s.FromMS != 7600 || s.ToMS != 10600 || s.Text != "ask what you can do for your country." {
		t.Fatalf("segment: %+v", s)
	}
	if got := joinText(r.Segments); !strings.HasPrefix(got, "And so my fellow Americans,") || !strings.HasSuffix(got, "your country.") {
		t.Fatalf("text: %q", got)
	}
}

func TestParseOutputOddCases(t *testing.T) {
	// whisper-cli escapes only quotes and backslashes: raw control characters reach the file.
	raw := "{\"result\":{\"language\":\"de\"},\"transcription\":[{\"offsets\":{\"from\":0,\"to\":10},\"text\":\" a\tb\nc \\\"q\\\" \\\\ d\"}]}"
	r, err := parseOutput([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if r.Segments[0].Text != `a b c "q" \ d` {
		t.Fatalf("text %q", r.Segments[0].Text)
	}
	if _, err := parseOutput([]byte(`{"result":{"language":"en"}}`)); err == nil {
		t.Fatal("output without transcription accepted")
	}
	if _, err := parseOutput([]byte(`not json`)); err == nil {
		t.Fatal("garbage accepted")
	}
	r, err = parseOutput([]byte(`{"result":{"language":"en"},"transcription":[]}`))
	if err != nil || len(r.Segments) != 0 || r.Segments == nil {
		t.Fatalf("silence: %+v %v", r, err)
	}
}

func fakeSpec(t *testing.T, mode string) runSpec {
	t.Helper()
	dir := t.TempDir()
	eng := filepath.Join(dir, "whisper-cli"+exeExt(runtime.GOOS))
	installFake(t, eng)
	mdl := filepath.Join(dir, "model.bin")
	os.WriteFile(mdl, []byte("m"), 0o600)
	in := filepath.Join(dir, "in.wav")
	writeWAV(t, in, 2)
	t.Setenv("TMXG_FAKE_WHISPER", mode)
	return runSpec{Engine: eng, Model: mdl, Input: in, Language: "auto", Threads: 2, Timeout: 20 * time.Second, LowPriority: true, TmpDir: dir}
}

func TestRunEngine(t *testing.T) {
	s := fakeSpec(t, "ok")
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("TMXG_FAKE_WHISPER_LOG", log)
	r, err := runEngine(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Language != "en" || len(r.Segments) != 1 || r.Segments[0].Text != "fake words of in.wav" || r.Segments[0].ToMS != 1000 {
		t.Fatalf("%+v", r)
	}
	b, _ := os.ReadFile(log)
	for _, want := range []string{"-m " + s.Model, "-f " + s.Input, "-l auto", "-t 2", "-oj", "-np", "-of "} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("engine args lack %q: %s", want, b)
		}
	}
	if bytes.Contains(b, []byte("--vad")) {
		t.Error("--vad without a VAD model")
	}
	if left, _ := filepath.Glob(filepath.Join(s.TmpDir, "run-*")); len(left) != 0 {
		t.Errorf("run folders left: %v", left)
	}

	s.VAD = filepath.Join(s.TmpDir, "vad.bin")
	os.WriteFile(s.VAD, []byte("v"), 0o600)
	if _, err := runEngine(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !bytes.Contains(b, []byte("--vad -vm "+s.VAD)) {
		t.Errorf("no --vad: %s", b)
	}
}

func TestRunEngineFailures(t *testing.T) {
	cases := map[string]string{
		"fail":   "failed to initialize whisper context",
		"nojson": "wrote no transcript: error: unknown argument",
	}
	for mode, want := range cases {
		s := fakeSpec(t, mode)
		_, err := runEngine(context.Background(), s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", mode, err)
		}
	}
	s := fakeSpec(t, "slow")
	s.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := runEngine(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "took longer than") {
		t.Fatalf("slow: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the timeout did not stop the engine")
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	s.Timeout = time.Minute
	if _, err := runEngine(ctx, s); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	s = fakeSpec(t, "ok")
	s.Engine = filepath.Join(t.TempDir(), "missing")
	if _, err := runEngine(context.Background(), s); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Fatalf("missing engine: %v", err)
	}
}

func TestProbeEngine(t *testing.T) {
	s := fakeSpec(t, "ok")
	if err := probeEngine(context.Background(), s.Engine); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMXG_FAKE_WHISPER", "badversion")
	if err := probeEngine(context.Background(), s.Engine); err == nil || !strings.Contains(err.Error(), "0.0.1") {
		t.Fatalf("bad version: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "notabinary")
	os.WriteFile(bad, []byte("garbage"), 0o755)
	if err := probeEngine(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Fatalf("garbage: %v", err)
	}
}

func TestReadWAV(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.wav")
	writeWAV(t, p, 1.5)
	w, err := readWAV(p)
	if err != nil {
		t.Fatal(err)
	}
	if w.Rate != 16000 || w.Channels != 1 || !w.pcm16() || w.Duration() != 1500*time.Millisecond || w.DataOffset != 44 {
		t.Fatalf("%+v %s", w, w.Duration())
	}

	// A writer that never patched the sizes, a LIST chunk before the data, and the extensible format.
	data := make([]byte, 32000) // 1 s
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(40))
	for _, v := range []any{uint16(0xFFFE), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16), uint16(22), uint16(16), uint32(4)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.Write([]byte{1, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71}) // KSDATAFORMAT_SUBTYPE_PCM
	b.WriteString("LIST")
	binary.Write(&b, binary.LittleEndian, uint32(5))
	b.WriteString("abcde\x00") // odd size: padded
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(0xFFFFFFFF))
	b.Write(data)
	q := filepath.Join(dir, "b.wav")
	os.WriteFile(q, b.Bytes(), 0o600)
	w, err = readWAV(q)
	if err != nil {
		t.Fatal(err)
	}
	if w.Format != 1 || w.DataSize != 32000 || w.Duration() != time.Second {
		t.Fatalf("extensible: %+v", w)
	}

	os.WriteFile(q, []byte("ID3\x04\x00 not a wav"), 0o600)
	if _, err := readWAV(q); !errors.Is(err, errNotWAV) {
		t.Fatalf("mp3 as wav: %v", err)
	}
}

func TestSplitWAV(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "long.wav")
	writeWAV(t, p, 9.3)
	w, _ := readWAV(p)
	// 4 s parts: a cut looks back up to 1 s, which always holds one of the tone's quiet gaps.
	parts, err := splitWAV(p, w, 4*time.Second, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 {
		t.Fatalf("%d parts", len(parts))
	}
	var total int64
	var last time.Duration = -1
	for i, pt := range parts {
		pw, err := readWAV(pt.Path)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if pt.Offset <= last {
			t.Fatalf("part %d starts at %s, after %s", i, pt.Offset, last)
		}
		if pw.DataSize == 0 {
			t.Fatalf("part %d is empty", i)
		}
		last = pt.Offset
		total += pw.DataSize
	}
	if total != w.DataSize {
		t.Fatalf("parts hold %d bytes of %d", total, w.DataSize)
	}
	// The cuts land in the silent gaps (the last quarter of every second).
	for _, pt := range parts[1:] {
		if ms := pt.Offset.Milliseconds() % 1000; ms < 750 {
			t.Errorf("a cut at %s is not in a quiet place", pt.Offset)
		}
	}
}

func TestAudioKind(t *testing.T) {
	for in, want := range map[string]string{
		"RIFF\x00\x00\x00\x00WAVEfmt ": "wav",
		"fLaC\x00\x00":                 "flac",
		"OggS\x00":                     "ogg",
		"ID3\x04":                      "mp3",
		"\xff\xfb\x90\x00":             "mp3",
		"<html>":                       "",
		"":                             "",
	} {
		if got := audioKind([]byte(in)); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestTimeoutFor(t *testing.T) {
	if got := timeoutFor(10 * time.Minute); got != 22*time.Minute {
		t.Fatal(got)
	}
	if got := timeoutFor(100 * time.Hour); got != 6*time.Hour {
		t.Fatal(got)
	}
}
