package guard

import (
	"path"
	"regexp"
	"strings"
)

// target is a path an action touches, normalised for comparison: slash-separated, cleaned,
// absolute when known, lower-cased on case-insensitive systems (macOS, Windows).
type target struct {
	p       string // normalised; "" when unknown
	shown   string // as written (for messages)
	unknown bool   // computed at run time ($VAR, $(...)): tmx cannot tell where it points
	glob    bool   // the original had a wildcard; p is the folder the wildcard expands in
}

var (
	driveRx  = regexp.MustCompile(`^[a-zA-Z]:([\\/]|$)`)
	gitBash  = regexp.MustCompile(`^/[a-zA-Z](/|$)`)
	deviceRx = regexp.MustCompile(`^/dev/(r?disk\d|sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|md\d|dm-\d|mapper/|loop\d|sr\d)`)
)

// norm makes p comparable. cwd is already normalised.
func (a *analyzer) norm(p, cwd string) string {
	if p == "" {
		return ""
	}
	win := a.ctx.OS == "windows" || driveRx.MatchString(p) || strings.HasPrefix(p, `\\`)
	if win {
		p = strings.ReplaceAll(p, `\`, "/")
		if a.ctx.OS == "windows" && gitBash.MatchString(p) { // Git Bash: /c/Users = C:\Users
			p = p[1:2] + ":" + p[2:]
		}
	}
	unc := strings.HasPrefix(p, "//")
	if abs := strings.HasPrefix(p, "/") || driveRx.MatchString(p); !abs {
		if cwd == "" {
			return ""
		}
		p = strings.TrimSuffix(cwd, "/") + "/" + p
	}
	p = path.Clean(p)
	if unc {
		p = "/" + p
	}
	if len(p) == 2 && p[1] == ':' {
		p += "/"
	}
	if a.fold || win {
		p = strings.ToLower(p)
	}
	return p
}

// within reports whether p is root or inside it.
func within(p, root string) bool {
	if p == "" || root == "" {
		return false
	}
	if p == root {
		return true
	}
	if strings.HasSuffix(root, "/") {
		return strings.HasPrefix(p, root)
	}
	return strings.HasPrefix(p, root+"/")
}

// inside reports whether p is strictly below root.
func inside(p, root string) bool { return p != root && within(p, root) }

// userContent: p lies inside one of the owner's own folders under the home (Documents\x,
// Desktop\y, D... at least two levels below the home), where deleting a tree is ordinary work,
// not machine protection (the owner, 3 Nov 13:55: ordinary deletes in user folders never ask him).
// Not the home itself or its direct folders, not AppData/Library or dot-folders (programs and
// credentials live there), and never a folder named NROP (the owner's private exclusion).
func (a *analyzer) userContent(p string) bool {
	if a.home == "" || depthBelow(p, a.home) < 2 {
		return false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p, a.home), "/")
	first, _, _ := strings.Cut(rest, "/")
	switch f := strings.ToLower(first); {
	case f == "appdata", f == "library", strings.HasPrefix(f, "."):
		return false
	}
	for _, seg := range strings.Split(strings.ToLower(rest), "/") {
		if seg == "nrop" {
			return false
		}
	}
	return true
}

func depthBelow(p, root string) int {
	if !inside(p, root) {
		return -1
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
	return strings.Count(rest, "/") + 1
}

// posix folders that belong to the operating system at any depth.
var systemDeep = []string{"/system", "/bin", "/sbin", "/etc", "/boot", "/dev", "/proc", "/sys", "/lib", "/lib32",
	"/lib64", "/libx32", "/private/etc", "/private/var/db", "/usr/bin", "/usr/sbin", "/usr/lib", "/usr/libexec",
	"/usr/share", "/usr/include", "/efi"}

// posix folders whose direct children are still system folders (or whole disks, or homes).
var systemKids = []string{"/usr", "/library", "/private", "/var", "/opt", "/users", "/home", "/volumes", "/mnt",
	"/media", "/run", "/snap", "/nix", "/applications", "/root", "/cores", "/usr/local"}

// home folders whose recursive deletion is never an agent's to do.
var homeKids = []string{"documents", "desktop", "downloads", "library", "pictures", "music", "movies", "videos",
	"public", "applications", ".ssh", ".gnupg", ".config", "appdata", "onedrive", "dropbox", "google drive",
	"icloud drive", "sites", "projects", "source", "src", "code", "developer", "work", "git"}

// windows folders under a drive root.
var winDeep = []string{"windows", "boot", "efi", "recovery", "system volume information", "$recycle.bin", "$winreagent", "bootmgr"}
var winKids = []string{"program files", "program files (x86)", "programdata", "users"}

// protected describes why p is a root, home or system folder (deleting it wholesale, or
// changing its permissions recursively, is never allowed); "" when it is not one.
func (a *analyzer) protected(p string) string {
	if p == "" {
		return ""
	}
	if p == "/" {
		return "the root of the disk (/)"
	}
	if driveRx.MatchString(p) {
		drive := strings.ToUpper(p[:2])
		rest := strings.Trim(p[2:], "/")
		if rest == "" {
			return "the drive root " + drive + `\`
		}
		first, _, _ := strings.Cut(rest, "/")
		for _, d := range winDeep {
			if first == d {
				return "the system folder " + drive + `\` + first
			}
		}
		for _, d := range winKids {
			if first == d && !strings.Contains(rest, "/") {
				return "the system folder " + drive + `\` + rest
			}
			if first == d && strings.Count(rest, "/") == 1 {
				return "the system folder " + drive + `\` + strings.ReplaceAll(rest, "/", `\`)
			}
		}
	}
	if strings.HasPrefix(p, "//") && strings.Count(strings.Trim(p, "/"), "/") <= 1 {
		return "the network share " + p
	}
	if h := a.home; h != "" {
		if p == h {
			return "your home folder"
		}
		if within(h, p) {
			return "a folder that contains your home folder (" + p + ")"
		}
		if depthBelow(p, h) == 1 {
			kid := path.Base(p)
			for _, k := range homeKids {
				if kid == k {
					if kid[0] >= 'a' && kid[0] <= 'z' {
						kid = strings.ToUpper(kid[:1]) + kid[1:]
					}
					return "your " + kid + " folder"
				}
			}
		}
	}
	if strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") {
		if strings.Count(p, "/") == 1 {
			return "the system folder " + p
		}
		for _, d := range systemDeep {
			if within(p, d) {
				return "the system folder " + p
			}
		}
		for _, d := range systemKids {
			if depthBelow(p, d) == 1 {
				return "the system folder " + p
			}
		}
	}
	return ""
}

func (a *analyzer) inTemp(p string) bool {
	for _, t := range a.temp {
		if inside(p, t) {
			return true
		}
	}
	return false
}

// claudeData are Claude Code's own working folders, which it writes through the file tools
// (memory, plans, todos): not the owner's files.
func (a *analyzer) claudeData(p string) bool {
	if a.home == "" {
		return false
	}
	for _, d := range []string{"/.claude/projects", "/.claude/plans", "/.claude/todos"} {
		if inside(p, a.home+d) {
			return true
		}
	}
	return false
}

// claudeSettingsFile: a Claude Code settings file in any scope, incl. the agents' CLAUDE_CONFIG_DIRs
// in tmx's state (<home>/claude-config, and a Claude account's <home>/ai-accounts/homes/claude/<name>).
func (a *analyzer) claudeSettingsFile(p string) bool {
	if claudeSettings.MatchString(p) {
		return true
	}
	if a.state != "" && (within(p, a.state+"/claude-config") || within(p, a.state+"/ai-accounts/homes/claude")) {
		b := path.Base(p)
		return b == "settings.json" || b == "settings.local.json"
	}
	return false
}

var claudeSettings = regexp.MustCompile(`(^|/)\.claude/settings(\.local)?\.json$|(^|/)managed-settings\.json$|/\.claude\.json$`)

func isNullDevice(p string) bool {
	switch strings.ToLower(p) {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty", "nul", "$null", "nul:", "con", "/dev/zero":
		return true
	}
	return strings.HasPrefix(p, "/dev/fd/")
}

// stateSecret reports whether p is inside tmx's secrets or agents folders, or the WhatsApp
// attachments (other people's photos and voice notes: agents read WhatsApp only through tmx).
func (a *analyzer) stateSecret(p string) bool {
	if a.state == "" || p == "" {
		return false
	}
	return within(p, a.state+"/secrets") || within(p, a.state+"/agents") || within(p, a.state+"/whatsapp") ||
		// the AI accounts' homes: sign-ins, and a Claude account's settings.json, which is the
		// user settings of every agent on that account (disableAllHooks there would unguard them)
		within(p, a.state+"/ai-accounts") ||
		// the guard's own settings (config.json: rule_states, off_for, always_allow), the approvals
		// (tmx.db and its -wal/-shm) and daemon.json: an agent writing them would undo the owner gate
		p == a.state+"/config.json" || p == a.state+"/daemon.json" || strings.HasPrefix(p, a.state+"/tmx.db")
}

// inState reports whether p is in tmx's state folder. Secrets and agent folders always count;
// the rest not where the agent's folder or the workspace were put inside the state folder.
func (a *analyzer) inState(p string) bool {
	if a.state == "" || !within(p, a.state) {
		return false
	}
	return a.stateSecret(p) || !(within(p, a.own) || within(p, a.workspace))
}

// holdsState reports whether p contains tmx's state folder (deleting or moving p takes it along).
func (a *analyzer) holdsState(p string) bool { return a.state != "" && p != "" && within(a.state, p) }
