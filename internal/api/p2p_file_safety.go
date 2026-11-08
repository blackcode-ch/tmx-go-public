package api

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Keep this superset in sync with Python tmx/servedcheck.py BAD_FILE/BAD_DIR.
var servedUnsafeName = regexp.MustCompile(`(?i)(\.(py|pyw|ps1|psm1|sh|bash|bat|cmd|exe|dll|key|pem|p12|pfx|kdbx|sqlite|db|npmrc|pypirc)$|signature|^profile [0-9]+$)`)

func sensitiveFilePart(part string) bool {
	p := strings.ToLower(strings.TrimRight(part, " ."))
	if strings.HasPrefix(p, ".") || strings.ContainsAny(p, ":\x00") || servedUnsafeName.MatchString(p) {
		return true
	}
	for _, x := range []string{"credentials", "secret"} {
		if strings.Contains(p, x) {
			return true
		}
	}
	switch p {
	case "appdata", "tokens", "claude-config", "ai-accounts", "terminalmatrix2", "tmxg-trial", "tmx-go", "tmx", "hosts.yml", "cookies", "login data", "local state", "web data", "venv", "node_modules", "default", "user data":
		return true
	}
	return strings.HasPrefix(p, "id_rsa") || strings.HasPrefix(p, "id_ed25519")
}

func fileRule(rule string) error {
	return fmt.Errorf("%s: refused; use a regular, non-sensitive document in a project working folder outside system, profile-root and application-state folders", rule)
}

// Check both lexical and resolved cwd so aliases cannot hide a protected root.
func p2pWorkingFolderRule(base, state string) error {
	base = filepath.Clean(base)
	if filepath.Dir(base) == base {
		return fileRule("cwd-drive-root")
	}
	if home, err := os.UserHomeDir(); err != nil {
		return fileRule("cwd-profile-unknown")
	} else {
		for _, h := range []string{home, resolvedFolder(home)} {
			if insidePath(base, h) {
				return fileRule("cwd-profile-root")
			}
		}
	}
	// Also refuse other users' profile roots and profile container folders.
	p := strings.ToLower(filepath.ToSlash(base))
	// Only the temp prefix is exempt from application-state name checks.
	// Descendants still undergo the normal sensitive-path checks.
	tempRoot := ""
	for _, tmp := range []string{os.TempDir(), resolvedFolder(os.TempDir())} {
		if insidePath(tmp, base) {
			tempRoot = tmp
			break
		}
	}
	for _, container := range []string{"users", "home"} {
		if strings.EqualFold(filepath.Base(filepath.Dir(base)), container) || strings.EqualFold(filepath.Base(base), container) {
			return fileRule("cwd-profile-root")
		}
	}
	protected := []string{state}
	if exe, err := os.Executable(); err == nil {
		protected = append(protected, filepath.Dir(exe))
	}
	for _, env := range []string{"SystemRoot", "WINDIR", "ProgramFiles", "ProgramFiles(x86)", "ProgramData", "LOCALAPPDATA", "APPDATA", "TMX_HOME", "TMXG_HOME"} {
		if dir := os.Getenv(env); dir != "" {
			if (env == "LOCALAPPDATA" || env == "APPDATA") && tempRoot != "" && insidePath(dir, tempRoot) && !strings.EqualFold(filepath.Clean(dir), filepath.Clean(tempRoot)) {
				continue
			}
			protected = append(protected, dir)
		}
	}
	if runtime.GOOS == "windows" {
		vol := filepath.VolumeName(base) + string(filepath.Separator)
		for _, dir := range []string{"Windows", "Program Files", "Program Files (x86)", "ProgramData", "Recovery", "System Volume Information", "$Recycle.Bin"} {
			protected = append(protected, filepath.Join(vol, dir))
		}
	} else {
		protected = append(protected, "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/dev", "/proc", "/sys", "/System", "/Library", "/Applications", "/root", "/private/etc")
		if !p2pMacTempFolder(base) {
			protected = append(protected, "/var", "/private/var")
		}
	}
	for _, dir := range protected {
		if dir != "" && (insidePath(dir, base) || insidePath(resolvedFolder(dir), base)) {
			return fileRule("cwd-system-state-install")
		}
	}
	if tempRoot != "" {
		prefix := strings.TrimPrefix(strings.ToLower(filepath.ToSlash(tempRoot)), strings.ToLower(filepath.ToSlash(filepath.VolumeName(tempRoot))))
		for _, part := range strings.Split(prefix, "/") {
			if part != "" && part != "appdata" && sensitiveFilePart(part) {
				return fileRule("cwd-sensitive-path")
			}
		}
		rel, err := filepath.Rel(tempRoot, base)
		if err == nil {
			p = strings.ToLower(filepath.ToSlash(rel))
			if p == "." {
				p = ""
			}
		}
	}
	for _, part := range strings.Split(strings.TrimPrefix(p, strings.ToLower(filepath.ToSlash(filepath.VolumeName(base)))), "/") {
		if part != "" && sensitiveFilePart(part) {
			return fileRule("cwd-sensitive-path")
		}
	}
	return nil
}

// Use slash-based cleaning so the macOS classifier is testable on every OS.
// /var is a symlink to /private/var on macOS; only the per-user T directory
// is a working area, never its parents or the rest of /private/var.
func p2pMacTempFolder(dir string) bool {
	p := path.Clean(strings.ReplaceAll(dir, `\`, "/"))
	for _, root := range []string{"/var/folders/", "/private/var/folders/"} {
		if rest, ok := strings.CutPrefix(p, root); ok {
			parts := strings.Split(rest, "/")
			return len(parts) >= 3 && parts[2] == "T"
		}
	}
	return false
}

func resolvedFolder(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// The shell guard has no reusable content scanner; the mirror uses external
// gitleaks. These conservative prefix rules apply to the exact bytes sent,
// scanning ALL bytes (including binary files), beyond the 1 MiB minimum.
var p2pSecretRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"github-token", regexp.MustCompile(`(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]+`)},
	{"aws-access-key", regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}`)},
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"sk-key", regexp.MustCompile(`sk-[A-Za-z0-9_-]+`)},
	{"bk-live-key", regexp.MustCompile(`bk_live_[A-Za-z0-9_-]+`)},
	{"slack-token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]+`)},
}

func p2pContentRule(data []byte) error {
	for _, rule := range p2pSecretRules {
		if rule.re.Match(data) {
			// Never include matching bytes in responses or logs.
			return fmt.Errorf("content-%s: refused; remove credentials and send a sanitized document from your project folder", rule.name)
		}
	}
	return nil
}
