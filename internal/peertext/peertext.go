// Package peertext prepares text that someone other than tmx and the owner wrote (another
// person or their agent in a chat, a WhatsApp contact, Companion) for an agent to read.
// tmx frames what it gives agents with its own labels at the start of a line ("[chat ...]",
// "[message from ...]", "[WhatsApp ...]"); the other side must never be able to write a line
// that looks like one, nor end a label early. So a value that goes inside a label (a name, a
// chat or topic title, a file name) passes through Field, and a body through Quote, which
// starts every line of it with Marker. DESIGN.md "Agents in chats".
package peertext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Marker starts every line of quoted text. tmx starts no line of its own with it.
const Marker = "│ "

// bar is Marker without its space: what a quoted blank line is.
const bar = "│"

// MaxField is how many characters Field keeps.
const MaxField = 100

// Field makes a short value safe inside tmx's "[...]" labels: one line (every line break and
// control character becomes a space, runs of spaces one), square brackets become parentheses,
// at most MaxField characters.
func Field(s string) string { return Inline(s, MaxField) }

// Inline is Field with its own length (n characters; n <= 0: no limit), for a longer text
// shown on one line after tmx's words (a log line, a preview).
func Inline(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '[':
			return '('
		case r == ']':
			return ')'
		case isBreak(r) || unicode.IsControl(r):
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if n > 0 && utf8.RuneCountInString(s) > n {
		s = strings.TrimSpace(string([]rune(s)[:n-1])) + "…"
	}
	return s
}

// Quote returns s with Marker in front of every line. CR LF, CR, LF, VT, FF, NEL and the
// Unicode line and paragraph separators all end a line (each is something a reader may show
// as a new line); other control characters but tab are dropped, trailing spaces too. Blank
// lines at the start and the end go; a blank s quotes to "".
func Quote(s string) string {
	lines := Lines(s)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if l == "" {
			b.WriteString(bar)
		} else {
			b.WriteString(Marker + l)
		}
	}
	return b.String()
}

// Under is label, then body quoted on the lines below it (only label when body is blank).
// With max > 0 the result is at most max bytes: a longer body is cut at a character and a
// line of tmx's says so.
func Under(label, body string, max int) string {
	q := Quote(body)
	if q == "" {
		return label
	}
	out := label + "\n" + q
	if max <= 0 || len(out) <= max {
		return out
	}
	const cut = "\n[… cut by tmx: the rest of this message is not shown]"
	keep := max - len(label) - 1 - len(cut)
	if keep <= len(Marker) {
		return label + cut
	}
	for keep > 0 && !utf8.RuneStart(q[keep]) {
		keep--
	}
	return label + "\n" + strings.TrimRight(q[:keep], " \t\n") + cut
}

// Lines splits s into lines as Quote reads them: every kind of line break ends a line,
// control characters but tab are dropped, trailing spaces and tabs go, and so do blank lines
// at the start and the end.
func Lines(s string) []string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\r\n", "\n")
	var lines []string
	var cur strings.Builder
	flush := func() {
		lines = append(lines, strings.TrimRight(cur.String(), " \t"))
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case isBreak(r):
			flush()
		case r == '\t' || !unicode.IsControl(r):
			cur.WriteRune(r)
		}
	}
	flush()
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Unquote undoes Quote on the lines that carry Marker (for showing people the words alone,
// as the Board does); other lines stay as they are.
func Unquote(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, Marker):
			lines[i] = l[len(Marker):]
		case l == bar:
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// isBreak: a character a terminal, an editor or a model may take as the end of a line.
func isBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return true
	}
	return false
}
