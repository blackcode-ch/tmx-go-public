package summarize

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// What Claude is given: the recording's transcript, line by line with the time from the
// recording's start (segments follow each other: segment 2's 0:00 is the end of segment 1),
// and up to MaxFrames still frames picked evenly from recordings/<id>/frames/f-<ms>.jpg.

// Limits (variables for tests).
var (
	// MaxChars of transcript in one run; a longer one is summarized part by part, then combined.
	MaxChars = 60_000
	// MaxParts of a long transcript; what comes after is left out (and the note says so).
	MaxParts = 10
	// MaxFrames Claude looks at.
	MaxFrames = 20
)

// maxFrameBytes: a frame larger than this is not shown (the contract is 600 KB).
const maxFrameBytes = 2 << 20

// FramesDir is where a recording's still frames are: <dir>/frames/f-<ms>.jpg.
const FramesDir = "frames"

var frameRx = regexp.MustCompile(`^f-(\d{1,12})\.jpe?g$`)

type line struct {
	at   int64 // ms from the recording's start
	text string
}

type frame struct {
	ms   int64
	path string // relative to the recording's folder, with forward slashes (frames/f-000030000.jpg)
}

// input is what a summary is made from.
type input struct {
	lines       []line
	language    string // the transcripts' language (the most common)
	transcripts int    // segments transcribed
	wavs        int    // segments with audio
	notes       []string
	missing     string // which segments have no transcript ("" = none)
	frames      []frame
}

func (in input) empty() bool { return len(in.lines) == 0 && len(in.frames) == 0 }

// nothingSaid is why an empty input has nothing to summarize.
func (in input) nothingSaid(r model.Recording) string {
	switch {
	case in.wavs == 0:
		return "There is nothing to summarize: the recording has no sound to transcribe and no still frames."
	case in.transcripts == 0:
		return "There is nothing to summarize yet: the recording isn't transcribed and has no still frames. Transcribe it, then summarize it again."
	}
	return "There is nothing to summarize: no speech was heard, and the recording has no still frames."
}

// collect reads the transcripts of r's segments and lists its frames.
func (s *Service) collect(r model.Recording) input {
	var in input
	langs := map[string]int{}
	var start int64
	var failed, missing []string
	for _, g := range r.Segments {
		base := start
		start += g.DurationMS
		if g.WAV == "" || g.WAVBytes <= 44 {
			continue
		}
		in.wavs++
		t, err := s.o.Store.TranscriptBySource(recorder.Source(r.ID, g.N))
		switch {
		case errors.Is(err, store.ErrNotFound):
			missing = append(missing, strconv.Itoa(g.N))
			continue
		case err != nil:
			failed = append(failed, strconv.Itoa(g.N))
			continue
		}
		switch t.State {
		case model.TranscriptDone:
		case model.TranscriptFailed:
			failed = append(failed, strconv.Itoa(g.N))
			continue
		default:
			missing = append(missing, strconv.Itoa(g.N))
			continue
		}
		in.transcripts++
		if t.Language != "" {
			langs[t.Language]++
		}
		for _, seg := range t.Segments {
			if txt := strings.Join(strings.Fields(seg.Text), " "); txt != "" {
				in.lines = append(in.lines, line{at: base + seg.FromMS, text: txt})
			}
		}
	}
	best := 0
	for l, n := range langs {
		if n > best || (n == best && l < in.language) {
			in.language, best = l, n
		}
	}
	if len(failed) > 0 {
		in.notes = append(in.notes, fmt.Sprintf("The transcription of %s failed.", segmentList(failed)))
	}
	if len(missing) > 0 {
		in.missing = fmt.Sprintf("%s %s not transcribed.", capFirst(segmentList(missing)), isAre(len(missing)))
	}
	in.frames = pickFrames(listFrames(r.Dir), MaxFrames)
	return in
}

func segmentList(ns []string) string {
	if len(ns) == 1 {
		return "segment " + ns[0]
	}
	return "segments " + strings.Join(ns[:len(ns)-1], ", ") + " and " + ns[len(ns)-1]
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// listFrames returns the recording's frames in time order (regular files only, no links).
func listFrames(dir string) []frame {
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(filepath.Join(dir, FramesDir))
	if err != nil {
		return nil
	}
	var out []frame
	for _, e := range ents {
		m := frameRx.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 || info.Size() > maxFrameBytes {
			continue
		}
		ms, _ := strconv.ParseInt(m[1], 10, 64)
		out = append(out, frame{ms: ms, path: FramesDir + "/" + e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ms < out[j].ms })
	return out
}

// pickFrames keeps at most n frames spread evenly over the recording: the first and the last
// always, the rest at equal steps between them.
func pickFrames(all []frame, n int) []frame {
	if len(all) <= n {
		return all
	}
	if n <= 0 {
		return nil
	}
	if n == 1 {
		return all[:1]
	}
	out := make([]frame, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, all[i*(len(all)-1)/(n-1)])
	}
	return out
}

// stamp formats milliseconds as m:ss, or h:mm:ss from an hour.
func stamp(ms int64) string {
	d := time.Duration(max(0, ms)) * time.Millisecond
	h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

// parseStamp reads m:ss, mm:ss or h:mm:ss (fractions of a second ignored) as milliseconds.
func parseStamp(s string) (int64, bool) {
	s = strings.Trim(strings.TrimSpace(s), "[]()")
	if s == "" {
		return 0, false
	}
	if i := strings.IndexByte(s, '.'); i >= 0 && strings.Contains(s, ":") {
		s = s[:i]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var total int64
	for _, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	return total * 1000, true
}

// transcriptText is the lines as Claude reads them: "[m:ss] text".
func transcriptText(lines []line) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("[" + stamp(l.at) + "] " + l.text + "\n")
	}
	return b.String()
}

// chunk of a long transcript.
type chunk struct {
	lines    []line
	from, to int64 // ms: the first line's time, and where the next part starts (or the end)
}

// split cuts the lines into parts of at most maxChars each (a line longer than that is a part of
// its own), and keeps at most maxParts; dropped says from when the rest was left out (-1: none).
func split(lines []line, end int64, maxChars, maxParts int) (parts []chunk, dropped int64) {
	dropped = -1
	var cur []line
	size := 0
	flush := func(next int64) {
		if len(cur) > 0 {
			parts = append(parts, chunk{lines: cur, from: cur[0].at, to: next})
		}
		cur, size = nil, 0
	}
	for _, l := range lines {
		n := len(l.text) + 10
		if size > 0 && size+n > maxChars {
			flush(l.at)
		}
		cur = append(cur, l)
		size += n
	}
	flush(end)
	if len(parts) > maxParts {
		dropped = parts[maxParts].from
		parts = parts[:maxParts]
		parts[len(parts)-1].to = dropped
	}
	return parts, dropped
}

// place gives every chapter its segment and offset there (what the player seeks to), keeps them
// inside the recording and in time order, and drops repeats.
func place(chs []model.SummaryChapter, r model.Recording) []model.SummaryChapter {
	var total int64
	for _, g := range r.Segments {
		total += g.DurationMS
	}
	if total == 0 {
		total = r.DurationMS
	}
	sort.SliceStable(chs, func(i, j int) bool { return chs[i].TMS < chs[j].TMS })
	out := make([]model.SummaryChapter, 0, len(chs))
	for _, c := range chs {
		if c.TMS < 0 {
			c.TMS = 0
		}
		if total > 0 && c.TMS >= total {
			if c.TMS > total+5_000 {
				continue // past the end: a time Claude made up
			}
			c.TMS = max(0, total-1)
		}
		if len(out) > 0 && (out[len(out)-1].TMS == c.TMS || out[len(out)-1].Title == c.Title) {
			continue
		}
		c.Segment, c.OffsetMS = 1, c.TMS
		var start int64
		for _, g := range r.Segments {
			if c.TMS < start+g.DurationMS || g.N == r.Segments[len(r.Segments)-1].N {
				c.Segment, c.OffsetMS = g.N, max(0, c.TMS-start)
				break
			}
			start += g.DurationMS
		}
		out = append(out, c)
	}
	return out
}
