package cli

import (
	"bytes"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func TestParseInterleaved(t *testing.T) {
	fs := newFlags("run")
	p := fs.String("project", "", "")
	m := fs.String("model", "", "")
	pos, err := parse(fs, []string{"w1", "--project", "alpha", "extra", "--model=x", "--", "--not-a-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if *p != "alpha" || *m != "x" || strings.Join(pos, ",") != "w1,extra,--not-a-flag" {
		t.Fatalf("%q %q %q", *p, *m, pos)
	}
	if _, err := parse(newFlags("x"), []string{"--nope"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

// cliDaemon starts a test daemon and points the CLI's state dir at it.
func cliDaemon(t *testing.T) *apitest.Daemon {
	t.Helper()
	d := apitest.New(t) // sets TMXG_HOME and writes the owner token there
	u, _ := url.Parse(d.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	n, _ := strconv.Atoi(port)
	p, _ := config.Resolve()
	if err := config.WriteDaemonInfo(p, config.DaemonInfo{PID: 1, Port: n}); err != nil {
		t.Fatal(err)
	}
	return d
}

func tmx(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestCommandsAgainstDaemon(t *testing.T) {
	d := cliDaemon(t)
	if out, _, code := tmx(t, "status"); code != 0 || !strings.Contains(out, "testbox") {
		t.Fatalf("status: %d %s", code, out)
	}
	if _, _, code := tmx(t, "project", "new", "Unsupported", "--harness", "pi"); code == 0 {
		t.Fatal("project with unsupported guard adapter accepted")
	}
	if out, errs, code := tmx(t, "project", "new", "Web", "Shop", "--brief", "sell things", "--harness", "claude", "--model", "claude-sonnet-5"); code != 0 || !strings.Contains(out, "web-shop") {
		t.Fatalf("project new: %d %s %s", code, out, errs)
	}
	lead, err := d.Fleet.Agent("web-shop-lead")
	if err != nil || lead.Harness != "claude" || lead.Model != "claude-sonnet-5" {
		t.Fatalf("project lead harness = %q, err %v", lead.Harness, err)
	}
	if out, _, _ := tmx(t, "project", "ls"); !strings.Contains(out, "web-shop-lead") {
		t.Fatalf("project ls: %s", out)
	}
	if out, errs, code := tmx(t, "run", "w1", "--project", "web-shop", "--prompt", "go"); code != 0 || !strings.Contains(out, "claude-sonnet-5") {
		t.Fatalf("run: %d %s %s", code, out, errs)
	}
	if _, errs, code := tmx(t, "run", "w2", "--project", "web-shop", "--model", "unsupported-model"); code != 1 || !strings.Contains(errs, "not allowed") {
		t.Fatalf("run wrong model: %d %s", code, errs)
	}
	// --account reaches the launch: this test daemon has no second account.
	if _, errs, code := tmx(t, "run", "w3", "--project", "web-shop", "--account", "nope"); code != 1 || !strings.Contains(errs, `no account "nope"`) {
		t.Fatalf("run unknown account: %d %s", code, errs)
	}
	out, _, _ := tmx(t, "ls")
	for _, want := range []string{"head  head", "project Web Shop (web-shop)", "web-shop-lead  lead", "w1  worker"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls lacks %q:\n%s", want, out)
		}
	}
	if out, _, code := tmx(t, "send", "w1", "hello", "there"); code != 0 || !strings.Contains(out, "delivered") {
		t.Fatalf("send: %d %s", code, out)
	}
	att := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(att, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, errs, code := tmx(t, "send", "w1", "-f", att, "see", "-x", "file"); code != 0 || !strings.Contains(out, "message") {
		t.Fatalf("send -f: %d %s %s", code, out, errs)
	}
	if sent := d.Proc(t, "w1").Sent(); len(sent) == 0 || !strings.Contains(strings.Join(sent, "\n"), "see -x file\n\nAttached files (open them from these paths):\n- notes.txt (text/plain, 2 B): ") {
		t.Fatalf("send -f delivered: %q", sent)
	}
	if _, _, code := tmx(t, "send", "w1", "-f", filepath.Join(t.TempDir(), "missing")); code == 0 {
		t.Fatal("send -f missing file succeeded")
	}
	if out, _, code := tmx(t, "stop", "w1"); code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("stop: %d %s", code, out)
	}
	if out, _, code := tmx(t, "start", "w1"); code != 0 || !strings.Contains(out, "starting") {
		t.Fatalf("start: %d %s", code, out)
	}

	d.Proc(t, "w1").Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "r", Tool: "Bash", Description: "run tests"}})
	var id string
	for i := 0; i < 200 && id == ""; i++ {
		pend, _ := d.Fleet.Approvals(model.ApprovalPending)
		if len(pend) > 0 {
			id = pend[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	if out, _, _ := tmx(t, "approvals"); !strings.Contains(out, "w1 wants Bash: run tests") {
		t.Fatalf("approvals: %s", out)
	}
	if out, _, code := tmx(t, "deny", id, "--message", "no"); code != 0 || !strings.Contains(out, "denied") {
		t.Fatalf("deny: %d %s", code, out)
	}
	if _, errs, code := tmx(t, "approve", id); code != 1 || !strings.Contains(errs, "already resolved") {
		t.Fatalf("approve twice: %d %s", code, errs)
	}

	if out, _, _ := tmx(t, "open"); !strings.Contains(out, "/#token="+d.OwnerToken) {
		t.Fatalf("open: %s", out)
	}
	if _, _, code := tmx(t, "project", "rm", "web-shop"); code != 2 {
		t.Fatal("rm without --yes")
	}
	if _, _, code := tmx(t, "project", "rm", "web-shop", "--yes"); code != 0 {
		t.Fatal("project rm")
	}
	if _, _, code := tmx(t, "bogus"); code != 2 {
		t.Fatal("unknown command")
	}
}

func TestStatusWhenNoDaemon(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	p, _ := config.Resolve()
	l, _ := net.Listen("tcp", "127.0.0.1:0") // a port nobody serves once closed
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	config.WriteDaemonInfo(p, config.DaemonInfo{Port: port})
	if out, _, code := tmx(t, "status"); code != 1 || !strings.Contains(out, "not running") {
		t.Fatalf("status: %d %s", code, out)
	}
	if _, errs, code := tmx(t, "ls"); code != 1 || !strings.Contains(errs, "tmx up") {
		t.Fatalf("ls: %d %s", code, errs)
	}
}

// Opened from Finder the app gets no arguments (or -psn_...): that opens the window (21 Oct:
// it printed the help and quit). The headless build keeps printing the help.
func TestLaunchArgs(t *testing.T) {
	cases := []struct {
		in      []string
		desktop bool
		want    string
	}{
		{nil, true, "app"},
		{[]string{"-psn_0_12345"}, true, "app"},
		{nil, false, ""},
		{[]string{"ls"}, true, "ls"},
		{[]string{"-psn_0_1", "ls"}, true, "ls"},
	}
	for _, c := range cases {
		got := strings.Join(launchArgs(c.in, c.desktop), " ")
		if got != c.want {
			t.Errorf("launchArgs(%q, %v) = %q, want %q", c.in, c.desktop, got, c.want)
		}
	}
}

func TestHarnessDiscoverAdoptCommands(t *testing.T) {
	cliDaemon(t)
	out, _, code := tmx(t, "harnesses")
	if code != 0 || !strings.Contains(out, "codex") || !strings.Contains(out, "chat, terminal") {
		t.Fatalf("%d %q", code, out)
	}
	if _, errs, code := tmx(t, "discover"); code != 1 || !strings.Contains(errs, "trial profile refuses discovery") {
		t.Fatalf("discover: %d %q", code, errs)
	}
	if _, errs, code := tmx(t, "adopt", "x"); code != 2 || !strings.Contains(errs, "not a process id") {
		t.Fatalf("%d %q", code, errs)
	}
	if _, errs, code := tmx(t, "adopt", "1", "--yes"); code != 1 || !strings.Contains(errs, "trial profile refuses adoption") {
		t.Fatalf("%d %q", code, errs)
	}
}
