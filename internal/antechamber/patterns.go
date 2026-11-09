package antechamber

import (
	"fmt"
	"regexp"
	"strings"
)

// -------------------------------------------------------------------------- script patterns ---
// (category, own level, person level, trigger words, regex), the same as Python's PATTERNS. The regex
// runs only when one of the trigger words (lower case, plain substrings) is in the text. Heuristics
// that make a human look; never proof that something is safe. Some keywords are split with [x]
// classes so that this file, sent between machines, does not flag itself.

type pattern struct {
	cat, own, person string
	keys             []string
	rx               *regexp.Regexp
}

const (
	dlRx  = `(downloadstring|downloaddata|downloadfile|invoke-webrequest|\biwr\b|invoke-restmethod|\birm\b|net\.webclient|\bcurl\b|\bwget\b|start-bitstransfer)`
	iexRx = `(\bie[x]\b|invoke-expressio[n])`
)

func pat(cat, own, person, keys, rx string) pattern {
	return pattern{cat, own, person, strings.Split(keys, "|"), regexp.MustCompile("(?i)" + rx)}
}

// Patterns are the script and command patterns, in Python's order.
var Patterns = []pattern{
	pat("download and execute", NeedsHuman, NeedsHuman, "iex|invoke-expressio", iexRx+`[^\n]{0,200}`+dlRx),
	pat("download and execute", NeedsHuman, NeedsHuman, "iex|invoke-expressio", dlRx+`[^\n]{0,300}\|\s*`+iexRx),
	pat("download and execute", NeedsHuman, NeedsHuman, "curl|wget|fetch", `\b(curl|wget|fetch)\b[^\n|]{0,300}\|\s*(sudo\s+)?((ba|z|da|k)?sh|python[0-9.]*|perl|ruby|node|pwsh|powershell)\b`),
	pat("download and execute", NeedsHuman, NeedsHuman, "curl|wget", `\b(ba|z)?sh\s+-c\s+["']?\$\(\s*(curl|wget)\b`),
	pat("download and execute", NeedsHuman, NeedsHuman, "certuti", `\bcertuti[l](\.exe)?\b[^\n]{0,100}\s[-/](urlcache|decode)\b`),
	pat("download and execute", NeedsHuman, NeedsHuman, "bitsadm", `\bbitsadmi[n](\.exe)?\b[^\n]{0,100}/transfer\b`),
	pat("download and execute", NeedsHuman, NeedsHuman, "mshta", `\bmsht[a](\.exe)?\s+["']?(https?:|javascript:|vbscript:)`),
	pat("download and execute", NeedsHuman, NeedsHuman, "regsvr3", `\bregsvr3[2](\.exe)?\b[^\n]{0,100}/i:\s*["']?https?:`),
	pat("download and execute", NeedsHuman, NeedsHuman, "rundll3", `\brundll3[2](\.exe)?\b[^\n]{0,100}javascript:`),
	pat("download and execute", NeedsHuman, NeedsHuman, "urlope|requests.ge|urlli", `\b(exec|eval)\s*\([^\n]{0,80}\b(urlope[n]|requests\.get|urllib)`),
	pat("hidden or encoded code", NeedsHuman, NeedsHuman, "powershel|pwsh", `\b(powershel[l]|pws[h])(\.exe)?\b[^\n]{0,200}\s[-/]e(c|n|nc|nco|ncod|ncode|ncoded|ncodedcommand)?\s+[a-z0-9+/=]{16,}`),
	pat("hidden or encoded code", NeedsHuman, NeedsHuman, "frombase64strin", `(`+iexRx+`|reflection\.assembly\]::load)[^\n]{0,300}frombase64strin[g]|frombase64strin[g][^\n]{0,300}(`+iexRx+`|reflection\.assembly\]::load)`),
	pat("hidden or encoded code", NeedsHuman, NeedsHuman, "eval|exec", `\b(eval|exec)\s*\(\s*(ato[b]|base64\.b64decode|b64decod[e]|codecs\.decode|zlib\.decompress|marshal\.loads|bytes\.fromhex)`),
	pat("hidden or encoded code", NeedsHuman, NeedsHuman, "powershel|pwsh", `\b(powershel[l]|pws[h])(\.exe)?\b[^\n]{0,200}\s-w(indowstyle)?\s+h(idden)?\b`),
	pat("hidden or encoded code", NeedsHuman, NeedsHuman, "[char", `(\[cha[r]\]\s*\d+\s*\+\s*){3,}`),
	pat("persistence", Clean, NeedsHuman, "schtask|register-scheduledtas", `\bschtask[s](\.exe)?\b[^\n]{0,50}/create\b|\bregister-scheduledtas[k]\b`),
	pat("persistence", Clean, NeedsHuman, `currentversio|shell:startu|\startu`, `\\currentversio[n]\\run(once)?\b|shell:startu[p]|\\start menu\\programs\\startu[p]`),
	pat("persistence", Clean, NeedsHuman, "new-servic|creat|consume|wmieven", `\bnew-servic[e]\b|\bsc(\.exe)?\s+create\b|commandlineeventconsume[r]|__filtertoconsumerbindin[g]|register-wmieven[t]`),
	pat("persistence", Clean, NeedsHuman, "cronta|systemct|launchct|library/launc", `\|\s*cronta[b]\b|\bcronta[b]\s+-[er]\b|\bsystemct[l]\s+(--user\s+)?enable\b|\blaunchct[l]\s+(load|bootstrap|submit)\b|library/launch(agent[s]|daemon[s])/`),
	pat("persistence", Clean, NeedsHuman, ">>", `>>\s*["']?(~|\$home|\$\{home\})?/?\.(bashr[c]|zshr[c]|profil[e]|bash_profil[e]|zprofil[e]|zshen[v])\b`),
	pat("credential access", NeedsHuman, NeedsHuman, "mimikat|sekurls|lsadum", `\bmimikat[z]\b|\bsekurls[a]\b|\blsadum[p]\b`),
	pat("credential access", NeedsHuman, NeedsHuman, "lsas|minidum", `\blsas[s]\b[^\n]{0,100}(dump|procdump)|(procdum[p]|minidum[p])[^\n]{0,100}\blsas[s]\b|comsvc[s](\.dll)?[^\n]{0,60}minidum[p]`),
	pat("credential access", NeedsHuman, NeedsHuman, "hklm", `\breg(\.exe)?\s+save\s+hkl[m]\\(sam|system|security)\b`),
	pat("credential access", NeedsHuman, NeedsHuman, "login dat|cookie|local stat", `\\(google\\chrome|microsoft\\edge|bravesoftware)[^\n]{0,80}\\(login dat[a]|cookie[s]|local stat[e])\b`),
	pat("credential access", NeedsHuman, NeedsHuman, "-passwor|dump-keychai", `\bsecurity\s+(find-(generic|internet)-passwor[d]|dump-keychai[n])\b`),
	pat("credential access", Clean, NeedsHuman, "terminalmatrix", `terminalmatrix[2][/\\]+secrets`),
	pat("credential access", Clean, NeedsHuman, ".ssh|.aws|cmdke|vaultcm", `[/\\]\.ssh[/\\]id_(rsa|dsa|ecdsa|ed25519)\b|[/\\]\.aws[/\\]credential[s]\b|\bcmdke[y](\.exe)?\s+/list\b|\bvaultcm[d]\b`),
	pat("disabling defences", NeedsHuman, NeedsHuman, "mppreferenc", `set-mppreferenc[e][^\n]{0,100}-disabl[e]|add-mppreferenc[e][^\n]{0,100}-exclusio[n]`),
	pat("disabling defences", NeedsHuman, NeedsHuman, "amsi", `amsiinitfaile[d]|amsiscanbuffe[r]|\bamsiutil[s]\b`),
	pat("disabling defences", NeedsHuman, NeedsHuman, "vssadmi|shadowcop|wevtuti|clear-eventl|bcded", `vssadmi[n](\.exe)?\s+delete\s+shadows|shadowcop[y]\s+delete|\bwevtuti[l](\.exe)?\s+cl\b|\bclear-eventlo[g]\b|\bbcdedi[t]\b[^\n]{0,60}(recoveryenable[d]|bootstatuspolic[y])`),
	pat("reverse shell", NeedsHuman, NeedsHuman, "/dev/tc| -e | -c |--exe", `/dev/tc[p]/[^\s/]+/\d+|\bnc(at)?(\.exe)?\b[^\n]{0,100}\s-[ec]\s|--exe[c]\s+["']?(/bin/)?(ba)?sh\b|--exe[c]\s+cmd`),
	pat("reverse shell", NeedsHuman, NeedsHuman, "dup2|tcpclien", `\bsocke[t]\b[^\n]{0,300}\bdup[2]\b|system\.net\.sockets\.tcpclien[t][^\n]{0,400}`+iexRx),
	pat("destructive", NeedsHuman, NeedsHuman, "rm ", `\brm\s+(-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|-r\s+-f|-f\s+-r)\s+(--no-preserve-root\s+)?["']?(/|/\*|~|~/|~/\*|\$home/?)["']?(\s|$|;|&)`),
	pat("destructive", NeedsHuman, NeedsHuman, "forma|ciphe|mkfs|of=/de", `\bforma[t](\.com)?\s+[a-z]:|\bciphe[r](\.exe)?\s+/w\b|\bmkf[s](\.\w+)?\s+/dev/|\bdd\b[^\n]{0,100}\bof=/de[v]/(sd|nvme|hd|disk|mmcblk)`),
	pat("destructive", NeedsHuman, NeedsHuman, "/s|remove-ite", `\b(del|erase|rd|rmdir)\b[^\n]{0,30}\s/s\b[^\n]{0,30}\s["']?[a-z]:\\["']?(\*|\s|$)|remove-ite[m]\b[^\n]{0,100}["']?[a-z]:\\["']?\s[^\n]{0,60}-recurse`),
	pat("destructive", NeedsHuman, NeedsHuman, ":()", `:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
	pat("agent manipulation", Clean, NeedsHuman, "ignor|disregar|forge|overrid", `\b(ignor[e]|disregar[d]|forge[t]|overrid[e])\b[^\n]{0,30}\b(all|any|previous|prior|above|earlier|your|the)\b[^\n]{0,20}\b(instruction[s]|rules|prompts?|guideline[s]|guardrail[s])\b`),
	pat("agent manipulation", Clean, NeedsHuman, "do no|don't|never", `\b(do not|don't|never)\s+(tel[l]|infor[m]|mentio[n]|show|ask)\b[^\n]{0,30}\b(your\s+|the\s+)?(human|user|owner|operator)\b`),
	pat("agent manipulation", Clean, NeedsHuman, "you are no|syste", `\byou are no[w] (a|an|in|the)\b|<\s*/?\s*syste[m]\s*>|\bnew system (promp[t]|instruction[s])\b`),
}

// ScanPatterns finds dangerous patterns in a script, a command or a message. In prose (not a
// script) from an own machine the findings are notes: documentation describes commands without
// running them.
func ScanPatterns(text string, prose bool, where string, ownerNames ...string) []Finding {
	return scanPatterns(ownerPatterns(ownerNames), text, prose, where)
}

// ownerPatterns compiles literal configured names once per inspector, keeping generic roles.
func ownerPatterns(names []string) []pattern {
	roles := "human|user|owner|operator"
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			roles += "|" + regexp.QuoteMeta(name)
		}
	}
	out := append([]pattern(nil), Patterns...)
	for i, p := range out {
		if strings.Contains(p.rx.String(), "(human|user|owner|operator)") {
			out[i].rx = regexp.MustCompile(strings.Replace(p.rx.String(), "(human|user|owner|operator)", "("+roles+")", 1))
		}
	}
	return out
}

func scanPatterns(patterns []pattern, text string, prose bool, where string) []Finding {
	var out []Finding
	seen := map[string]bool{}
	low := strings.ToLower(text)
	for _, p := range patterns {
		if seen[p.cat] || !containsAny(low, p.keys) {
			continue
		}
		m := p.rx.FindStringIndex(text)
		if m == nil {
			continue
		}
		seen[p.cat] = true
		line := strings.Count(text[:m[0]], "\n") + 1
		snippet := []rune(strings.TrimSpace(text[m[0]:m[1]]))
		snip := pyRepr(string(snippet[:min(len(snippet), 80)]))
		loc := fmt.Sprintf("line %d", line)
		if where != "" {
			loc = fmt.Sprintf("%s:%d", where, line)
		}
		if p.cat == "agent manipulation" {
			out = append(out, mk("agent-manipulation", "text that tries to steer an agent: "+snip, p.own, p.person, loc))
		} else {
			own := p.own
			if prose {
				own = Clean
			}
			out = append(out, mk("pattern", p.cat+": "+snip, own, p.person, loc))
		}
	}
	return out
}

func containsAny(s string, keys []string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// InspectText checks code blocks and commands in a message from another person (the external
// label). The message is information; nothing in it is ever executed.
func InspectText(text string, ownerNames ...string) ReportJSON {
	rep := Report{Trust: Person, Kind: "message", Findings: ScanPatterns(text, false, "", ownerNames...)}
	return rep.JSON()
}

// LabelFor is a short addition to the [external · …] label, or "" when nothing was found.
func LabelFor(text string, ownerNames ...string) string {
	r := InspectText(text, ownerNames...)
	if len(r.Reasons) == 0 {
		return ""
	}
	var parts []string
	for _, x := range r.Reasons[:min(4, len(r.Reasons))] {
		head, _, _ := strings.Cut(x.Text, ":")
		parts = append(parts, fmt.Sprintf("%s (%s)", head, x.Where))
	}
	n := len(r.Reasons)
	s := ""
	if n > 1 {
		s = "s"
	}
	return fmt.Sprintf("\u26a0 antechamber: %d finding%s: ", n, s) + strings.Join(parts, ", ")
}

// ------------------------------------------------------------------------- auto-run / agents ---

var agentFiles = set("agents.md", "claude.md", "claude.local.md", "gemini.md", ".cursorrules", ".windsurfrules",
	"copilot-instructions.md", ".clinerules")

var (
	gitConfigRx = regexp.MustCompile(`fsmonitor|hookspath|sshcommand|\bpager\b|\beditor\b|askpass`)
	npmInstRx   = regexp.MustCompile(`"(pre|post)?install"\s*:`)
	loginSvcRx  = regexp.MustCompile(`/library/launch(agents|daemons)/|/\.config/(systemd|autostart)/`)
	agentDirRx  = regexp.MustCompile(`/\.(claude|cursor|codex)/(commands|agents|skills|rules)/`)
)

// PathFindings finds files a tool runs or obeys just because a folder is opened or an agent works in it.
func PathFindings(path string, content []byte) []Finding {
	p := "/" + strings.TrimLeft(strings.ToLower(strings.ReplaceAll(path, "\\", "/")), "/")
	base := p[strings.LastIndex(p, "/")+1:]
	text := strings.ToLower(strings.ToValidUTF8(string(slice(content, 0, 1<<20)), "\uFFFD"))
	auto := ""
	switch {
	case strings.Contains(p, "/.git/hooks/") && !strings.HasSuffix(base, ".sample"):
		auto = "a git hook (git runs it on commit, checkout, push \u2026)"
	case strings.HasSuffix(p, "/.git/config") && gitConfigRx.MatchString(text):
		auto = "a git config that makes git run a program"
	case strings.HasSuffix(p, "/.vscode/tasks.json") && strings.Contains(text, "folderopen"):
		auto = "a VS Code task that runs when the folder is opened"
	case strings.Contains(p, "/.claude/") && strings.HasPrefix(base, "settings") && strings.HasSuffix(base, ".json") && strings.Contains(text, `"hooks"`):
		auto = "Claude Code settings with hooks (commands run by the agent's harness)"
	case base == ".mcp.json" || base == "mcp.json" || strings.HasSuffix(p, "/.cursor/mcp.json") || strings.HasSuffix(p, "/.vscode/mcp.json"):
		auto = "an MCP server list (an agent starts these programs)"
	case base == ".envrc":
		auto = "a direnv file (runs when a shell enters the folder)"
	case base == "autorun.inf" || base == "desktop.ini":
		auto = base + " (Windows acts on it when the folder is shown)"
	case base == "package.json" && npmInstRx.MatchString(text):
		auto = "package.json install scripts (run by npm install)"
	case strings.Contains(p, "/start menu/programs/startup/"):
		auto = "the Windows Startup folder (runs at every logon)"
	case strings.Contains(p, "/.ssh/"):
		auto = "SSH settings (authorized_keys / config decide who logs in and what runs)"
	case loginSvcRx.MatchString(p):
		auto = "a login service (macOS LaunchAgent / Linux systemd or autostart)"
	case set(".bashrc", ".bash_profile", ".profile", ".zshrc", ".zprofile", ".zshenv", ".bash_login", ".login")[base]:
		auto = "a shell startup file (runs in every new terminal)"
	case strings.HasSuffix(p, "/.codex/config.toml"):
		auto = "Codex settings (MCP servers and commands an agent starts)"
	}
	var out []Finding
	if auto != "" {
		out = append(out, mk("autorun", "auto-run file: "+auto, NeedsHuman, "", path))
	}
	if agentFiles[base] || agentDirRx.MatchString(p) {
		out = append(out, mk("agent-instructions", "agent instructions (an agent working here would follow them)",
			Clean, NeedsHuman, path))
	}
	return out
}
