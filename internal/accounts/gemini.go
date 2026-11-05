package accounts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Gemini CLI (npm @google/gemini-cli; source google-gemini/gemini-cli at 89e9d25, 4 Nov 2026):
//   - Its user state is <home>/.gemini, where home is $GEMINI_CLI_HOME, else the user's home
//     (packages/core/src/utils/paths.ts homedir(); docs/reference/configuration.md
//     "GEMINI_CLI_HOME": "keeping CLI state isolated").
//   - The Google sign-in's token goes to the OS keychain under one fixed name
//     ("gemini-cli-oauth"/"main-account", code_assist/oauth-credential-storage.ts), shared by
//     every home, unless GEMINI_FORCE_FILE_STORAGE=true: then to the encrypted file
//     <home>/.gemini/gemini-credentials.json (services/keychainService.ts, fileKeychain.ts).
//     So a second account needs both variables (HomeEnv).
//   - The signed-in Google account is <home>/.gemini/google_accounts.json {"active": email,
//     "old": [...]} (utils/userAccountManager.ts, config/storage.ts): no token, only addresses.
//     /signout clears it with the credentials.
//   - There is no sign-in, status or sign-out command: `gemini` signs in on its first run
//     (GOOGLE_GENAI_USE_GCA=true picks "Login with Google"), and /signout signs out inside it.
//   - No plan or quota is reported outside a session.

// geminiLoginEnv makes the first run go straight to Google's sign-in.
var geminiLoginEnv = []string{"GOOGLE_GENAI_USE_GCA=true"}

func geminiLogin(context.Context, *scope, string) []string  { return []string{} }
func geminiLogout(context.Context, *scope, string) []string { return nil }

// geminiHome is Gemini CLI's home for s: the account's GEMINI_CLI_HOME, else the user's home.
func (s *scope) geminiHome() string {
	env := s.env()
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "GEMINI_CLI_HOME="); ok && v != "" {
			return v
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// geminiActive is the signed-in Google account's address, "" when none.
func geminiActive(s *scope) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.geminiHome(), ".gemini", "google_accounts.json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var f struct {
		Active *string `json:"active"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return "", err
	}
	if f.Active == nil {
		return "", nil
	}
	return *f.Active, nil
}

func geminiSignedIn(s *scope) bool {
	email, err := geminiActive(s)
	return err == nil && email != ""
}

func geminiCheck(_ context.Context, s *scope, _ string) result {
	email, err := geminiActive(s)
	if err != nil {
		return result{err: "Couldn't read Gemini CLI's account: " + err.Error()}
	}
	if email == "" {
		return result{known: true}
	}
	return result{known: true, connected: true, id: Identity{Method: MethodSubscription, Email: email},
		note: "Gemini CLI reports its quota only inside a session"}
}
