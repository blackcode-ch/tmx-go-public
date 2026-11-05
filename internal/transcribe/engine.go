package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// The engine runs once per file: whisper-cli -m MODEL -f FILE -l LANG -t N -oj -of OUT -np
// [--vad -vm VAD], at low priority, and writes OUT.json. A crash or out-of-memory in it cannot
// take the daemon down, and its memory is freed when it exits.
//
// whisper-cli (examples/cli/cli.cpp at the pinned tag) exits 0 on some errors (an unknown
// argument or language), so the result is the JSON file: no file, no transcript. Its JSON is
// {"result": {"language": "de"}, "transcription": [{"offsets": {"from": ms, "to": ms},
// "text": " ..."}], ...}; it escapes only quotes and backslashes, so sanitizeJSON escapes the
// control characters a text could carry before decoding.

type runSpec struct {
	Engine, Model, VAD string // paths; VAD "" = without voice activity detection
	Input              string
	Language           string // "auto" or a code
	Threads            int
	Timeout            time.Duration
	LowPriority        bool
	TmpDir             string // where OUT.json goes
}

type engineResult struct {
	Language string
	Segments []model.TranscriptSegment
}

// timeoutMargin is added to every job's time limit (model loading; a variable for tests).
var timeoutMargin = 2 * time.Minute

// stderrKeep: how much of the engine's error output an error message carries.
const stderrKeep = 2048

func engineArgs(s runSpec, out string) []string {
	args := []string{"-m", s.Model, "-f", s.Input, "-l", s.Language, "-t", strconv.Itoa(s.Threads), "-oj", "-of", out, "-np"}
	if s.VAD != "" {
		args = append(args, "--vad", "-vm", s.VAD)
	}
	return args
}

// runEngine transcribes one file.
func runEngine(ctx context.Context, s runSpec) (engineResult, error) {
	if _, err := os.Stat(s.Engine); err != nil {
		return engineResult{}, fmt.Errorf("the voice engine does not start: %w", err)
	}
	dir, err := os.MkdirTemp(s.TmpDir, "run-")
	if err != nil {
		return engineResult{}, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "out")
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	argv0, args := s.Engine, engineArgs(s, out)
	if s.LowPriority {
		argv0, args = sysinfo.LowPriorityCommand(argv0, args) // macOS: low disk I/O policy
	}
	cmd := hiddenexec.CommandContext(ctx, argv0, args...)
	cmd.Dir = dir
	stderr := &tail{max: stderrKeep}
	cmd.Stdout, cmd.Stderr = nil, stderr // stdout repeats the text with timestamps
	cmd.WaitDelay = 2 * time.Second
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return engineResult{}, fmt.Errorf("the voice engine does not start: %w", err)
	}
	if s.LowPriority {
		_ = sysinfo.LowerPriority(cmd.Process.Pid)
	}
	werr := cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		return engineResult{}, fmt.Errorf("the voice engine took longer than %s and was stopped", s.Timeout.Round(time.Second))
	}
	if err := ctx.Err(); err != nil {
		return engineResult{}, err
	}
	if werr != nil {
		return engineResult{}, fmt.Errorf("the voice engine failed (%v)%s", werr, stderr.explain())
	}
	b, err := os.ReadFile(out + ".json")
	if err != nil {
		return engineResult{}, fmt.Errorf("the voice engine wrote no transcript%s", stderr.explain())
	}
	return parseOutput(b)
}

// whisperJSON is the part of whisper-cli's -oj output tmx reads.
type whisperJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription *[]struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	} `json:"transcription"`
}

// blankRx: what whisper writes for a stretch without speech.
var blankRx = regexp.MustCompile(`(?i)^[\[(]\s*(blank_audio|silence|no speech)\s*[\])]$`)

func parseOutput(b []byte) (engineResult, error) {
	var j whisperJSON
	if err := json.Unmarshal(sanitizeJSON(b), &j); err != nil {
		return engineResult{}, fmt.Errorf("the voice engine's output is not readable: %w", err)
	}
	if j.Transcription == nil {
		return engineResult{}, errors.New("the voice engine's output has no transcription")
	}
	r := engineResult{Language: j.Result.Language, Segments: []model.TranscriptSegment{}}
	for _, s := range *j.Transcription {
		t := strings.Join(strings.Fields(s.Text), " ")
		if t == "" || blankRx.MatchString(t) {
			continue
		}
		r.Segments = append(r.Segments, model.TranscriptSegment{FromMS: s.Offsets.From, ToMS: s.Offsets.To, Text: t})
	}
	return r, nil
}

// joinText is the transcript's text: its segments in order.
func joinText(segs []model.TranscriptSegment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, " ")
}

// sanitizeJSON escapes raw control characters inside JSON strings (whisper-cli writes them as
// they are), so the decoder accepts them.
func sanitizeJSON(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b) + 16)
	in, esc := false, false
	for _, c := range b {
		switch {
		case !in:
			if c == '"' {
				in = true
			}
			out.WriteByte(c)
		case esc:
			esc = false
			out.WriteByte(c)
		case c == '\\':
			esc = true
			out.WriteByte(c)
		case c == '"':
			in = false
			out.WriteByte(c)
		case c < 0x20:
			fmt.Fprintf(&out, `\u%04x`, c)
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
}

// probeEngine runs "<engine> --version" and checks that it is the pinned whisper.cpp.
func probeEngine(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := hiddenexec.CommandContext(ctx, path, "--version")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	want := "whisper.cpp version: " + strings.TrimPrefix(EngineTag, "v")
	if err != nil {
		return fmt.Errorf("it does not run on this machine (%v): %s", err, strings.TrimSpace(string(clip(out, 300))))
	}
	if !strings.Contains(string(out), want) {
		return fmt.Errorf("it says %q, not %q", strings.TrimSpace(string(clip(out, 120))), want)
	}
	return nil
}

func clip(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

// tail keeps the last max bytes written to it.
type tail struct {
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

// explain is ": <the last error line>" of the engine's output ("" = nothing useful).
func (t *tail) explain() string {
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.Contains(strings.ToLower(l), "error") || strings.Contains(l, "failed") {
			return ": " + l
		}
	}
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		return ": " + strings.TrimSpace(lines[len(lines)-1])
	}
	return ""
}

// timeoutFor bounds a job on audio of about d: CPU-only engines run several times faster than
// real time, so twice the length plus a start-up margin is generous.
func timeoutFor(d time.Duration) time.Duration {
	t := 2*d + timeoutMargin
	if t > 6*time.Hour {
		t = 6 * time.Hour
	}
	return t
}
