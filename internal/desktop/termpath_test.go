package desktop

import "testing"

func TestTerminalPath(t *testing.T) {
	for in, want := range map[string]string{
		"/api/agents/a1/terminal":                       "/api/agents/a1/terminal|",
		"/api/accounts/codex/login/terminal":            "/api/accounts/codex/login/terminal|",
		"/api/accounts/codex/login/terminal?account=w2": "/api/accounts/codex/login/terminal|account=w2",
	} {
		p, q, err := TerminalPath(in)
		if err != nil || p+"|"+q.Encode() != want {
			t.Errorf("%s: %s %v %v", in, p, q, err)
		}
	}
	for _, bad := range []string{
		"/api/agents/a1/terminal?account=x",          // only a sign-in names an account
		"/api/accounts/codex/login/terminal?token=x", // nothing else
		"/api/accounts/codex/login/terminal?account=a&account=b",
		"/api/accounts/codex/login",
		"ws://evil/api/accounts/codex/login/terminal",
		"/api/accounts/../x/terminal",
		"/other/terminal",
	} {
		if _, _, err := TerminalPath(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
