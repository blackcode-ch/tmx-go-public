package transfers

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Places no copy may read from, as in Python's transfers.forbidden(role "src"): tmx's own state
// (secrets, tokens, the database), the tmx installation, the system, and credential stores (an
// agent could be told to by an injected message). The receiving machine judges destinations
// itself (Python peers: their own forbidden(role "dest")).

// CredentialStores are keys and logins no copy may take along (Python credential_stores).
func CredentialStores() []string {
	home, _ := os.UserHomeDir()
	h := func(p ...string) string {
		if home == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, p...)...)
	}
	env := func(v string, p ...string) string {
		if os.Getenv(v) == "" {
			return ""
		}
		return filepath.Join(append([]string{os.Getenv(v)}, p...)...)
	}
	out := []string{h(".ssh"), h(".aws"), h(".azure"), h(".gnupg"), h(".docker", "config.json"),
		h(".config", "gh"), h(".git-credentials"), h(".netrc"), h(".kube"), h(".npmrc"), h(".pypirc"),
		h(".claude", ".credentials.json"), h(".codex", "auth.json"), h(".config", "gcloud")}
	if runtime.GOOS == "windows" {
		out = append(out, env("APPDATA", "npm", "etc"), env("APPDATA", "GitHub CLI"), env("APPDATA", "gcloud"), env("LOCALAPPDATA", "Google", "Chrome", "User Data"), env("LOCALAPPDATA", "Microsoft", "Edge", "User Data"),
			env("APPDATA", "Mozilla", "Firefox", "Profiles"), env("APPDATA", "Microsoft", "Credentials"),
			env("LOCALAPPDATA", "Microsoft", "Credentials"), env("APPDATA", "Microsoft", "Protect"), env("APPDATA", "Microsoft", "Crypto"))
	} else {
		out = append(out, h(".config", "google-chrome"), h(".config", "chromium"), h(".config", "microsoft-edge"), h(".mozilla"),
			h("Library", "Application Support", "Google", "Chrome"), h("Library", "Application Support", "Microsoft Edge"),
			h("Library", "Application Support", "Firefox", "Profiles"),
			h("Library", "Keychains"), h(".local", "share", "keyrings"))
	}
	return out
}

func systemRoots() []string {
	if runtime.GOOS == "windows" {
		var out []string
		for _, v := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
			if os.Getenv(v) != "" {
				out = append(out, os.Getenv(v))
			}
		}
		return out
	}
	return []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/lib", "/lib64", "/proc", "/sys", "/dev", "/var/lib", "/root"}
}

// within reports whether p is root or inside it (case-insensitive on Windows).
func within(p, root string) bool {
	return withinNormalized(p, root, string(filepath.Separator), normalizePath)
}

// ForbiddenSource says why path may not be copied from ("" = it may): inside tmx's state
// (home), its installation (install), the system, a credential store, or one of extra (the
// Python tmx's state on a machine that runs both).
func ForbiddenSource(path, home, install string, extra ...string) string {
	p, err := filepath.Abs(path)
	if err != nil {
		return "not a usable path"
	}
	// Resolve links, so a link cannot smuggle a forbidden place in under another name.
	p = normalizePath(p)
	// npm can place credential-bearing configuration anywhere under APPDATA.
	if runtime.GOOS == "windows" {
		root := os.Getenv("APPDATA")
		inside := within(p, root)
		name := filepath.Base(p)
		if inside && (strings.EqualFold(name, "npmrc") || strings.EqualFold(name, ".npmrc")) {
			return p + " is a credential store (npm configuration)"
		}
	}
	// Python exempts only the default cold-backup store from the state refusal.
	if within(p, filepath.Join(home, "backups")) {
		return ""
	}
	type place struct{ root, why string }
	places := []place{{home, "tmx's own state (secrets, tokens)"}, {install, "the tmx installation"}}
	for _, x := range extra {
		places = append(places, place{x, "tmx's own state (secrets, tokens)"})
	}
	for _, r := range systemRoots() {
		places = append(places, place{r, "a system folder"})
	}
	for _, c := range CredentialStores() {
		places = append(places, place{c, "a credential store (keys, logins)"})
	}
	for _, pl := range places {
		if within(p, pl.root) {
			return p + " is inside " + pl.why
		}
	}
	return ""
}

// ForbiddenDestination mirrors Python forbidden(role="dest"), including the final name.
func ForbiddenDestination(path, home, install string, extra ...string) string {
	p, err := filepath.Abs(path)
	if err != nil {
		return "not a usable path"
	}
	roots := append([]string{home, install}, extra...)
	roots = append(roots, systemRoots()...)
	user, _ := os.UserHomeDir()
	for _, v := range []string{".ssh", ".config/autostart", ".config/systemd", "Library/LaunchAgents"} {
		roots = append(roots, filepath.Join(user, filepath.FromSlash(v)))
	}
	if runtime.GOOS == "windows" && os.Getenv("APPDATA") != "" {
		roots = append(roots, filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup"))
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if within(p, root) {
			return "inside a protected state, installation, system or autostart folder"
		}
	}
	return ""
}

// noLinks rejects symlinks and Windows junctions in existing path components. Both read
// and placement paths are checked again immediately before use.
func noLinks(p string) error {
	var err error
	p, err = canonicalOSPrefix(p)
	if err != nil {
		return err
	}
	for q := filepath.Clean(p); ; q = filepath.Dir(q) {
		st, err := os.Lstat(q)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && st.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("path traverses a link or junction: %s", q)
		}
		if filepath.Dir(q) == q {
			return nil
		}
	}
}
