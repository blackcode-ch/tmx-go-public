package guard

import (
	"path"
	"regexp"
	"strings"
)

type finding struct {
	rule   string
	detail string
	match  string
}

// analyzer walks one tool call. It records findings; the verdict is the strongest one.
type analyzer struct {
	ctx                         Context
	fold                        bool // case-insensitive file system
	home, own, workspace, state string
	temp                        []string
	cwd                         string
	dirs                        []string // pushd stack
	vars                        map[string]string
	procVars                    map[string]bool // PowerShell variables holding processes found by name
	found                       []finding
	depth                       int
	fileTool                    bool // a Write/Edit tool call: Check judged its content already
}

func newAnalyzer(ctx Context) *analyzer {
	a := &analyzer{ctx: ctx, fold: ctx.OS == "darwin" || ctx.OS == "windows", vars: map[string]string{}, procVars: map[string]bool{}}
	a.home = a.norm(ctx.Home, "")
	a.own = a.norm(ctx.Own, "")
	a.workspace = a.norm(ctx.Workspace, "")
	a.state = a.norm(ctx.StateDir, "")
	temps := append([]string{}, ctx.Temp...)
	if ctx.OS != "windows" {
		temps = append(temps, "/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp", "/var/folders", "/private/var/folders")
	}
	for _, t := range temps {
		// a misconfigured temp folder (/, C:\, the home folder) must not whitelist everything below it
		if n := a.norm(t, ""); n != "" && n != "/" && len(n) > 3 && n != a.home {
			a.temp = append(a.temp, n)
		}
	}
	a.cwd = a.norm(ctx.Cwd, "")
	if a.cwd == "" {
		a.cwd = a.own
	}
	return a
}

func (a *analyzer) add(rule, detail, match string) {
	a.found = append(a.found, finding{rule: rule, detail: detail, match: strings.TrimSpace(match)})
	// The carve-outs ride along with their parent finding: a switched-off parent never hides them.
	if rule == RuleKillByName && killsTmxRuntime(match) {
		a.found = append(a.found, finding{rule: RuleKillTmx, detail: detail + ", which includes tmx's own processes", match: strings.TrimSpace(match)})
	}
	if rule == RuleTmxState && strings.Contains(detail, "secrets") {
		a.found = append(a.found, finding{rule: RuleTmxSecrets, detail: detail, match: strings.TrimSpace(match)})
	}
}

// verdict is the strongest finding, each one first mapped through the owner's state for its
// rule: an allowed rule never hides another rule's block in the same command.
func (a *analyzer) verdict() Verdict {
	best, bestOut := -1, Allow
	for i, f := range a.found {
		o := a.ctx.StateOf(f.rule)
		if o == Allow {
			continue
		}
		if best < 0 || o.rank() > bestOut.rank() {
			best, bestOut = i, o
			continue
		}
		// Two holds: the verdict's rule decides whether an Allow may be remembered, so a
		// non-rememberable finding wins over a rememberable one, or `cp x ../out && chmod 700
		// /etc/x` would be remembered as a mere write-outside (go-reviewer-o4, #241).
		if o == Hold && bestOut == Hold && Rememberable[a.found[best].rule] && !Rememberable[f.rule] {
			best = i
		}
	}
	if best < 0 {
		return Verdict{Outcome: Allow}
	}
	f := a.found[best]
	r := ruleByID[f.rule]
	return Verdict{Outcome: bestOut, Rule: r.ID, RuleName: r.Name, Why: r.Why, Detail: f.detail, Match: f.match}
}

// resolve turns a path written by a file tool into a target.
func (a *analyzer) resolve(p string) target {
	shown := p
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = a.ctx.Home + p[1:]
	}
	return target{p: a.norm(p, a.cwd), shown: shown}
}

// arg is one expanded word.
type arg struct {
	s            string // expanded text; pieces tmx cannot know stay as $NAME / $(...)
	raw          string
	unknown      bool // has a piece tmx cannot know
	unknownStart bool // ...and starts with it
	glob         bool // has an unquoted wildcard
	subs         []string
	procs        []string
}

func (a *analyzer) lookup(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if v, ok := a.vars[name]; ok {
		return v, true
	}
	if v, ok := a.vars[strings.ToLower(name)]; ok {
		return v, true
	}
	switch strings.ToUpper(name) {
	case "HOME", "USERPROFILE":
		if a.ctx.Home != "" {
			return a.ctx.Home, true
		}
	case "PWD":
		if a.ctx.Cwd != "" {
			return a.ctx.Cwd, true
		}
	}
	if a.ctx.Env != nil && isNameStart(name[0]) {
		if v := a.ctx.Env(name); v != "" {
			return v, true
		}
	}
	return "", false
}

func (a *analyzer) expand(w word) arg {
	var b strings.Builder
	x := arg{raw: w.raw()}
	for i, p := range w {
		switch p.kind {
		case pLit:
			t := p.text
			if i == 0 && !p.quoted && strings.HasPrefix(t, "~") {
				rest := t[1:]
				user, tail, _ := strings.Cut(rest, "/")
				switch {
				case a.ctx.Home == "":
				case rest == "" || strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, `\`):
					t = a.ctx.Home + rest
				case user != "" && !strings.ContainsAny(user, `\:*?`):
					t = path.Dir(strings.ReplaceAll(a.ctx.Home, `\`, "/")) + "/" + user
					if tail != "" {
						t += "/" + tail
					}
				}
			}
			if !p.quoted && strings.ContainsAny(t, "*?[") {
				x.glob = true
			}
			b.WriteString(t)
		case pVar:
			if v, ok := a.lookup(p.text); ok {
				b.WriteString(v)
				continue
			}
			x.unknown = true
			if b.Len() == 0 {
				x.unknownStart = true
			}
			b.WriteString("$" + p.text)
		case pSub:
			x.subs = append(x.subs, p.text)
			x.unknown = true
			if b.Len() == 0 {
				x.unknownStart = true
			}
			b.WriteString("$(" + p.text + ")")
		case pProcSub:
			x.procs = append(x.procs, p.text)
			b.WriteString("/dev/fd/63")
		}
	}
	x.s = b.String()
	return x
}

// target turns an argument into a path target.
func (a *analyzer) target(x arg) target {
	if x.unknownStart {
		return target{unknown: true, shown: x.raw}
	}
	s := x.s
	t := target{shown: x.raw}
	if x.glob {
		// A wildcard that means "everything here" stands for the folder itself; any other
		// pattern stands for something inside that folder.
		comps := strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' })
		lead := ""
		if strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) {
			lead = "/"
		}
		for i, c := range comps {
			if !strings.ContainsAny(c, "*?[") {
				continue
			}
			base := lead + strings.Join(comps[:i], "/")
			if base == "" {
				base = "."
			}
			switch c {
			case "*", ".*", "*.*", "**", "{*,.*}":
				s = base
			default:
				s = base + "/" + "x"
			}
			t.glob = true
			break
		}
	}
	t.p = a.norm(s, a.cwd)
	return t
}

func looksLikePath(s string) bool {
	return strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, "~") || strings.HasPrefix(s, ".")
}

// script analyzes a command line in shell sh.
func (a *analyzer) script(src string, sh Shell) {
	if a.depth > 8 {
		return
	}
	a.rawChecks(src, sh)
	var up []*stage
	for _, c := range lex(src, sh) {
		if c.op != "|" && c.op != "(" {
			up = nil
		}
		st := a.command(c, sh, up)
		if st != nil {
			up = append(up, st)
		}
	}
}

// nested analyzes a command line found inside another (bash -c, eval, $(...)); a nested
// shell's cd and variables do not leak out.
func (a *analyzer) nested(src string, sh Shell) {
	cwd, dirs := a.cwd, a.dirs
	vars := make(map[string]string, len(a.vars))
	for k, v := range a.vars {
		vars[k] = v
	}
	a.depth++
	a.script(src, sh)
	a.depth--
	a.cwd, a.dirs, a.vars = cwd, dirs, vars
}

// stage is what a pipeline stage tells the stages after it.
type stage struct {
	verb      string
	downloads bool     // emits downloaded or decoded text
	byName    bool     // lists processes by name (pgrep, pidof, Get-Process node)
	recurse   bool     // walks a tree (Get-ChildItem -Recurse)
	roots     []string // find's starting points (normalised)
	grep      bool
}

var shellKeywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "do": true,
	"while": true, "until": true, "!": true, "{": true, "}": true, "fi": true, "done": true, "esac": true}

func (a *analyzer) command(c simpleCmd, sh Shell, up []*stage) *stage {
	args := make([]arg, 0, len(c.words))
	for _, w := range c.words {
		x := a.expand(w)
		for _, s := range x.subs {
			a.nested(s, sh)
		}
		for _, s := range x.procs {
			a.nested(s, sh)
		}
		args = append(args, x)
	}
	for _, r := range c.redirs {
		if r.target == nil {
			continue
		}
		x := a.expand(r.target)
		for _, s := range x.subs {
			a.nested(s, sh)
		}
		t := a.target(x)
		if r.output() {
			a.write(t, c.raw)
		} else {
			a.read(t, c.raw)
		}
	}

	// PowerShell: $name = value, or $name = <command>
	psVar := ""
	if sh == ShellPowerShell && len(c.words) >= 3 && len(c.words[0]) == 1 && c.words[0][0].kind == pVar {
		if eq, ok := c.words[1].literal(); ok && eq == "=" {
			psVar = strings.ToLower(c.words[0][0].text)
			delete(a.vars, psVar)
			delete(a.procVars, psVar)
			if len(args) == 3 && !args[2].unknown {
				a.vars[psVar] = args[2].s
			}
			args = args[2:]
		}
	}
	if sh == ShellPowerShell && len(c.words) > 0 && c.words[0][0].kind == pVar && a.procVars[strings.ToLower(c.words[0][0].text)] {
		if len(c.words[0]) > 1 && psKillCall.MatchString(c.words[0].raw()) {
			a.add(RuleKillByName, "kills processes found by name ($"+c.words[0][0].text+".Kill())", c.raw)
		}
		return &stage{verb: "$" + c.words[0][0].text, byName: true}
	}
	// POSIX: NAME=value [NAME=value...] [command]
	for len(args) > 0 && sh != ShellCmd && psVar == "" {
		name, ok := assignment(c.words[len(c.words)-len(args)])
		if !ok {
			break
		}
		if len(args) == 1 { // only assignments: they set shell variables
			delete(a.vars, name)
			if x := args[0]; !x.unknown {
				_, v, _ := strings.Cut(x.s, "=")
				a.vars[name] = v
			}
		}
		args = args[1:]
	}
	for len(args) > 0 && shellKeywords[args[0].s] {
		args = args[1:]
	}
	if len(args) == 0 {
		return nil
	}
	st := a.dispatch(args, c.raw, sh, up)
	if psVar != "" && st != nil && st.byName {
		a.procVars[psVar] = true
	}
	if c.heredoc != "" && st != nil && isShell(st.verb) {
		a.nested(c.heredoc, ShellPOSIX)
	}
	return st
}

// assignment reports whether w is NAME=value with an unquoted name.
func assignment(w word) (name string, ok bool) {
	if len(w) == 0 || w[0].kind != pLit || w[0].quoted {
		return "", false
	}
	n, _, found := strings.Cut(w[0].text, "=")
	n = strings.TrimSuffix(n, "+")
	if !found || n == "" || !isNameStart(n[0]) {
		return "", false
	}
	for i := 0; i < len(n); i++ {
		if !isNameChar(n[i]) {
			return "", false
		}
	}
	return n, true
}

// verbOf is a command's name: basename, lower case, without .exe and similar.
func verbOf(s string) string {
	s = strings.TrimLeft(s, `\`)
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.ToLower(s)
	for _, ext := range []string{".exe", ".com", ".cmd", ".bat"} {
		s = strings.TrimSuffix(s, ext)
	}
	return s
}

// psAliases maps PowerShell's built-in aliases to their cmdlets (only where it matters).
var psAliases = map[string]string{
	"rm": "remove-item", "del": "remove-item", "erase": "remove-item", "rd": "remove-item", "rmdir": "remove-item",
	"ri": "remove-item", "mv": "move-item", "move": "move-item", "mi": "move-item", "cp": "copy-item",
	"copy": "copy-item", "cpi": "copy-item", "kill": "stop-process", "spps": "stop-process", "ps": "get-process",
	"gps": "get-process", "start": "start-process", "saps": "start-process", "ii": "invoke-item",
	"iex": "invoke-expression", "iwr": "invoke-webrequest", "irm": "invoke-restmethod", "curl": "invoke-webrequest",
	"wget": "invoke-webrequest", "sc": "set-content", "ac": "add-content", "ni": "new-item", "md": "new-item",
	"mkdir": "new-item", "sl": "set-location", "cd": "set-location", "chdir": "set-location",
	"pushd": "push-location", "popd": "pop-location", "echo": "write-output", "write": "write-output",
	"cat": "get-content", "gc": "get-content", "type": "get-content", "clc": "clear-content",
}

// verb is the command name of args[0], with PowerShell aliases and tmx's own names resolved.
func (a *analyzer) verb(s string, sh Shell) string {
	v := verbOf(s)
	if sh == ShellPowerShell && !strings.Contains(strings.ToLower(s), v+".") { // "sc.exe" is the program, "sc" the alias
		if cmdlet, ok := psAliases[v]; ok {
			return cmdlet
		}
	}
	for _, n := range a.ctx.TmxNames {
		if n != "" && (v == verbOf(n) || a.norm(s, a.cwd) == a.norm(n, "")) {
			return "tmx"
		}
	}
	return v
}

func (a *analyzer) dispatch(args []arg, raw string, sh Shell, up []*stage) *stage {
	for {
		rest, wrapped := a.unwrap(a.verb(args[0].s, sh), args, raw, sh)
		if !wrapped {
			break
		}
		if len(rest) == 0 {
			return &stage{verb: verbOf(args[0].s)}
		}
		args = rest
	}
	verb := a.verb(args[0].s, sh)
	st := &stage{verb: verb}
	if !textOnly[verb] {
		for _, x := range args[1:] {
			if looksLikePath(x.s) {
				a.read(a.target(x), raw)
			}
		}
	}
	h, ok := handlers[verb]
	if !ok {
		h = prefixHandler(verb)
	}
	if h != nil {
		h(a, &call{verb: verb, args: args, raw: raw, sh: sh, up: up, st: st})
	}
	return st
}

// call is one command handed to a verb handler.
type call struct {
	verb string
	args []arg
	raw  string
	sh   Shell
	up   []*stage
	st   *stage
}

// upstream reports whether an earlier stage of the pipeline has the property.
func (c *call) upstream(f func(*stage) bool) bool {
	for _, s := range c.up {
		if f(s) {
			return true
		}
	}
	return false
}

var textOnly = map[string]bool{"echo": true, "printf": true, "print": true, "write-output": true,
	"write-host": true, "git": true, "tmx": true, "say": true}

// unwrap strips a wrapper (sudo, env, nohup, timeout, ...) and returns the wrapped command.
func (a *analyzer) unwrap(verb string, args []arg, raw string, sh Shell) ([]arg, bool) {
	rest := args[1:]
	skip := func(withValue map[string]bool) []arg {
		for len(rest) > 0 {
			s := rest[0].s
			if s == "--" {
				return rest[1:]
			}
			if !strings.HasPrefix(s, "-") || s == "-" {
				return rest
			}
			if withValue[s] && len(rest) > 1 {
				rest = rest[2:]
				continue
			}
			rest = rest[1:]
		}
		return rest
	}
	switch verb {
	case "sudo", "doas", "gsudo", "pkexec", "sudo-rs", "run0", "please":
		a.add(RuleElevation, verb+" runs a command with administrator rights", raw)
		rest = skip(map[string]bool{"-u": true, "-g": true, "-p": true, "-C": true, "-D": true, "-h": true,
			"-T": true, "-U": true, "-r": true, "-t": true, "--user": true, "--group": true, "--prompt": true})
		for len(rest) > 0 && strings.Contains(rest[0].s, "=") && !strings.HasPrefix(rest[0].s, "-") {
			rest = rest[1:] // sudo VAR=value cmd
		}
		return rest, true
	case "su":
		a.add(RuleElevation, "su switches to another user (usually root)", raw)
		for i := 0; i < len(rest); i++ {
			if s := rest[i].s; (s == "-c" || s == "--command") && i+1 < len(rest) {
				a.nested(rest[i+1].s, ShellPOSIX)
			}
		}
		return nil, true
	case "runas":
		a.add(RuleElevation, "runas runs a program as another user", raw)
		for len(rest) > 0 && strings.HasPrefix(rest[0].s, "/") {
			rest = rest[1:]
		}
		if len(rest) > 0 {
			a.nested(rest[0].s, ShellCmd)
		}
		return nil, true
	case "env":
		for len(rest) > 0 {
			s := rest[0].s
			switch {
			case s == "-S" || s == "--split-string":
				if len(rest) > 1 {
					a.nested(rest[1].s, ShellPOSIX)
				}
				return nil, true
			case s == "-u" || s == "--unset" || s == "-C" || s == "--chdir" || s == "-P":
				rest = rest[min(2, len(rest)):]
			case strings.HasPrefix(s, "-"):
				rest = rest[1:]
			case strings.Contains(s, "="):
				rest = rest[1:]
			default:
				return rest, true
			}
		}
		return rest, true
	case "nohup", "unbuffer", "chronic", "builtin", "noglob", "nocorrect", "time", "caffeinate", "stdbuf",
		"nice", "ionice", "exec", "command", "setsid", "torsocks", "proxychains", "proxychains4", "flock":
		if verb == "command" {
			for _, x := range rest {
				if x.s == "-v" || x.s == "-V" {
					return nil, true
				}
			}
		}
		if verb == "flock" && len(rest) > 0 {
			rest = skip(map[string]bool{"-w": true, "--timeout": true, "-E": true})
			if len(rest) > 0 {
				rest = rest[1:] // the lock file
			}
			return rest, true
		}
		return skip(wrapperValues[verb]), true
	case "tmx", "tmxg":
		// `tmx slot [--kind k] -- cmd ...` only waits for a heavy-command slot, then runs cmd: the
		// guard checks cmd, not tmx (Hostb's log, 3 Nov: agents use it for builds and tests).
		if len(rest) > 0 && strings.EqualFold(rest[0].s, "slot") {
			for i, x := range rest {
				if x.s == "--" {
					return rest[i+1:], true
				}
			}
		}
	case "timeout", "gtimeout":
		rest = skip(map[string]bool{"-s": true, "--signal": true, "-k": true, "--kill-after": true})
		if len(rest) > 0 {
			rest = rest[1:] // the duration
		}
		return rest, true
	case "watch":
		rest = skip(map[string]bool{"-n": true, "--interval": true, "-d": false})
		if len(rest) == 1 {
			a.nested(rest[0].s, ShellPOSIX)
			return nil, true
		}
		return rest, true
	}
	return nil, false
}

// wrapperValues are the options of each wrapper that take a value.
var wrapperValues = map[string]map[string]bool{
	"exec":        {"-a": true},
	"nice":        {"-n": true, "--adjustment": true},
	"ionice":      {"-c": true, "-n": true, "--class": true, "--classdata": true},
	"caffeinate":  {"-t": true, "-w": true},
	"stdbuf":      {"-i": true, "-o": true, "-e": true},
	"proxychains": {"-f": true}, "proxychains4": {"-f": true},
}

// rawChecks catch forms the word view misses (PowerShell method calls).
// psShadowDelete: removing shadow copies (restore points) through WMI/CIM (Python's SHADOW_PS).
var psShadowDelete = regexp.MustCompile(`(?i)Win32_ShadowCopy.*\b(Remove-(WmiObject|CimInstance)|Delete\(\))`)

func (a *analyzer) rawChecks(src string, sh Shell) {
	if sh != ShellPowerShell {
		return
	}
	if psKillMethod.MatchString(src) {
		a.add(RuleKillByName, "kills processes found by name (Get-Process ... .Kill())", src)
	}
	if psIEX.MatchString(src) && psDownload.MatchString(src) {
		a.add(RulePipeShell, "runs downloaded code with Invoke-Expression", src)
	}
	if psShadowDelete.MatchString(src) {
		a.add(RuleDisk, "deletes restore points (Win32_ShadowCopy)", src)
	}
	if m := psDirDelete.FindString(src); m != "" {
		a.add(RuleDeleteOutside, "deletes a folder through .NET ("+m+"), which tmx cannot check", src)
	}
}
