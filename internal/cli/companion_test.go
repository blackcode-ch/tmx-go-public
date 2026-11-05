package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/companion"
	"github.com/blackcode-ch/tmx-go/internal/companion/companiontest"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

// tmxIn runs tmx with stdin.
func tmxIn(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runIO(args, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), code
}

// useCompanionAt configures Companion's address (where Companion's own run of tmx finds it).
func useCompanionAt(t *testing.T, url string) {
	t.Helper()
	p, err := config.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Companion = &config.CompanionConfig{URL: url}
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
}

// oneJSON decodes stdout as exactly one JSON object and nothing else.
func oneJSON(t *testing.T, out string) connectJSON {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var v connectJSON
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not JSON: %q (%v)", out, err)
	}
	if _, err := dec.Token(); err != io.EOF || strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout has more than one JSON object: %q", out)
	}
	return v
}

// The contract Companion's "Connect tmx" relies on: it runs `<entry.command> companion connect
// --token-stdin --json` with the entry's TMXG_HOME, writes "<key>\n" and reads one JSON object.
func TestCompanionConnectTokenStdinJSON(t *testing.T) {
	testCompanionProtocol(t)
	d := cliDaemon(t)
	const key = "Kq7v-agent-key-minted-by-companion-0123456"
	f := companiontest.New(t, key)
	useCompanionAt(t, f.URL)
	noKey := func(where, s string) {
		t.Helper()
		if strings.Contains(s, key) || strings.Contains(s, "wrong-key-9876") {
			t.Fatalf("the key shows in %s: %q", where, s)
		}
	}

	out, errs, code := tmxIn(t, key+"\n", "companion", "connect", "--token-stdin", "--json")
	if code != 0 || out != `{"ok":true,"label":"tmxg"}`+"\n" || errs != "" {
		t.Fatalf("success: %d %q %q", code, out, errs)
	}
	noKey("stdout", out)
	if got, _ := config.ReadSecret(filepath.Join(d.Home, "secrets", "companion-agent-token")); got != key {
		t.Fatal("the key was not stored")
	}
	var st companion.Status
	if err := client.New(d.URL, d.OwnerToken).Get(context.Background(), "/api/companion?probe=1", &st); err != nil || !st.Connected || st.KeyOK == nil || !*st.KeyOK || st.KeySetAt == 0 {
		t.Fatalf("status after connecting through the daemon: %+v %v", st, err)
	}

	// A key Companion refuses: one JSON object with Companion's sentence, exit 1, not stored.
	out, errs, code = tmxIn(t, "wrong-key-9876\n", "companion", "connect", "--token-stdin", "--json")
	if v := oneJSON(t, out); code != 1 || v.OK || !strings.Contains(v.Error, "does not accept this key") || v.Label != "" {
		t.Fatalf("refused key: %d %+v", code, v)
	}
	noKey("stdout", out)
	noKey("stderr", errs)
	if got, _ := config.ReadSecret(filepath.Join(d.Home, "secrets", "companion-agent-token")); got != key {
		t.Fatal("a refused key replaced the good one")
	}

	refusals := []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{"--token-stdin", "--json"}, "no key on stdin"},
		{" \n", []string{"--token-stdin", "--json"}, "no key on stdin"},
		{key + "\n", []string{"--token-stdin", "--json", "--token", key}, "--token is refused"},
		{"", []string{"--json"}, "needs the key on stdin"},
		{strings.Repeat("k", 2000) + "\n", []string{"--token-stdin", "--json"}, "not a key"},
		{strings.Repeat("k", 600) + "\n", []string{"--token-stdin", "--json"}, "not a key"},
		{key + "\n", []string{"--token-stdin", "--json", "--bogus"}, "invalid connect arguments"},
	}
	for _, r := range refusals {
		out, errs, code := tmxIn(t, r.stdin, append([]string{"companion", "connect"}, r.args...)...)
		if v := oneJSON(t, out); code != 1 || v.OK || !strings.Contains(v.Error, r.want) {
			t.Errorf("%q %v: %d %+v, want an error with %q", r.stdin, r.args, code, v, r.want)
		}
		noKey("stdout", out)
		noKey("stderr", errs)
	}
	// Without --json the same refusals are usage errors for people.
	if out, errs, code := tmxIn(t, "", "companion", "connect", "--token-stdin"); code != 2 || out != "" || !strings.Contains(errs, "no key on stdin") {
		t.Fatalf("empty stdin: %d %q %q", code, out, errs)
	}
	if _, errs, code := tmxIn(t, key, "companion", "connect", "--token-stdin", "--token", key); code != 2 || !strings.Contains(errs, "--token is refused") {
		t.Fatalf("both: %d %q", code, errs)
	}
	noKey("stderr", errs)
	// The plain --token-stdin prints for people (and never the key).
	if out, _, code := tmxIn(t, key+"\r\n", "companion", "connect", "--token-stdin"); code != 0 || !strings.Contains(out, "Connected: Companion at "+f.URL) {
		t.Fatalf("plain --token-stdin: %d %q", code, out)
	} else {
		noKey("stdout", out)
	}
}

// Without a daemon the key is checked and stored here.
func TestCompanionConnectTokenStdinNoDaemon(t *testing.T) {
	testCompanionProtocol(t)
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(companion.EnvAIOSHome, filepath.Join(home, "aios"))
	t.Setenv("TMXG_COMPANION_DATA", filepath.Join(home, "companion-data"))
	p, _ := config.Resolve()
	l, _ := net.Listen("tcp", "127.0.0.1:0") // a port nobody serves once closed
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := config.WriteDaemonInfo(p, config.DaemonInfo{Port: port}); err != nil {
		t.Fatal(err)
	}
	const key = "local-key-from-companion"
	f := companiontest.New(t, key)
	useCompanionAt(t, f.URL)
	out, errs, code := tmxIn(t, key+"\n", "companion", "connect", "--token-stdin", "--json")
	if code != 1 || (!strings.Contains(out+errs, "listener identity") && !strings.Contains(out+errs, "diagnostic withheld")) {
		t.Fatalf("unverified listener was not refused: %d", code)
	}
	if _, err := os.Stat(p.CompanionAgentTokenFile()); !os.IsNotExist(err) {
		t.Fatal("unverified key was persisted")
	}

}

// A caller that keeps stdin open after the line does not hang the read.
func TestReadKeyStopsAtTheLine(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	go func() { _, _ = w.Write([]byte("abc-key\n")) }()
	got := make(chan string, 1)
	go func() { k, _ := readKey(r); got <- k }()
	select {
	case k := <-got:
		if k != "abc-key" {
			t.Fatalf("key %q", k)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readKey waited for the end of stdin")
	}
}

// `tmx companion connect` without a key: installs into Companion, opens Companion's "Connect
// tmx?" (through the daemon; a fake opener here) and waits for the key Companion hands over.
func TestCompanionConnectOneClick(t *testing.T) {
	testCompanionProtocol(t)
	d := cliDaemon(t)
	wait, poll := connectWait, connectPoll
	connectWait, connectPoll = 2*time.Second, 20*time.Millisecond
	t.Cleanup(func() { connectWait, connectPoll = wait, poll })
	const key = "one-click-key-minted-by-companion"
	f := companiontest.New(t, key)
	useCompanionAt(t, f.URL)

	if _, errs, code := tmx(t, "companion", "connect"); code != 1 || !strings.Contains(errs, "has not been set up") || len(d.Opened()) != 0 {
		t.Fatalf("without Companion: %d %q, opened %v", code, errs, d.Opened())
	}
	if _, errs, code := tmx(t, "companion", "connect", "--url", f.URL); code != 2 || !strings.Contains(errs, "--url goes with a key") {
		t.Fatalf("--url without a key: %d %q", code, errs)
	}
	if err := os.MkdirAll(d.AIOSHome, 0o755); err != nil {
		t.Fatal(err)
	}
	// Companion, once the owner clicks Connect: runs the entry's command with its env.
	child := make(chan string, 1)
	d.OnOpen(func(url string) error {
		if url != companion.DeepLink {
			return errors.New("unexpected link " + url)
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			out, _, code := tmxIn(t, key+"\n", "companion", "connect", "--token-stdin", "--json")
			if code != 0 {
				out = "exit " + out
			}
			child <- out
		}()
		return nil
	})
	out, errs, code := tmx(t, "companion", "connect")
	if code != 0 || !strings.Contains(out, "Installed tmx into Companion.") ||
		!strings.Contains(out, "Companion is asking you to connect tmx: click Connect there") ||
		!strings.Contains(out, "Connected: Companion at "+f.URL+" accepts the key") {
		t.Fatalf("one click: %d %q %q", code, out, errs)
	}
	if got := <-child; got != `{"ok":true,"label":"tmxg"}`+"\n" {
		t.Fatalf("Companion's run of tmx: %q", got)
	}
	if strings.Contains(out+errs, key) {
		t.Fatal("the key was printed")
	}
	if got := d.Opened(); len(got) != 1 || got[0] != "aios://connect/tmx" {
		t.Fatalf("opened %v", got)
	}
	var e companion.Entry
	b, _ := os.ReadFile(filepath.Join(d.AIOSHome, "mcps", "tmxg", "mcp.json"))
	if err := json.Unmarshal(b, &e); err != nil || e.ManagedBy != "tmxg" || e.Env["TMXG_HOME"] != d.Home {
		t.Fatalf("entry: %+v %v", e, err)
	}

	// Connecting again replaces the key; nothing installed this time.
	d.OnOpen(func(string) error {
		go func() {
			time.Sleep(100 * time.Millisecond)
			_, _, _ = tmxIn(t, key+"\n", "companion", "connect", "--token-stdin", "--json")
		}()
		return nil
	})
	if out, errs, code := tmx(t, "companion", "connect"); code != 0 || strings.Contains(out, "Installed") || !strings.Contains(out, "Connected") {
		t.Fatalf("again: %d %q %q", code, out, errs)
	}
	// Companion did not open.
	d.OnOpen(func(string) error {
		return errors.New("Companion did not open: no app on this computer opens Companion's links (aios://)")
	})
	if _, errs, code := tmx(t, "companion", "connect"); code != 1 || !strings.Contains(errs, "did not open") {
		t.Fatalf("not opened: %d %q", code, errs)
	}
	// Nobody clicks Connect.
	d.OnOpen(nil)
	if _, errs, code := tmx(t, "companion", "connect"); code != 1 || !strings.Contains(errs, "has not given tmx a key") {
		t.Fatalf("no click: %d %q", code, errs)
	}
}

func TestCompanionCommands(t *testing.T) {
	testCompanionProtocol(t)
	d := cliDaemon(t) // TMXG_HOME and TMXG_AIOS_HOME are temp dirs (never ~/.aios)
	aios := os.Getenv(companion.EnvAIOSHome)
	if aios == "" || !strings.HasPrefix(aios, d.Home) {
		t.Fatalf("AIOS home not a temp dir: %q", aios)
	}
	if _, errs, code := tmx(t, "companion", "install"); code != 1 || !strings.Contains(errs, "has not been set up") {
		t.Fatalf("install without Companion: %d %s", code, errs)
	}
	if err := os.MkdirAll(aios, 0o755); err != nil {
		t.Fatal(err)
	}
	out, errs, code := tmx(t, "companion", "install")
	if code != 0 || !strings.Contains(out, "Installed into Companion") {
		t.Fatalf("install: %d %s %s", code, out, errs)
	}
	var e companion.Entry
	b, _ := os.ReadFile(filepath.Join(aios, "mcps", "tmxg", "mcp.json"))
	if err := json.Unmarshal(b, &e); err != nil || e.Command == "" || strings.Join(e.Args, " ") != "mcp --companion" || e.Env["TMXG_HOME"] != d.Home {
		t.Fatalf("entry: %+v %v", e, err)
	}
	if tok, _ := config.ReadSecret(filepath.Join(d.Home, "secrets", "companion-token")); len(tok) < 32 {
		t.Fatal("no link token after install")
	}
	out, _, code = tmx(t, "companion", "status")
	if code != 0 || !strings.Contains(out, "installed: ") || !strings.Contains(out, "link token: yes") || !strings.Contains(out, "agent key set: no") {
		t.Fatalf("status: %d %s", code, out)
	}
	if _, errs, code := tmxIn(t, "k1\n", "companion", "connect", "--token-stdin", "--url", "http://10.0.0.2:8010"); code != 1 || !strings.Contains(errs, "on this machine") {
		t.Fatalf("connect to a LAN address: %d %s", code, errs)
	}
	if _, _, code := tmxIn(t, "k1\n", "companion", "connect", "--token-stdin", "--url", "http://127.0.0.1:8099"); code != 0 {
		t.Fatalf("connect: %d", code)
	}
	if out, _, _ := tmx(t, "companion", "status"); !strings.Contains(out, "agent key set: yes") || !strings.Contains(out, "127.0.0.1:8099 (config)") {
		t.Fatalf("status after connect: %s", out)
	}
	if out, _, code := tmx(t, "companion", "tasks"); code != 0 || !strings.Contains(out, "No tasks") {
		t.Fatalf("tasks: %d %s", code, out)
	}
	// The owner's message to Companion (queued: nothing answers at 8099).
	if out, errs, code := tmx(t, "companion", "message", "How", "are", "things?"); code != 0 || !strings.Contains(out, ": queued.") {
		t.Fatalf("message: %d %s %s", code, out, errs)
	}
	if out, _, _ := tmx(t, "companion", "tasks"); !strings.Contains(out, "message: How are things?") {
		t.Fatalf("tasks after a message: %s", out)
	}
	if _, errs, code := tmx(t, "companion", "message", "--reply-to", "nope", "x"); code != 1 || !strings.Contains(errs, "not waiting for an answer") {
		t.Fatalf("answer to nothing: %d %s", code, errs)
	}
	if _, _, code := tmx(t, "companion", "token", "--rotate"); code != 0 {
		t.Fatalf("rotate: %d", code)
	}
	if out, _, code := tmx(t, "companion", "uninstall"); code != 0 || !strings.Contains(out, "Removed tmx from Companion") {
		t.Fatalf("uninstall: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(aios, "mcps", "tmxg")); !os.IsNotExist(err) {
		t.Fatal("entry left behind")
	}
	if _, err := os.Stat(filepath.Join(d.Home, "secrets", "companion-token")); !os.IsNotExist(err) {
		t.Fatal("link token not revoked")
	}
}

// Exercise the dormant protocol implementation independently of the trial entry gate.
// TestTrialCompanionCommandRefused covers the actual fleet command.
func testCompanionProtocol(t *testing.T) {
	t.Helper()
	old := commands["companion"]
	commands["companion"] = companionCommand
	t.Cleanup(func() { commands["companion"] = old })
}
