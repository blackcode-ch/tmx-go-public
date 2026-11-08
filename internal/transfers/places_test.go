package transfers

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCredentialStoreExportsRefused(t *testing.T) {
	user, state, install := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("USERPROFILE", user)
	local, roaming := filepath.Join(user, "Local"), filepath.Join(user, "Roaming")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)
	paths := []string{
		".ssh/id_ed25519", ".gnupg/private-keys-v1.d/key", ".aws/credentials", ".azure/accessTokens.json",
		".docker/config.json", ".npmrc", ".pypirc", ".git-credentials", ".netrc", ".kube/config",
		".config/gh/hosts.yml", ".claude/.credentials.json", ".codex/auth.json", ".config/gcloud/credentials.db",
	}
	if runtime.GOOS == "windows" {
		paths = append(paths, "Roaming/npm/etc/npmrc", "Roaming/npm/etc/other-config",
			"Roaming/npmrc", "Roaming/.npmrc", "Roaming/arbitrary/deep/npmrc", "Roaming/arbitrary/.NPMRC",
			"Roaming/gcloud/credentials.db", "Roaming/gcloud/access_tokens.db",
			"Roaming/gcloud/application_default_credentials.json", "Roaming/gcloud/legacy_credentials/account/adc.json",
			"Roaming/GitHub CLI/hosts.yml", "Local/Google/Chrome/User Data/Default/Login Data",
			"Local/Microsoft/Edge/User Data/Default/Login Data", "Roaming/Mozilla/Firefox/Profiles/default/logins.json",
			"Local/Microsoft/Credentials/key", "Roaming/Microsoft/Credentials/key",
			"Roaming/Microsoft/Protect/key", "Roaming/Microsoft/Crypto/key")
	} else {
		paths = append(paths, ".config/google-chrome/Default/Login Data", ".config/chromium/Default/Login Data",
			".config/microsoft-edge/Default/Login Data", ".mozilla/firefox/default/logins.json",
			"Library/Application Support/Google/Chrome/Default/Login Data",
			"Library/Application Support/Microsoft Edge/Default/Login Data",
			"Library/Application Support/Firefox/Profiles/default/logins.json", "Library/Keychains/login.keychain-db",
			".local/share/keyrings/login.keyring")
	}
	x := NewExports(t.TempDir())
	for _, rel := range paths {
		t.Run(rel, func(t *testing.T) {
			path := filepath.Join(user, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("synthetic test credential"), 0600); err != nil {
				t.Fatal(err)
			}
			if why := ForbiddenSource(path, state, install); why == "" {
				t.Fatal("credential source accepted")
			}
			if _, err := x.Prepare(path, "peer", "/dest", "test", state, install); !errors.Is(err, ErrForbidden) {
				t.Fatalf("credential export: %v", err)
			}
			if rel == ".npmrc" || rel == ".pypirc" || rel == ".docker/config.json" {
				if why := ForbiddenSource(path+"-unrelated", state, install); why != "" {
					t.Fatalf("unrelated sibling refused: %s", why)
				}
			}
		})
	}
}

func TestBothDaemonSecretStoresRefused(t *testing.T) {
	state, pythonState, install := t.TempDir(), t.TempDir(), t.TempDir()
	x := NewExports(t.TempDir())
	x.Extra = []string{pythonState}
	for _, root := range []string{state, pythonState} {
		path := filepath.Join(root, "secrets", "owner-token")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic test credential"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := x.Prepare(path, "peer", "/dest", "test", state, install); !errors.Is(err, ErrForbidden) {
			t.Fatalf("daemon secret export: %v", err)
		}
	}
}

func TestAppDataNpmProtectionBoundaries(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows APPDATA paths")
	}
	appdata, state, install := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("APPDATA", appdata)
	for _, path := range []string{
		filepath.Join(appdata, "npm", "etc"),
		filepath.Join(appdata, "nested", "NPMRC"),
		filepath.Join(appdata, "nested", ".npmrc"),
	} {
		if why := ForbiddenSource(path, state, install); why == "" {
			t.Errorf("accepted %s", path)
		}
	}
	for _, path := range []string{
		filepath.Join(appdata, "npm", "etc-sibling", "ordinary.txt"),
		filepath.Join(appdata, "nested", "npmrc-sibling"),
		filepath.Join(appdata, "nested", ".npmrc-sibling"),
		filepath.Join(appdata+"-sibling", "npmrc"),
	} {
		if why := ForbiddenSource(path, state, install); why != "" {
			t.Errorf("refused unrelated path: %s", why)
		}
	}
}
