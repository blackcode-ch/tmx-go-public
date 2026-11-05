package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

type connectBoundaryTransport func(*http.Request) (*http.Response, error)

func (f connectBoundaryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the complete CLI handover with a synthetic owner-daemon transport.
// Replacing DefaultTransport is scoped to this nonparallel test; no sockets open.
func TestConnectBoundaryDaemonReply(t *testing.T) {
	testCompanionProtocol(t)
	const key = "Z9q7W8v6K5u4R3t2"
	for _, status := range []int{200, 400, 307, 308} {
		for _, echoed := range []string{key, key[:4], key[len(key)-4:]} {
			home := t.TempDir()
			t.Setenv(config.EnvHome, home)
			t.Setenv(companion.EnvURL, "http://127.0.0.1:8777")
			calls := 0
			previous := http.DefaultTransport
			http.DefaultTransport = connectBoundaryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Hostname() != "127.0.0.1" || r.URL.Path != "/api/companion/connect" {
					t.Fatal("credential sent to wrong recipient")
				}
				var input map[string]string
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["token"] != key {
					t.Fatal("key handover changed")
				}
				b, _ := json.Marshal(map[string]any{"label": echoed, "error": echoed, "key_ok": true})
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"http://example.invalid/stolen"}}, Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
			})
			// Restore even if a regression fails the test in RoundTrip.
			t.Cleanup(func() { http.DefaultTransport = previous })
			out, stderr, code := tmxIn(t, key+"\n", "companion", "connect", "--token-stdin", "--json")
			http.DefaultTransport = previous
			if calls != 1 {
				t.Fatalf("followed redirect or retried: %d calls", calls)
			}
			v := oneJSON(t, out)
			if (status == 200) != (code == 0 && v.OK) {
				t.Fatalf("unexpected result for HTTP %d: code=%d ok=%v", status, code, v.OK)
			}
			if strings.Contains(out+stderr, echoed) {
				t.Fatal("daemon echoed key material into CLI output")
			}
			if _, err := os.Stat((config.Paths{Home: home}).CompanionAgentTokenFile()); !os.IsNotExist(err) {
				t.Fatal("CLI unexpectedly wrote a key after daemon reply")
			}
		}
	}
}

// Pure refusal tests: no daemon, sockets or Companion process are started.
func TestConnectBoundaryRemoteEnvironment(t *testing.T) {
	testCompanionProtocol(t)
	for _, address := range []string{"http://example.invalid:8777", "http://localhost:8777", "http://127.0.0.1.evil.invalid", "http://[::ffff:127.0.0.1]:8777", "https://127.0.0.1:8777"} {
		t.Run(address, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "uncreated-state")
			t.Setenv(config.EnvHome, home)
			t.Setenv(companion.EnvURL, address)
			const key = "Z9q7W8v6K5u4R3t2"
			out, stderr, code := tmxIn(t, key+"\n", "companion", "connect", "--token-stdin", "--json")
			v := oneJSON(t, out)
			if code != 1 || v.OK || !strings.Contains(v.Error, "TMXG_URL") {
				t.Fatalf("remote entry accepted: %d %+v", code, v)
			}
			if strings.Contains(out+stderr, key) {
				t.Fatal("key echoed")
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatal("refusal should happen before loading/writing state")
			}
		})
	}
}

func TestConnectBoundaryFlagEcho(t *testing.T) {
	testCompanionProtocol(t)
	const key = "Z9q7W8v6K5u4R3t2"
	for _, args := range [][]string{
		{"--token", key, "--token-stdin", "--json"},
		{"--token-stdin", "--json=" + key, "--json"},
		{"--token-stdin", "--" + key, "--json"},
		{"--token-stdin", "--json", key},
	} {
		out, stderr, code := tmxIn(t, key+"\n", append([]string{"companion", "connect"}, args...)...)
		if code != 1 || oneJSON(t, out).OK {
			t.Fatalf("invalid arguments accepted: %d", code)
		}
		if strings.Contains(out+stderr, key) {
			t.Fatal("argument parsing echoed a credential")
		}
	}
}
