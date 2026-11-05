package companion_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

// No listeners: these tests exercise net/http's redirect machinery using a
// transport that records requests and returns synthetic responses.
type boundaryTransport func(*http.Request) (*http.Response, error)

func (f boundaryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func boundaryResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestConnectBoundaryNoRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, target := range []string{"http://example.invalid/stolen", "http://127.0.0.1:9999/stolen"} {
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				calls := 0
				original := &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					resp := boundaryResponse(r, status, "")
					resp.Header.Set("Location", target)
					return resp, nil
				})}
				req, _ := http.NewRequest(method, "http://127.0.0.1:8010/local", strings.NewReader(`{"token":"test-only-credential"}`))
				req.Header.Set(companion.TokenHeader, "test-only-credential")
				resp, err := companion.NoRedirectClient(original).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if calls != 1 || resp.StatusCode != status || original.CheckRedirect != nil {
					t.Fatalf("%s %d to %s: calls=%d, status=%d or shared client modified", method, status, target, calls, resp.StatusCode)
				}
			}
		}
	}
	// Test the actual A2A call site, not just the client constructor.
	calls := 0
	c := companion.Client{BaseURL: "http://127.0.0.1:8010", Token: "test-only-credential", HTTP: &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := boundaryResponse(r, http.StatusTemporaryRedirect, "")
		resp.Header.Set("Location", "http://example.invalid/stolen")
		return resp, nil
	})}}
	if _, err := c.Me(context.Background()); err == nil || calls != 1 {
		t.Fatalf("A2A followed redirect or reported success: calls=%d, err=%v", calls, err)
	}
}

func TestConnectBoundaryAddresses(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1:8010", "http://example.invalid", "http://127.0.0.1.evil.invalid", "http://127.1", "http://2130706433", "http://127.0.0.1:8010#fragment", "http://127.0.0.1:8010?", "http://user@127.0.0.1:8010", "http://[::ffff:127.0.0.1]:8010"} {
		if _, err := companion.CheckLoopback(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	got, err := companion.CheckLoopback("http://localhost:8010/")
	if err != nil || got != "http://127.0.0.1:8010" {
		t.Fatalf("localhost was not pinned: %s, %v", got, err)
	}
}

func TestConnectBoundaryKeyEcho(t *testing.T) {
	const key = "Z9q7W8v6K5u4R3t2"
	for _, echoed := range []string{key, key[:4], key[len(key)-4:]} {
		for _, status := range []int{200, 401, 500} {
			c := companion.Client{BaseURL: "http://127.0.0.1:8010", Token: key, HTTP: &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
				body, _ := json.Marshal(map[string]string{"label": echoed, "detail": echoed})
				// Force escaped characters, to test screening after JSON decoding.
				encoded := strings.ReplaceAll(string(body), string(echoed[0]), fmt.Sprintf(`\u%04x`, echoed[0]))
				return boundaryResponse(r, status, encoded), nil
			})}}
			me, err := c.Me(context.Background())
			if strings.Contains(me.Label, echoed) || err != nil && strings.Contains(err.Error(), echoed) {
				t.Fatal("key material escaped in label/error")
			}
		}
	}
}

func TestConnectBoundaryPersistence(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	const key = "test-only-credential-for-persistence"
	hc := &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "127.0.0.1:8010" || r.Header.Get(companion.TokenHeader) != key {
			t.Fatal("wrong recipient or credential")
		}
		return boundaryResponse(r, 200, `{"label":"tmx"}`), nil
	})}
	if _, err := companion.Connect(context.Background(), p, "http://127.0.0.1:8010", key, hc, "test"); err != nil {
		t.Fatal(err)
	}
	if got, err := config.ReadSecret(p.CompanionAgentTokenFile()); err != nil || got != key {
		t.Fatal("key was not stored in the state directory")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(p.CompanionAgentTokenFile())
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("key is not owner-only")
		}
	}
	if err := filepath.WalkDir(p.Home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && path != p.CompanionAgentTokenFile() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), key) {
				t.Error("key appeared outside its secret file")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConnectBoundaryRedirectPreservesOldKey(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteSecret(p.CompanionAgentTokenFile(), "old-test-credential"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	hc := &http.Client{Transport: boundaryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := boundaryResponse(r, 307, "")
		resp.Header.Set("Location", "http://example.invalid/stolen")
		return resp, nil
	})}
	if _, err := companion.Connect(context.Background(), p, "http://127.0.0.1:8010", "new-test-credential", hc, "test"); err == nil || calls != 1 {
		t.Fatal("redirect accepted")
	}
	if got, err := config.ReadSecret(p.CompanionAgentTokenFile()); err != nil || got != "old-test-credential" {
		t.Fatal("redirect replaced the old key")
	}
}

func TestConnectBoundaryURLDiagnostic(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	const key = "Z9q7W8v6K5u4R3t2"
	hc := &http.Client{Transport: boundaryTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid address reached HTTP transport")
		return nil, nil
	})}
	_, err := companion.Connect(context.Background(), p, "http://127.0.0.1:8010/"+key, key, hc, "test")
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatal("invalid address accepted or key echoed")
	}
}
