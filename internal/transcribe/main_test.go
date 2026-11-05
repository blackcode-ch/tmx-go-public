package transcribe

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake whisper-cli: run with TMXG_FAKE_WHISPER set, it behaves
// like the real one as far as tmx can tell (arguments, exit codes, OUT.json in the pinned
// tag's format). Modes: ok, nojson (exit 0 without output, like an unknown argument), fail,
// slow, ctrl (raw control characters in the text), badversion. TMXG_FAKE_WHISPER_LOG gets a
// line per run: "start|end <pid> <args>".
func TestMain(m *testing.M) {
	if mode := os.Getenv("TMXG_FAKE_WHISPER"); mode != "" {
		os.Exit(fakeWhisper(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeWhisper(mode string, args []string) int {
	flags := map[string]string{}
	var bools []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--version":
			if mode == "badversion" {
				fmt.Println("whisper.cpp version: 0.0.1")
			} else {
				fmt.Println("whisper.cpp version: " + strings.TrimPrefix(EngineTag, "v"))
			}
			return 0
		case "-oj", "-np", "--vad":
			bools = append(bools, a)
		default:
			if i+1 < len(args) {
				flags[a] = args[i+1]
				i++
			}
		}
	}
	logf := func(what string) {
		if p := os.Getenv("TMXG_FAKE_WHISPER_LOG"); p != "" {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			fmt.Fprintf(f, "%s %d %s\n", what, os.Getpid(), strings.Join(args, " "))
			f.Close()
		}
	}
	logf("start")
	defer logf("end")
	if _, err := os.Stat(flags["-m"]); err != nil {
		fmt.Fprintf(os.Stderr, "error: model file not found '%s'\n", flags["-m"])
		return 3
	}
	in := flags["-f"]
	if _, err := os.Stat(in); err != nil {
		fmt.Fprintf(os.Stderr, "error: input file not found '%s'\n", in)
		return 2
	}
	switch mode {
	case "nojson":
		fmt.Fprintln(os.Stderr, "error: unknown argument: --whatever")
		return 0
	case "fail":
		fmt.Fprintln(os.Stderr, "whisper_init_from_file_with_params_no_state: loading model")
		fmt.Fprintln(os.Stderr, "error: failed to initialize whisper context")
		return 3
	case "slow":
		time.Sleep(20 * time.Second)
	}
	time.Sleep(50 * time.Millisecond) // long enough for a second job to overlap, if one could
	lang := flags["-l"]
	if lang == "auto" {
		lang = "en"
	}
	durMS := int64(3000)
	if w, err := readWAV(in); err == nil {
		durMS = w.Duration().Milliseconds()
	}
	text := " fake words of " + filepath.Base(in)
	if mode == "ctrl" {
		text = " tab\there\nnewline \\\"quoted\\\""
	}
	half := durMS / 2
	json := "{\n\t\"systeminfo\": \"AVX = 1 | fake\",\n\t\"model\": {\n\t\t\"type\": \"base\",\n\t\t\"multilingual\": true\n\t},\n" +
		"\t\"params\": {\n\t\t\"model\": \"" + jsonEsc(flags["-m"]) + "\",\n\t\t\"language\": \"" + flags["-l"] + "\",\n\t\t\"translate\": false\n\t},\n" +
		"\t\"result\": {\n\t\t\"language\": \"" + lang + "\"\n\t},\n\t\"transcription\": [\n" +
		"\t\t{\n\t\t\t\"timestamps\": {\n\t\t\t\t\"from\": \"00:00:00,000\",\n\t\t\t\t\"to\": \"x\"\n\t\t\t},\n\t\t\t\"offsets\": {\n\t\t\t\t\"from\": 0,\n\t\t\t\t\"to\": " + fmt.Sprint(half) + "\n\t\t\t},\n\t\t\t\"text\": \"" + text + "\"\n\t\t},\n" +
		"\t\t{\n\t\t\t\"timestamps\": {\n\t\t\t\t\"from\": \"x\",\n\t\t\t\t\"to\": \"x\"\n\t\t\t},\n\t\t\t\"offsets\": {\n\t\t\t\t\"from\": " + fmt.Sprint(half) + ",\n\t\t\t\t\"to\": " + fmt.Sprint(durMS) + "\n\t\t\t},\n\t\t\t\"text\": \" [BLANK_AUDIO]\"\n\t\t}\n" +
		"\t]\n}\n"
	if err := os.WriteFile(flags["-of"]+".json", []byte(json), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 11
	}
	fmt.Println("[00:00:00.000 --> 00:00:01.500]  fake words") // stdout, like the real one
	return 0
}

func jsonEsc(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) }

// installFake puts the test binary where the service looks for the engine.
func installFake(t *testing.T, dst string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && os.Symlink(exe, dst) == nil {
		return
	}
	copyFile(t, exe, dst)
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()
}

// writeWAV writes secs seconds of 16 kHz mono PCM16 (a quiet tone with a silent gap every
// second, so cuts have quiet places to go).
func writeWAV(t *testing.T, path string, secs float64) {
	t.Helper()
	n := int64(secs * 16000)
	b := wavHeader(1, 16000, 16, n*2)
	data := make([]byte, n*2)
	for i := int64(0); i < n; i++ {
		v := int16(0)
		if i%16000 < 12000 {
			v = int16(3000 * ((i/8)%2*2 - 1))
		}
		binary.LittleEndian.PutUint16(data[i*2:], uint16(v))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, data...), 0o600); err != nil {
		t.Fatal(err)
	}
}
