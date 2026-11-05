package term

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

// A harness TUI's conversation in the timeline (DESIGN.md "Switching live"). Its screen says
// when a turn ends (working → idle), which becomes a result entry: the end of a turn in the
// log and on the head's Board. For Claude, its transcript adds what was said: tmx follows the
// conversation's file while the TUI runs and turns what Claude writes there (replies,
// thinking, tool calls and results, and what a person typed into the screen) into entries, so
// the conversation carries on in tmx while the agent is in its own screen. Only what is
// written after the start is read: a resumed conversation's history is in the timeline already.

const (
	maxTranscriptRead = 4 << 20 // per reading; the rest comes with the next one
	maxTyped          = 20
)

// transcript follows one Claude conversation file. Used under process.mu only.
type transcript struct {
	path  string
	off   int64
	part  []byte   // the start of a line not written completely yet
	typed []string // what tmx typed (mail, the first prompt): the fleet has it in the timeline
}

// newTranscript follows the conversation spec resumes or starts (--session-id); nil when its
// id is not known.
func newTranscript(spec runner.Spec) *transcript {
	id := spec.SessionID
	if id == "" {
		id = argAfter(spec.ExtraArgs, "--session-id")
	}
	root := claude.ConfigRoot(spec.Env) // the agent's own CLAUDE_CONFIG_DIR
	if id == "" || root == "" || strings.ContainsAny(id, `/\`) {
		return nil
	}
	t := &transcript{path: filepath.Join(harness.ClaudeProjectDirIn(root, spec.Cwd), id+".jsonl")}
	if st, err := os.Stat(t.path); err == nil {
		t.off = st.Size()
	}
	return t
}

// note remembers a text tmx typed, so the transcript's copy of it is not shown again as typed
// by a person.
func (t *transcript) note(text string) {
	t.typed = append(t.typed, normText(text))
	if len(t.typed) > maxTyped {
		t.typed = t.typed[len(t.typed)-maxTyped:]
	}
}

// wasTyped reports (and forgets) whether tmx typed prompt. Claude may shorten a long paste in
// its record ("[Pasted text #1 +12 lines]"): such a prompt is not attributed to a person.
func (t *transcript) wasTyped(prompt string) bool {
	p := normText(prompt)
	for i, s := range t.typed {
		if s == p || strings.HasPrefix(s, p) || strings.HasPrefix(p, s) {
			t.typed = append(t.typed[:i], t.typed[i+1:]...)
			return true
		}
	}
	return strings.HasPrefix(p, "[Pasted text #")
}

func normText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
}

// read returns the entries for what was added to the file since the last read.
func (t *transcript) read() []model.TimelineEntry {
	st, err := os.Stat(t.path)
	if err != nil {
		return nil
	}
	if st.Size() < t.off { // rewritten from scratch
		t.off, t.part = 0, nil
	}
	if st.Size() == t.off {
		return nil
	}
	f, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return nil
	}
	buf := make([]byte, min(st.Size()-t.off, maxTranscriptRead))
	n, _ := io.ReadFull(f, buf)
	t.off += int64(n)
	data := append(t.part, buf[:n]...)
	t.part = nil
	var out []model.TimelineEntry
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			t.part = append([]byte(nil), data...)
			break
		}
		line := data[:i]
		data = data[i+1:]
		entries, prompt := claude.TranscriptLine(line)
		out = append(out, entries...)
		if prompt != "" && !t.wasTyped(prompt) {
			out = append(out, model.TimelineEntry{Kind: model.EntryUser, Text: prompt,
				Meta: map[string]any{"from": model.Owner, "source": "screen"}})
		}
	}
	return out
}

// followLocked emits what the conversation file gained (a Claude TUI; nothing otherwise).
func (w *watcher) followLocked() {
	if w.tr == nil {
		return
	}
	for _, e := range w.tr.read() {
		w.turnInput = true
		w.p.emitLocked(runner.Event{Kind: runner.EvTimeline, Entry: &e})
	}
}

// turnEndedLocked runs when the screen goes from working (or a menu) back to its input box:
// what the transcript gained first, then the turn's result, if it was a turn: something went
// in (a resumed TUI looks busy for a moment while it starts).
func (w *watcher) turnEndedLocked(now time.Time) {
	w.followLocked()
	if !w.turnInput {
		return
	}
	meta := map[string]any{"subtype": "success", "source": "screen"}
	if !w.p.turnAt.IsZero() {
		meta["duration_ms"] = now.Sub(w.p.turnAt).Milliseconds()
	}
	w.p.emitLocked(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryResult, Meta: meta}})
}
