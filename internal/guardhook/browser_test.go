package guardhook

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/guard"
)

// The hook takes the browser rule's state from the daemon on every call (the owner's rule switches,
// 3 Nov); a missing or broken answer means the strict built-in outcomes, never "anything goes".
// Uses only an in-memory HTTP fixture, never apitest.New or daemon state/ACLs.
func TestBrowserRuleFromTheDaemon(t *testing.T) {
	const unsafe = "chrome.exe https://example.com"
	const safe = `C:\Users\o\AppData\Local\ms-playwright\chromium-1140\chrome-win\chrome.exe --disable-features=WebAuthenticationUseNativeWinApi --user-data-dir=C:\x https://example.com`
	for _, tc := range []struct {
		name, body string
		status     int
		cmd        string
		allowed    bool
	}{
		{"blocked", `{"states":{"browser-automation":"block"}}`, 200, safe, false},
		{"allowed, safe launch", `{"states":{"browser-automation":"allow"}}`, 200, safe, true},
		{"allowed, unsafe launch", `{"states":{"browser-automation":"allow"}}`, 200, unsafe, false},
		{"no states = strict", `{}`, 200, safe, false},
		{"bad JSON = strict", `invalid`, 200, safe, false},
		{"wrong type = strict", `{"states":"allow"}`, 200, safe, false},
		{"daemon down = strict", `{}`, 503, safe, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/guard/policy" {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				w.WriteHeader(200)
			}))
			defer srv.Close()
			o := Options{Client: client.New(srv.URL, "fixture"), Context: guard.Context{OS: "windows"}}
			dec := run(t, o, input("PowerShell", map[string]any{"command": "& " + tc.cmd}))
			if tc.allowed {
				if dec != nil {
					t.Fatalf("not let through: %+v", dec)
				}
			} else if dec == nil || dec.PermissionDecision != "deny" {
				t.Fatalf("not denied: %+v", dec)
			}
		})
	}
	// No daemon at all: a browser tool needs the owner (scripted), and nobody can be asked.
	o := Options{Context: guard.Context{OS: "windows"}}
	if dec := run(t, o, input("mcp__browser__navigate", nil)); dec == nil || dec.PermissionDecision != "deny" {
		t.Fatal("missing host must deny")
	}
}
