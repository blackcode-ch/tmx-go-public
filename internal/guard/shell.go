package guard

import (
	"strings"
)

// A small, forgiving shell lexer: it splits a command line into simple commands (at ; && ||
// | & newlines and parentheses), each a list of words and redirections, without running
// anything. Words keep their pieces (literal text, $variables, $(substitutions)) so they can be
// expanded later with what the analyzer knows. It never fails: unbalanced quotes simply run to
// the end of the input.

type partKind uint8

const (
	pLit     partKind = iota
	pVar              // $NAME, ${NAME}, %NAME%, $env:NAME
	pSub              // $(...) or `...`: text is the inner command line
	pProcSub          // <(...) or >(...)
)

type part struct {
	kind   partKind
	text   string
	quoted bool
}

type word []part

// literal is the word's text when it has no variables or substitutions.
func (w word) literal() (string, bool) {
	var b strings.Builder
	for _, p := range w {
		if p.kind != pLit {
			return "", false
		}
		b.WriteString(p.text)
	}
	return b.String(), true
}

// raw is a readable form of the word (variables as $NAME).
func (w word) raw() string {
	var b strings.Builder
	for _, p := range w {
		switch p.kind {
		case pLit:
			b.WriteString(p.text)
		case pVar:
			b.WriteString("$" + p.text)
		case pSub:
			b.WriteString("$(" + p.text + ")")
		case pProcSub:
			b.WriteString("<(" + p.text + ")")
		}
	}
	return b.String()
}

type redir struct {
	op     string // > >> < << <<< &> etc.
	target word   // nil for fd duplications (2>&1) and heredocs
}

func (r redir) output() bool { return strings.Contains(r.op, ">") }

type simpleCmd struct {
	words   []word
	redirs  []redir
	op      string // the operator before it: "" | ";" | "&&" | "||" | "|" | "&" | "\n" | "(" | ")" | "{" | "}"
	raw     string
	heredoc string // body of a << heredoc
}

type pendingDoc struct {
	delim string
	strip bool
	cmd   int // index in lexer.cmds; -1 = the command being built
}

type lexer struct {
	src  string
	sh   Shell
	i    int
	cmds []simpleCmd

	cur      simpleCmd
	curStart int

	w      word
	inWord bool

	nextOp  string
	pending *redir // a redirection waiting for its target word
	docs    []pendingDoc
	docNext bool // the next word is a heredoc delimiter
	docStr  bool // <<- (leading tabs stripped)
}

func lex(src string, sh Shell) []simpleCmd {
	l := &lexer{src: src, sh: sh}
	for l.i < len(l.src) {
		if !l.inWord && len(l.cur.words) == 0 && len(l.cur.redirs) == 0 && l.pending == nil {
			l.curStart = l.i
		}
		l.step()
	}
	l.endCmd("")
	return l.cmds
}

func (l *lexer) peek(off int) byte {
	if l.i+off < len(l.src) {
		return l.src[l.i+off]
	}
	return 0
}

func (l *lexer) lit(s string, quoted bool) {
	l.inWord = true
	if n := len(l.w); n > 0 && l.w[n-1].kind == pLit && l.w[n-1].quoted == quoted {
		l.w[n-1].text += s
		return
	}
	l.w = append(l.w, part{kind: pLit, text: s, quoted: quoted})
}

func (l *lexer) add(p part) {
	l.inWord = true
	l.w = append(l.w, p)
}

// soFar is the literal text of the current word (to tell C:\path from \escape).
func (l *lexer) soFar() string {
	var b strings.Builder
	for _, p := range l.w {
		b.WriteString(p.text)
	}
	return b.String()
}

func (l *lexer) endWord() {
	if !l.inWord {
		return
	}
	w := l.w
	l.w, l.inWord = nil, false
	if l.docNext {
		l.docNext = false
		d, ok := w.literal()
		if !ok {
			d = w.raw()
		}
		l.docs = append(l.docs, pendingDoc{delim: d, strip: l.docStr, cmd: -1})
		return
	}
	if l.pending != nil {
		r := *l.pending
		r.target = w
		l.pending = nil
		l.cur.redirs = append(l.cur.redirs, r)
		return
	}
	l.cur.words = append(l.cur.words, w)
}

// endCmd closes the command being built; l.i is where the operator op starts.
func (l *lexer) endCmd(op string) {
	l.endWord()
	if l.pending != nil { // "echo >" with no target
		l.cur.redirs = append(l.cur.redirs, *l.pending)
		l.pending = nil
	}
	if len(l.cur.words) > 0 || len(l.cur.redirs) > 0 {
		end := min(l.i, len(l.src))
		l.cur.op = l.nextOp
		l.cur.raw = strings.TrimSpace(l.src[min(l.curStart, end):end])
		l.cmds = append(l.cmds, l.cur)
		for k := range l.docs {
			if l.docs[k].cmd == -1 {
				l.docs[k].cmd = len(l.cmds) - 1
			}
		}
	}
	l.cur = simpleCmd{}
	l.nextOp = op
}

func (l *lexer) step() {
	c := l.src[l.i]
	switch {
	case c == ' ' || c == '\t' || c == '\r':
		l.endWord()
		l.i++
	case c == '\n':
		l.endCmd("\n")
		l.i++
		l.readHeredocs()
	case c == ';':
		l.endCmd(";")
		l.i++
	case c == '&':
		switch {
		case l.peek(1) == '&':
			l.endCmd("&&")
			l.i += 2
		case l.peek(1) == '>' && l.sh != ShellCmd:
			l.endWord()
			l.i++
			l.redirect("&")
		case l.sh == ShellPowerShell && !l.inWord:
			l.i++ // the call operator: & "C:\x.exe" args
		default:
			l.endCmd("&")
			l.i++
		}
	case c == '|':
		l.endCmd(map[bool]string{true: "||", false: "|"}[l.peek(1) == '|'])
		if n := l.peek(1); n == '|' || n == '&' {
			l.i++
		}
		l.i++
	case c == '(' || c == ')':
		if c == '(' && l.sh == ShellPOSIX && l.inWord {
			l.lit("(", false) // a=(1 2) arrays, f() definitions
			l.i++
			return
		}
		l.endCmd(string(c))
		l.i++
	case (c == '{' || c == '}') && l.sh == ShellPowerShell:
		l.endCmd(string(c)) // script blocks: their statements are commands too
		l.i++
	case c == '>' || c == '<':
		if l.sh == ShellPOSIX && l.peek(1) == '(' {
			end := matchClose(l.src, l.i+1, '(', ')')
			l.add(part{kind: pProcSub, text: l.src[l.i+2 : end]})
			l.i = min(end+1, len(l.src))
			return
		}
		fd := ""
		if l.inWord {
			if s, ok := l.w.literal(); ok && isDigits(s) {
				fd = s
				l.w, l.inWord = nil, false
			}
		}
		l.endWord()
		l.redirect(fd)
	case c == '*' && l.sh == ShellPowerShell && l.peek(1) == '>' && !l.inWord:
		l.i++
		l.redirect("*")
	case c == '#' && !l.inWord && l.sh != ShellCmd:
		if l.sh == ShellPowerShell && l.i > 0 && l.src[l.i-1] == '<' {
			l.lit("#", false)
			l.i++
			return
		}
		for l.i < len(l.src) && l.src[l.i] != '\n' {
			l.i++
		}
	case c == '\'' && l.sh != ShellCmd:
		l.singleQuoted()
	case c == '"':
		l.doubleQuoted()
	case c == '\\' && l.sh == ShellPOSIX:
		l.backslash()
	case c == '`' && l.sh == ShellPowerShell: // PowerShell's escape character
		if n := l.peek(1); n != '\n' && n != 0 {
			l.lit(string(n), true)
		}
		l.i += 2
	case c == '`' && l.sh == ShellPOSIX:
		end := strings.IndexByte(l.src[l.i+1:], '`')
		if end < 0 {
			end = len(l.src) - l.i - 1
		}
		l.add(part{kind: pSub, text: l.src[l.i+1 : l.i+1+end]})
		l.i += end + 2
	case c == '^' && l.sh == ShellCmd:
		if n := l.peek(1); n != 0 {
			l.lit(string(n), true)
		}
		l.i += 2
	case c == '$' && l.sh != ShellCmd:
		l.dollar(false)
	case c == '%':
		l.percent(false)
	default:
		l.lit(string(c), false)
		l.i++
	}
}

func (l *lexer) redirect(fd string) {
	s := l.src[l.i:]
	op := ""
	for _, o := range []string{">>", ">|", ">&", "<<<", "<<-", "<<", "<&", "<>", ">", "<"} {
		if strings.HasPrefix(s, o) {
			op = o
			break
		}
	}
	l.i += len(op)
	full := fd + op
	switch op {
	case ">&", "<&":
		// 2>&1, >&2, <&3, >&-: duplications. ">&file" is a csh-style redirect to a file.
		j := l.i
		for j < len(l.src) && (l.src[j] >= '0' && l.src[j] <= '9' || l.src[j] == '-') {
			j++
		}
		if j > l.i {
			l.i = j
			l.cur.redirs = append(l.cur.redirs, redir{op: full})
			return
		}
		l.pending = &redir{op: full}
	case "<<", "<<-":
		l.docNext, l.docStr = true, op == "<<-"
		l.cur.redirs = append(l.cur.redirs, redir{op: full})
	default:
		l.pending = &redir{op: full}
	}
}

// readHeredocs consumes the bodies of the heredocs started on the line just ended.
func (l *lexer) readHeredocs() {
	docs := l.docs
	l.docs = nil
	for _, d := range docs {
		var body strings.Builder
		for l.i < len(l.src) {
			line, next := l.src[l.i:], len(l.src)
			if end := strings.IndexByte(l.src[l.i:], '\n'); end >= 0 {
				line, next = l.src[l.i:l.i+end], l.i+end+1
			}
			l.i = next
			check := line
			if d.strip {
				check = strings.TrimLeft(line, "\t")
			}
			if strings.TrimRight(check, "\r") == d.delim {
				break
			}
			body.WriteString(line + "\n")
		}
		if d.cmd >= 0 && d.cmd < len(l.cmds) {
			l.cmds[d.cmd].heredoc += body.String()
		}
	}
}

func (l *lexer) singleQuoted() {
	j := l.i + 1
	var b strings.Builder
	for j < len(l.src) {
		if l.src[j] == '\'' {
			if l.sh == ShellPowerShell && j+1 < len(l.src) && l.src[j+1] == '\'' {
				b.WriteByte('\'')
				j += 2
				continue
			}
			break
		}
		b.WriteByte(l.src[j])
		j++
	}
	l.lit(b.String(), true)
	l.i = j + 1
}

func (l *lexer) doubleQuoted() {
	l.lit("", true)
	l.i++
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch {
		case c == '"':
			if l.sh == ShellPowerShell && l.peek(1) == '"' {
				l.lit(`"`, true)
				l.i += 2
				continue
			}
			l.i++
			return
		case c == '\\' && l.sh == ShellPOSIX:
			switch n := l.peek(1); n {
			case '$', '`', '"', '\\':
				l.lit(string(n), true)
				l.i += 2
			case '\n':
				l.i += 2
			default:
				l.lit(`\`, true)
				l.i++
			}
		case c == '`' && l.sh == ShellPowerShell:
			if n := l.peek(1); n != 0 {
				l.lit(string(n), true)
			}
			l.i += 2
		case c == '`' && l.sh == ShellPOSIX:
			end := strings.IndexByte(l.src[l.i+1:], '`')
			if end < 0 {
				end = len(l.src) - l.i - 1
			}
			l.add(part{kind: pSub, text: l.src[l.i+1 : l.i+1+end], quoted: true})
			l.i += end + 2
		case c == '$' && l.sh != ShellCmd:
			l.dollar(true)
		case c == '%':
			l.percent(true)
		default:
			l.lit(string(c), true)
			l.i++
		}
	}
}

// backslash: outside quotes a backslash escapes the next character (\rm is rm), except in
// words that are plainly Windows paths (C:\Windows, .\x, \\server), where it is a separator.
func (l *lexer) backslash() {
	n := l.peek(1)
	switch n {
	case '\n':
		l.i += 2
		return
	case 0:
		l.lit(`\`, false)
		l.i++
		return
	}
	so := l.soFar()
	winPath := strings.Contains(so, ":") || strings.Contains(so, `\`) || so == "." || so == ".." ||
		(so == "" && n == '\\')
	if winPath && strings.IndexByte(" \t'\";&|()<>$`", n) < 0 {
		l.lit(`\`, false)
		l.i++
		return
	}
	l.lit(string(n), true)
	l.i += 2
}

func (l *lexer) dollar(quoted bool) {
	s := l.src[l.i:]
	switch {
	case strings.HasPrefix(s, "$(("):
		end := matchClose(l.src, l.i+1, '(', ')')
		l.lit("0", true)
		l.i = min(end+1, len(l.src))
	case strings.HasPrefix(s, "$("):
		end := matchClose(l.src, l.i+1, '(', ')')
		l.add(part{kind: pSub, text: l.src[l.i+2 : end], quoted: quoted})
		l.i = min(end+1, len(l.src))
	case strings.HasPrefix(s, "${"):
		end := strings.IndexByte(s, '}')
		if end < 0 {
			end = len(s) - 1
		}
		inner := s[2:max(end, 2)]
		name := inner
		if k := strings.IndexAny(inner, ":-=+?#%/[^,@"); k > 0 {
			if strings.EqualFold(inner[:k], "env") && k+1 < len(inner) {
				name = inner[k+1:]
			} else {
				name = inner[:k]
			}
		}
		l.add(part{kind: pVar, text: name, quoted: quoted})
		l.i += end + 1
	case l.sh == ShellPowerShell && len(s) > 5 && strings.EqualFold(s[1:5], "env:"):
		j := 5
		for j < len(s) && (isNameChar(s[j]) || s[j] == '(' || s[j] == ')') {
			j++
		}
		l.add(part{kind: pVar, text: s[5:j], quoted: quoted})
		l.i += j
	case len(s) > 1 && isNameStart(s[1]):
		j := 1
		for j < len(s) && isNameChar(s[j]) {
			j++
		}
		l.add(part{kind: pVar, text: s[1:j], quoted: quoted})
		l.i += j
	case len(s) > 1 && strings.IndexByte("0123456789@*#?$!-", s[1]) >= 0:
		l.add(part{kind: pVar, text: s[1:2], quoted: quoted})
		l.i += 2
	default:
		l.lit("$", quoted)
		l.i++
	}
}

func (l *lexer) percent(quoted bool) {
	s := l.src[l.i:]
	if len(s) > 2 && isNameStart(s[1]) {
		j := 1
		for j < len(s) && (isNameChar(s[j]) || s[j] == '(' || s[j] == ')') {
			j++
		}
		if j < len(s) && s[j] == '%' {
			l.add(part{kind: pVar, text: s[1:j], quoted: quoted})
			l.i += j + 1
			return
		}
	}
	l.lit("%", quoted)
	l.i++
}

// matchClose returns the index of the bracket closing the one at src[open], skipping quoted
// text and nested brackets; len(src) when it is never closed.
func matchClose(src string, open int, o, c byte) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '\'':
			k := strings.IndexByte(src[i+1:], '\'')
			if k < 0 {
				return len(src)
			}
			i += k + 1
		case '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case o:
			depth++
		case c:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(src)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isNameStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isNameChar(c byte) bool  { return isNameStart(c) || c >= '0' && c <= '9' }
