package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSensitiveP2PNames(t *testing.T) {
	for _, name := range []string{"AppData", ".ssh", ".gnupg", ".config", ".aws", ".azure", ".docker", ".git", ".env", ".env.prod", "a.pem", "a.key", "a.p12", "a.pfx", "id_rsa.pub", "id_ed25519.pub", "myCredentials.json", "client-secret.txt", "hosts.yml", ".npmrc", ".pypirc", "x.npmrc", "x.pypirc", "cookies", "Login Data", "Local State", "Web Data", ".git-credentials", ".netrc", "signature.pdf", "a.py", "a.pyw", "a.ps1", "a.psm1", "a.sh", "a.bash", "a.bat", "a.cmd", "a.exe", "a.dll", "a.kdbx", "a.sqlite", "a.db", ".venv", "venv", "node_modules", "Default", "Profile 12", "User Data", ".claude", ".codex", "hosts.yml. "} {
		if !sensitiveFilePart(name) {
			t.Errorf("allowed %s", name)
		}
	}
	for _, name := range []string{"report.pdf", "photo.png", "worktree", "report.txt"} {
		if sensitiveFilePart(name) {
			t.Errorf("refused %s", name)
		}
	}
}

func TestP2PContentPatterns(t *testing.T) {
	for _, token := range []string{"gh" + "p_" + strings.Repeat("x", 36), "gh" + "o_fixture", "github_" + "pat_fixture", "AK" + "IA" + strings.Repeat("A", 16), "-----BEGIN RSA " + "PRIVATE KEY-----", "-----BEGIN OPENSSH " + "PRIVATE KEY-----", "sk-" + "fixture", "bk_" + "live_fixture", "xox" + "b-fixture"} {
		for _, data := range [][]byte{[]byte(token), []byte("\x00\xff" + token), []byte(strings.Repeat("a", 1<<20) + token)} {
			if err := p2pContentRule(data); err == nil || !strings.Contains(err.Error(), "sanitized") || strings.Contains(err.Error(), token) {
				t.Fatal("missing or unsafe content refusal")
			}
		}
	}
}

func TestP2PWorkingFolderPolicy(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(cwd, "daemon-data")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, filepath.VolumeName(home) + string(filepath.Separator)} {
		if err := p2pWorkingFolderRule(dir, state); err == nil || !strings.Contains(err.Error(), "cwd-") {
			t.Errorf("profile/drive root allowed: %s", dir)
		}
	}
	for _, dir := range []string{state, filepath.Join(state, "nested"), filepath.Join(cwd, "AppData", "Local"), filepath.Join(cwd, "TerminalMatrix2"), filepath.Join(cwd, ".ssh"), filepath.Join(cwd, "tmx-go", "bin")} {
		if err := p2pWorkingFolderRule(dir, state); err == nil {
			t.Errorf("allowed %s", dir)
		}
	}
	if err := p2pWorkingFolderRule(filepath.Join(cwd, "project", "worktree"), state); err != nil {
		t.Fatal(err)
	}
	if system := os.Getenv("SystemRoot"); system != "" {
		if err := p2pWorkingFolderRule(filepath.Join(system, "System32"), state); err == nil {
			t.Fatal("system folder allowed")
		}
	}
}

func TestP2PMacTempFolder(t *testing.T) {
	for _, dir := range []string{
		"/var/folders/ab/user/T", "/var/folders/ab/user/T/project",
		"/private/var/folders/ab/user/T/project", "/private/var/folders/ab/user/T/project/../work",
	} {
		if !p2pMacTempFolder(dir) {
			t.Errorf("temp folder refused: %s", dir)
		}
	}
	for _, dir := range []string{
		"/var", "/private/var", "/var/folders", "/var/folders/ab", "/var/folders/ab/user",
		"/private/var/folders/ab/user/C", "/private/var/folders/ab/user/Trash",
		"/private/var/folders/ab/user/T/..", "/var/folders/ab/user/T/../../../../log",
		"/System", "/Library", "/private/etc", "/usr", "/bin", "/sbin", "/Applications",
		"/private/var/db", "/private/var/root", "/var/folders-other/ab/user/T",
	} {
		if p2pMacTempFolder(dir) {
			t.Errorf("non-temp folder exempted: %s", dir)
		}
	}
}

func TestP2PTempWorkingFolder(t *testing.T) {
	project := t.TempDir()
	state := filepath.Join(project, "daemon-state")
	for _, dir := range []string{project, resolvedFolder(project)} {
		if err := p2pWorkingFolderRule(dir, state); err != nil {
			t.Fatalf("native temp workspace refused: %v", err)
		}
	}
	// Exercise a Windows-style user temp layout with native paths on every OS.
	appdata := filepath.Join(project, "AppData", "Local")
	temp := filepath.Join(appdata, "Temp")
	t.Setenv("LOCALAPPDATA", appdata)
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	if err := p2pWorkingFolderRule(filepath.Join(temp, "work"), state); err != nil {
		t.Fatalf("user temp workspace refused: %v", err)
	}
	for _, dir := range []string{appdata, filepath.Join(appdata, "Other"), filepath.Join(temp, ".ssh"), filepath.Join(temp, "AppData"), filepath.Join(temp, "state")} {
		if err := p2pWorkingFolderRule(dir, filepath.Join(temp, "state")); err == nil {
			t.Errorf("protected folder allowed: %s", dir)
		}
	}
}
