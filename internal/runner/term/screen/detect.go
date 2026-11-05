package screen

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// State is what a screen shows (v2 detect.py's states).
type State string

const (
	Starting   State = "starting"    // nothing recognisable yet
	Working    State = "working"     // the harness shows its busy indicator
	Idle       State = "idle"        // at its input prompt
	Approval   State = "approval"    // asks permission to run a tool or edit a file
	Question   State = "question"    // asks the user to pick an option or answer
	Trust      State = "trust"       // the "do you trust this folder" gate
	Locked     State = "locked"      // the conversation is held by another process
	Dialog     State = "dialog"      // another menu with a highlighted option
	UsageLimit State = "usage_limit" // out of quota until a reset time (Prompt)
)

// Menu reports whether the state is a dialog waiting for an answer.
func (s State) Menu() bool {
	return s == Approval || s == Question || s == Trust || s == Locked || s == Dialog
}

// Choice is one numbered (or cursor-listed) option of a menu.
type Choice struct {
	N     int    `json:"n"`
	Label string `json:"label"`
}

// Detection is one reading of the screen.
type Detection struct {
	NumericEnter bool // this harness requires digit + Enter, never arrow navigation
	State        State
	Prompt       string   // the question or dialog text above a menu; usage_limit: the reset time
	Choices      []Choice // options, numbered from 1
	Selected     int      // the highlighted option's N; 0 = none
	// Horizontal: the options sit on one row and are selected with left/right
	// (opencode's permission prompt); otherwise they are a vertical list.
	Horizontal bool
}

// Labels returns the options' labels.
func (d Detection) Labels() []string {
	out := make([]string, len(d.Choices))
	for i, c := range d.Choices {
		out[i] = c.Label
	}
	return out
}

// Signature identifies a menu: the same dialog redrawn has the same signature.
func (d Detection) Signature() string {
	return string(d.State) + "\x00" + d.Prompt + "\x00" + strings.Join(d.Labels(), "\x00")
}

// Patterns are one harness's screen regexes (compiled case-insensitive, multi-line). nil
// fields never match.
type Patterns struct {
	NumericEnter                      bool
	FooterAfterRule                   bool // busy text above the last horizontal rule is history
	Trust, Approval, Question, Locked *regexp.Regexp
	Working, Idle                     *regexp.Regexp
	// AtPrompt: a footer shown only while the input box is live. Near the bottom it overrides
	// anything above that merely looks like a menu (conversation history).
	AtPrompt   *regexp.Regexp
	UsageLimit *regexp.Regexp
	// MenuCursor: the characters that highlight a menu option; "" = any known cursor.
	MenuCursor string
	// RowMenu: a footer that marks a menu drawn as one row of options selected with
	// left/right arrows (opencode's permission prompt).
	RowMenu *regexp.Regexp
}

// Rx compiles a pattern with v2's flags (re.I | re.M); "" gives nil.
func Rx(expr string) *regexp.Regexp {
	if expr == "" {
		return nil
	}
	return regexp.MustCompile("(?im)" + expr)
}

func match(rx *regexp.Regexp, s string) bool { return rx != nil && rx.MatchString(s) }

var (
	menuLine   = regexp.MustCompile(`^\s*(?:[│|]\s*)?(?P<cursor>[›❯>➜▶●]\s*)?(?P<num>\d{1,2})[.)]\s+(?P<label>\S.*?)\s*(?:[│|]\s*)?$`)
	cursorLine = regexp.MustCompile(`^(?P<indent>\s*)(?P<cursor>[›❯>➜▶])\s+(?P<label>\S.*?)\s*$`)
	boxMark    = regexp.MustCompile(`^\s*│?\s*[›❯>](\s|$)`)
	ruleLine   = regexp.MustCompile(`^[─━═\-_╭╮╰╯ ]+$`)
	dashedRule = regexp.MustCompile(`^[╌╍┄┅┈┉ ]+$`)
	tryAgainAt = regexp.MustCompile(`(?i)try again at (.+?)(?:\.(?:\s|$)|$)`)
	spaces     = regexp.MustCompile(`\s+`)
)

const (
	menuScan     = 30 // lines from the bottom searched for a menu
	maxOptionLen = 90
	maxOptions   = 12
)

// ParseMenu finds numbered options near the bottom of the screen and returns them, the
// highlighted one (0 = none) and the index of the first option's line (-1 = no menu).
// cursors: the characters this harness highlights options with ("" = any): Claude marks
// the user's own messages with ">" and its replies with "●", which must not count.
func ParseMenu(lines []string, cursors string) ([]Choice, int, int) {
	var choices []Choice
	selected, first := 0, -1
	stop := max(-1, len(lines)-menuScan)
	width := 0 // the screen's width, as far as the lines show it (rules span it)
	for _, l := range lines {
		width = max(width, utf8.RuneCountInString(l))
	}
	var wrapped []string // indented lines just below the line being read: a long label's rest
	for i := len(lines) - 1; i > stop; i-- {
		m := menuLine.FindStringSubmatch(lines[i])
		if m != nil {
			n, _ := strconv.Atoi(m[2])
			if len(choices) > 0 && n != choices[0].N-1 && n >= choices[0].N {
				break
			}
			label := strings.TrimSpace(m[3])
			if len(wrapped) > 0 && utf8.RuneCountInString(lines[i]) >= width-1 {
				label = joinWrapped(label, wrapped) // a label cut at the screen's edge
			}
			wrapped = nil
			choices = append([]Choice{{N: n, Label: label}}, choices...)
			if cur := strings.TrimSpace(m[1]); cur != "" && (cursors == "" || strings.Contains(cursors, cur)) {
				selected = n
			}
			first = i
		} else if t := strings.TrimSpace(lines[i]); t == "" {
			wrapped = nil
		} else if strings.HasPrefix(lines[i], "   ") {
			wrapped = append([]string{t}, wrapped...)
		} else if len(choices) > 0 {
			if ruleLine.MatchString(t) {
				continue // a rule between options (Claude's question tool)
			}
			break
		}
	}
	if len(choices) > 0 && choices[0].N != 1 {
		choices, selected, first = nil, 0, -1
	}
	if len(choices) == 0 {
		return parseCursorList(lines, cursors)
	}
	return choices, selected, first
}

// joinWrapped puts a wrapped label back together: no space after a cut that ends in "-"
// or "/" (a path or a word broken at the edge).
func joinWrapped(label string, rest []string) string {
	for _, r := range rest {
		if strings.HasSuffix(label, "-") || strings.HasSuffix(label, "/") {
			label += r
		} else {
			label += " " + r
		}
	}
	return label
}

// parseCursorList finds unnumbered menus (e.g. Claude's trust gate): one line carries the
// cursor, its siblings share its label column.
func parseCursorList(lines []string, cursors string) ([]Choice, int, int) {
	stop := max(-1, len(lines)-menuScan)
	for i := len(lines) - 1; i > stop; i-- {
		m := cursorLine.FindStringSubmatchIndex(lines[i])
		if m == nil {
			continue
		}
		cur := lines[i][m[4]:m[5]]
		if cursors != "" && !strings.Contains(cursors, cur) {
			continue
		}
		label := lines[i][m[6]:m[7]]
		col := utf8.RuneCountInString(lines[i][:m[6]])
		sibling := func(j int) bool {
			r := []rune(lines[j])
			return len(r) > col && strings.TrimSpace(string(r[:col])) == "" && r[col] != ' '
		}
		start, end := i, i
		for start-1 >= 0 && sibling(start-1) {
			start--
		}
		for end+1 < len(lines) && sibling(end+1) {
			end++
		}
		if start == end {
			return nil, 0, -1
		}
		var choices []Choice
		for k, j := 0, start; j <= end; k, j = k+1, j+1 {
			l := strings.TrimSpace(lines[j])
			if j == i {
				l = label
			}
			choices = append(choices, Choice{N: k + 1, Label: l})
		}
		// Options are short. A long line is prose: Claude shows the user's own sent messages
		// with the same ❯, wrapped lines indented under it, which is not a menu.
		if len(choices) > maxOptions {
			return nil, 0, -1
		}
		for _, c := range choices {
			if utf8.RuneCountInString(c.Label) > maxOptionLen {
				return nil, 0, -1
			}
		}
		return choices, i - start + 1, start
	}
	return nil, 0, -1
}

// QuestionText is the dialog body above a menu: up to the nearest rule or box edge.
func QuestionText(lines []string, firstChoice int) string {
	const maxLines = 12
	var out []string
	blanks := 0
	for i := firstChoice - 1; i >= 0 && len(out) < maxLines; i-- {
		raw := strings.TrimSpace(lines[i])
		if raw != "" && ruleLine.MatchString(raw) {
			break
		}
		if raw != "" && dashedRule.MatchString(raw) {
			continue // a dashed rule inside the dialog (Claude frames the command with it)
		}
		text := strings.Trim(lines[i], " │|")
		if text != "" {
			out = append([]string{text}, out...)
			blanks = 0
		} else {
			blanks++
			if blanks >= 2 && len(out) > 0 {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

func lastParagraph(lines []string) string {
	var out []string
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			out = append([]string{t}, out...)
		} else if len(out) > 0 {
			break
		}
	}
	if len(out) > 8 {
		out = out[len(out)-8:]
	}
	return strings.Join(out, "\n")
}

func usageLimitPrompt(near string) string {
	flat := strings.TrimSpace(spaces.ReplaceAllString(near, " "))
	if m := tryAgainAt.FindStringSubmatch(flat); m != nil {
		return strings.TrimSpace(m[1])
	}
	if len(flat) > 200 {
		return flat[len(flat)-200:]
	}
	return flat
}

// unknownQuiet: an unrecognised screen with output this recent counts as working.
const unknownQuiet = 3 * time.Second

// Classify reads a screen. quiet is how long the screen has been without new output.
func Classify(lines []string, p *Patterns, quiet time.Duration) (result Detection) {
	if p == nil {
		p = &Patterns{}
	}
	defer func() { result.NumericEnter = p.NumericEnter }()
	tail := lines
	if len(tail) > 40 {
		tail = tail[len(tail)-40:]
	}
	screen := strings.Join(tail, "\n")
	var filled []string
	for _, l := range tail {
		if strings.TrimSpace(l) != "" {
			filled = append(filled, l)
		}
	}
	last := func(n int) []string { return filled[max(0, len(filled)-n):] }

	// Out of quota overrides everything, even a live-looking input box below the banner.
	// Only near the bottom: an old banner scrolled away means the agent has recovered.
	if p.UsageLimit != nil {
		near := strings.Join(last(15), "\n")
		if p.UsageLimit.MatchString(near) {
			return Detection{State: UsageLimit, Prompt: usageLimitPrompt(near)}
		}
	}
	// The live input box's footer at the very bottom settles it: idle, or working when the
	// status line (just above the box) says so. Menus further up are history.
	bottom := strings.Join(last(5), "\n")
	if match(p.AtPrompt, bottom) && (!p.FooterAfterRule || match(p.AtPrompt, strings.Join(last(1), "\n"))) {
		if p.FooterAfterRule {
			start := 0
			for i, line := range filled {
				if ruleLine.MatchString(strings.TrimSpace(line)) {
					start = i + 1
				}
			}
			if match(p.Working, strings.Join(filled[start:], "\n")) {
				return Detection{State: Working}
			}
			return Detection{State: Idle}
		}
		top := len(filled)
		for i, l := range filled {
			if boxMark.MatchString(l) {
				top = i
			}
		}
		near := strings.Join(append(append([]string{}, filled[max(0, top-3):top]...), bottom), "\n")
		if match(p.Working, near) {
			return Detection{State: Working}
		}
		return Detection{State: Idle}
	}
	choices, selected, first := ParseMenu(tail, p.MenuCursor)
	for _, c := range []struct {
		st State
		rx *regexp.Regexp
	}{{Locked, p.Locked}, {Trust, p.Trust}, {Approval, p.Approval}, {Question, p.Question}} {
		if match(c.rx, screen) {
			prompt := lastParagraph(tail)
			if first >= 0 {
				prompt = QuestionText(tail, first)
			}
			return Detection{State: c.st, Prompt: prompt, Choices: choices, Selected: selected}
		}
	}
	if match(p.Working, screen) {
		return Detection{State: Working}
	}
	// A menu without a specific pattern is still a question, but only if it is interactive
	// (an option is highlighted): a numbered list in a reply has no cursor.
	if len(choices) > 0 && selected != 0 {
		return Detection{State: Dialog, Prompt: QuestionText(tail, first), Choices: choices, Selected: selected}
	}
	if match(p.Idle, screen) {
		return Detection{State: Idle}
	}
	if len(filled) == 0 {
		return Detection{State: Starting}
	}
	if p.FooterAfterRule {
		return Detection{State: Working}
	}
	if quiet < unknownQuiet {
		return Detection{State: Working}
	}
	return Detection{State: Idle}
}
