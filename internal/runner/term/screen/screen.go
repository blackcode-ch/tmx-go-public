// Package screen keeps a virtual terminal (github.com/charmbracelet/x/vt) fed by an agent's
// pty output, and reads states, menus and questions off it (Classify), a port of v2's
// detect.py. See DESIGN.md "Screen detection".
package screen

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Screen is a terminal emulator that renders an agent's output. Safe for concurrent use.
type Screen struct {
	mu        sync.Mutex
	em        *vt.Emulator
	w, h      int
	bracketed bool // the program enabled bracketed paste
}

// New returns a cols x rows screen. reply receives what the emulator answers to the
// program's queries (cursor position, device attributes): write it to the pty, so a TUI
// that asks gets an answer even when no viewer is attached. reply may be nil.
func New(cols, rows int, reply func([]byte)) *Screen {
	s := &Screen{em: vt.NewEmulator(cols, rows), w: cols, h: rows}
	// Called from inside Write, which holds s.mu.
	s.em.SetCallbacks(vt.Callbacks{
		EnableMode:  func(m ansi.Mode) { s.setMode(m, true) },
		DisableMode: func(m ansi.Mode) { s.setMode(m, false) },
	})
	go func() { // the emulator blocks on writing replies until they are read
		buf := make([]byte, 4096)
		for {
			n, err := s.em.Read(buf)
			if n > 0 && reply != nil {
				reply(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *Screen) setMode(m ansi.Mode, on bool) {
	if m == ansi.ModeBracketedPaste {
		s.bracketed = on
	}
}

// Write feeds program output.
func (s *Screen) Write(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.em.Write(b)
}

// Resize changes the screen size.
func (s *Screen) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.em.Resize(cols, rows)
	s.w, s.h = cols, rows
}

// Close stops the reply reader. (Emulator.Close would race with its own Read on a flag;
// closing the reply pipe's writer ends the reader the same way.)
func (s *Screen) Close() {
	if pw, ok := s.em.InputPipe().(*io.PipeWriter); ok {
		_ = pw.CloseWithError(io.EOF)
	}
}

// Lines returns the visible rows, top to bottom, without trailing blanks.
func (s *Screen) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.linesLocked()
}

// linesLocked is Lines without locking.
func (s *Screen) linesLocked() []string {
	out := make([]string, s.h)
	var b strings.Builder
	for y := 0; y < s.h; y++ {
		b.Reset()
		for x := 0; x < s.w; x++ {
			c := s.em.CellAt(x, y)
			switch {
			case c == nil:
				b.WriteByte(' ')
			case c.Width == 0 && c.Content == "":
				// the second half of a wide character
			case c.Content == "":
				b.WriteByte(' ')
			default:
				b.WriteString(c.Content)
			}
		}
		out[y] = strings.TrimRight(b.String(), " \t ")
	}
	return out
}

// Classify reads this screen, like Classify, and fills in a menu drawn as one row of
// options selected with left/right arrows (opencode's permission prompt).
func (s *Screen) Classify(p *Patterns, quiet time.Duration) Detection {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := s.linesLocked()
	d := Classify(lines, p, quiet)
	if p != nil && p.RowMenu != nil && d.State == Approval && len(d.Choices) == 0 {
		if choices, selected, first, ok := s.rowChoicesLocked(p.RowMenu); ok {
			d.Choices, d.Selected, d.Horizontal = choices, selected, true
			// Keep the prompt stable: the spinner/timing lines above the box change every
			// redraw, so build it from the approval header down to the option row only.
			start := first
			for i := first - 1; i >= 0; i-- {
				if p.Approval != nil && p.Approval.MatchString(lines[i]) {
					start = i
					break
				}
			}
			var out []string
			for i := start; i <= first; i++ {
				if text := strings.Trim(lines[i], " │|"); text != "" {
					out = append(out, text)
				}
			}
			d.Prompt = strings.Join(out, "\n")
		}
	}
	return d
}

// rowChoicesLocked finds a single row of options above the RowMenu marker and parses them
// by their visual gaps; the selected one is the option whose cell style differs from the
// others. Caller holds s.mu.
func (s *Screen) rowChoicesLocked(marker *regexp.Regexp) ([]Choice, int, int, bool) {
	h := s.h
	for y := h - 1; y >= max(0, h-8); y-- {
		line := s.linesLocked()[y]
		if !match(marker, line) {
			continue
		}
		spans := s.rowSpansLocked(y)
		if len(spans) < 2 || len(spans) > maxOptions {
			continue
		}
		counts := map[string]int{}
		for _, sp := range spans {
			counts[sp.key]++
		}
		selected := 1
		if len(counts) > 1 {
			for i, sp := range spans {
				if counts[sp.key] == 1 {
					selected = i + 1
					break
				}
			}
		}
		choices := make([]Choice, len(spans))
		for i, sp := range spans {
			choices[i] = Choice{N: i + 1, Label: sp.text}
		}
		return choices, selected, y, true
	}
	return nil, 0, -1, false
}

// rowSpan is one option label found on a single row of a menu.
type rowSpan struct {
	text string
	key  string
}

// rowSpansLocked groups the words of a line into option labels, stopping before the
// large gap that separates the options from the trailing hints.
func (s *Screen) rowSpansLocked(y int) []rowSpan {
	type word struct {
		text  string
		start int
		end   int
		key   string
	}
	var words []word
	start := -1
	var b strings.Builder
	var key string
	for x := 0; x <= s.w; x++ {
		c := s.em.CellAt(x, y)
		content := " "
		var cellKey string
		if c != nil {
			if c.Width == 0 && c.Content == "" {
				continue // second half of a wide character
			}
			if c.Content != "" {
				content = c.Content
			}
			r, g, b_, a := 0, 0, 0, 0
			if c.Style.Bg != nil {
				rr, gg, bb, aa := c.Style.Bg.RGBA()
				r, g, b_, a = int(rr), int(gg), int(bb), int(aa)
			}
			r2, g2, b3, a2 := 0, 0, 0, 0
			if c.Style.Fg != nil {
				rr, gg, bb, aa := c.Style.Fg.RGBA()
				r2, g2, b3, a2 = int(rr), int(gg), int(bb), int(aa)
			}
			cellKey = fmt.Sprintf("%d:%d:%d:%d|%d:%d:%d:%d|%d", r, g, b_, a, r2, g2, b3, a2, c.Style.Attrs)
		}
		if content != " " && content != "" {
			if start == -1 {
				start = x
				key = cellKey
			}
			b.WriteString(content)
		} else {
			if start != -1 {
				words = append(words, word{text: b.String(), start: start, end: x, key: key})
				b.Reset()
				start = -1
			}
		}
	}
	if start != -1 {
		words = append(words, word{text: b.String(), start: start, end: s.w, key: key})
	}
	// Skip leading box borders.
	i := 0
	for i < len(words) && isBorderWord(words[i].text) {
		i++
	}
	// Group words into option labels. A gap of 2..4 cells starts a new option; a larger gap
	// separates the options from the trailing hints and stops the scan.
	var spans []rowSpan
	var cur strings.Builder
	curKey := ""
	curEnd := -1
	for ; i < len(words); i++ {
		gap := words[i].start - curEnd
		if curEnd == -1 {
		} else if gap >= 8 {
			break
		} else if gap >= 3 {
			spans = append(spans, rowSpan{text: strings.TrimSpace(cur.String()), key: curKey})
			cur.Reset()
		} else if cur.Len() > 0 {
			cur.WriteString(" ")
		}
		if cur.Len() == 0 {
			curKey = words[i].key
		}
		cur.WriteString(words[i].text)
		curEnd = words[i].end
	}
	if cur.Len() > 0 {
		spans = append(spans, rowSpan{text: strings.TrimSpace(cur.String()), key: curKey})
	}
	// Drop empty labels.
	var out []rowSpan
	for _, sp := range spans {
		if sp.text != "" {
			out = append(out, sp)
		}
	}
	return out
}

func isBorderWord(s string) bool {
	for _, r := range s {
		switch r {
		case '┃', '│', '|', '╭', '╮', '╰', '╯', '─', '━':
		default:
			return false
		}
	}
	return true
}

// Text is Lines joined by newlines (for tests and debugging).
func (s *Screen) Text() string { return strings.Join(s.Lines(), "\n") }

// Paste returns text as the program should receive a paste: wrapped in bracketed-paste
// markers when it enabled that mode (DECSET 2004).
func (s *Screen) Paste(text string) []byte {
	s.mu.Lock()
	bracketed := s.bracketed
	s.mu.Unlock()
	if !bracketed {
		return []byte(text)
	}
	return []byte(ansi.BracketedPasteStart + text + ansi.BracketedPasteEnd)
}
