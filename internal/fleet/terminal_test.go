package fleet

import (
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/fake"
)

// Fleet control's type and keys (DESIGN.md "Fleet control"): the owner types raw input
// anywhere; a head or lead only into its own subtree's harness TUIs, its text delivered like
// mail (Process.Send, prefixed, cleaned, never a / or ! command) and its keys limited to esc
// and ctrl+c. The runner's own Write is the race-free gate for menus (internal/runner/term's
// guardedWrite); fake.Process.SetPending/PendAfter stand in for it here.

// typeTextLead sets up a project (alpha-lead) with a terminal-mode worker launched by the
// lead, and returns the lead's Caller and the worker's fake process.
func typeTextLead(t *testing.T) (e *env, f *Fleet, lc Caller, worker model.Agent, p *fake.Process) {
	t.Helper()
	e = newEnv(t)
	f = e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	lc = callerFor(t, f, e.chat, lead.ID)
	w, err := f.LaunchAgent(lc, LaunchRequest{Name: "w1", Mode: model.ModeTerminal})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	p = e.term.Last(w.ID)
	if p == nil {
		t.Fatal("worker not started in terminal mode")
	}
	p.State(model.StateIdle)
	eventually(t, "worker idle", func() bool { return mustAgent(t, f, "w1").State == model.StateIdle })
	return e, f, lc, w, p
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func TestAgentTypedTextIsDeliveredLikeMail(t *testing.T) {
	_, f, lc, w, p := typeTextLead(t)

	// The lead's text goes through Send, prefixed with who typed it; no raw bytes.
	if err := f.TypeText(lc, "w1", "  run the tests\r\nthen report  ", true); err != nil {
		t.Fatalf("lead typed: %v", err)
	}
	if got := p.Sent(); len(got) != 1 || got[0] != "[message from alpha-lead (lead)]\n│ run the tests\n│ then report" {
		t.Fatalf("sent %q", got)
	}
	if got := p.Written(); len(got) != 0 {
		t.Fatalf("an agent's text reached the pty as raw input: %q", got)
	}
	// It is in the worker's timeline, with who typed it.
	tl, _ := f.Timeline(w.ID, 0, 50)
	found := false
	for _, e := range tl {
		if e.Kind == model.EntryMessageIn && e.Meta["typed"] == true && e.Meta["from_name"] == "alpha-lead" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no timeline entry for the typed text: %+v", tl)
	}

	// Esc and other control bytes are dropped: they cannot end the paste and become keys.
	if err := f.TypeText(lc, "w1", "a\x1b[201~\x1b[Zb\x03\x7f\u009bc", true); err != nil {
		t.Fatalf("lead typed: %v", err)
	}
	if got := p.Sent(); got[1] != "[message from alpha-lead (lead)]\n│ a[201~[Zbc" {
		t.Fatalf("control bytes survived: %q", got[1])
	}

	// Refused: a slash command or shell escape, a draft, too long, nothing left.
	for _, c := range []struct {
		text   string
		submit bool
		kind   Kind
	}{
		{"/permissions", true, KindForbidden},
		{"  !rm -rf ~", true, KindForbidden},
		{"\x1b/approvals", true, KindForbidden}, // the Esc is dropped first: still a command
		{"draft", false, KindInvalid},
		{strings.Repeat("x", MaxTypedBytes+1), true, KindInvalid},
		{"\x1b\x1b\r\n", true, KindInvalid},
	} {
		if err := f.TypeText(lc, "w1", c.text, c.submit); KindOf(err) != c.kind {
			t.Fatalf("type %q (submit %v): err=%v, want %v", cut(c.text, 20), c.submit, err, c.kind)
		}
	}
	if got := p.Sent(); len(got) != 2 {
		t.Fatalf("a refused text was sent: %q", got)
	}

	// The owner types raw input as at the keyboard: anything, and no prefix.
	if err := f.TypeText(OwnerCaller, "w1", "/permissions", true); err != nil {
		t.Fatalf("owner refused: %v", err)
	}
	if got := string(p.Written()); got != "/permissions\r" {
		t.Fatalf("owner wrote %q", got)
	}
	if err := f.TypeText(OwnerCaller, "w1", "draft", false); err != nil {
		t.Fatalf("owner draft refused: %v", err)
	}

	// The owner just typed: an agent's text waits (TypingQuiet), as mail does.
	if err := f.TypeText(lc, "w1", "hello", true); KindOf(err) != KindConflict {
		t.Fatalf("lead typed over the owner's typing: %v", err)
	}
	f.mu.Lock()
	f.agents[w.ID].humanInputAt = time.Now().Add(-TypingQuiet - time.Second)
	f.mu.Unlock()
	if err := f.TypeText(lc, "w1", "hello", true); err != nil {
		t.Fatalf("lead typed after the quiet period: %v", err)
	}
}

func TestAgentKeysAreInterruptsOnly(t *testing.T) {
	_, f, lc, _, p := typeTextLead(t)

	if err := f.SendKeys(lc, "w1", "esc ctrl+c"); err != nil {
		t.Fatalf("lead pressed esc ctrl+c: %v", err)
	}
	if got := string(p.Written()); got != "\x1b\x03" {
		t.Fatalf("written %q", got)
	}
	// Every other key can answer a menu, cycle the permission mode or recall a command.
	for _, k := range []string{"enter", "y", "1", "up", "down", "shift+tab", "tab", "ctrl+d", "esc enter"} {
		if err := f.SendKeys(lc, "w1", k); KindOf(err) != KindForbidden {
			t.Fatalf("lead pressed %q: err=%v", k, err)
		}
	}
	if got := string(p.Written()); got != "\x1b\x03" {
		t.Fatalf("a refused key reached the pty (a sequence is checked before any key is sent): %q", got)
	}
	// The owner presses anything.
	if err := f.SendKeys(OwnerCaller, "w1", "down enter shift+tab"); err != nil {
		t.Fatalf("owner keys: %v", err)
	}
	if got := string(p.Written()); got != "\x1b\x03\x1b[B\r\x1b[Z" {
		t.Fatalf("written %q", got)
	}
}

func TestAgentKeysRefusedWhileAMenuWaits(t *testing.T) {
	_, f, lc, _, p := typeTextLead(t)

	// A menu or approval on the runner (the watcher's own doing, mirrored by SetPending): the
	// runner's Write refuses the agent's key.
	p.SetPending(true)
	if err := f.SendKeys(lc, "w1", "esc"); KindOf(err) != KindForbidden {
		t.Fatalf("lead pressed a key into a pending menu: err=%v", err)
	}
	if got := p.Written(); len(got) != 0 {
		t.Fatalf("a refused key reached the pty: %q", got)
	}
	// The owner answers it.
	if err := f.SendKeys(OwnerCaller, "w1", "enter"); err != nil {
		t.Fatalf("owner refused: %v", err)
	}

	// A menu appearing mid-sequence: the keys after it are refused.
	p.PendAfter(1)
	if err := f.SendKeys(lc, "w1", "esc ctrl+c esc"); KindOf(err) != KindForbidden {
		t.Fatalf("multi-key sequence was not refused: err=%v", err)
	}
	if got := string(p.Written()); got != "\r\x1b" {
		t.Fatalf("keys after the menu appeared reached the pty: %q", got)
	}
}

func TestShellIsTheOwnersAlone(t *testing.T) {
	e, f, lc, _, _ := typeTextLead(t)
	lead := mustAgent(t, f, "alpha-lead")

	// An agent cannot launch a shell.
	if _, err := f.LaunchAgent(lc, LaunchRequest{Name: "sh-agent", Harness: model.HarnessShell}); KindOf(err) != KindForbidden {
		t.Fatalf("lead launched a shell: %v", err)
	}
	if _, err := f.LaunchAgent(lc, LaunchRequest{Name: "sh-agent2", Harness: model.HarnessShell, Mode: model.ModeTerminal}); KindOf(err) != KindForbidden {
		t.Fatalf("lead launched a terminal shell: %v", err)
	}

	// The owner may, even below the lead; the lead still cannot type or press keys into it.
	sh, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "sh", Harness: model.HarnessShell, ParentID: lead.ID})
	if err != nil {
		t.Fatalf("owner launched a shell: %v", err)
	}
	changedRole := "agent-controlled shell"
	if _, err := f.UpdateAgent(lc, sh.ID, AgentPatch{Role: &changedRole}); KindOf(err) != KindForbidden {
		t.Fatalf("lead edited an owner shell: %v", err)
	}
	if got := mustAgent(t, f, sh.ID); got.Role != sh.Role || got.Harness != model.HarnessShell {
		t.Fatal("refused agent edit changed the owner shell")
	}
	p := e.term.Last(sh.ID)
	p.State(model.StateIdle)
	eventually(t, "shell idle", func() bool { return mustAgent(t, f, "sh").State == model.StateIdle })
	if err := f.TypeText(lc, "sh", "echo pwned", true); KindOf(err) != KindForbidden {
		t.Fatalf("lead typed into a shell: %v", err)
	}
	if err := f.SendKeys(lc, "sh", "ctrl+c"); KindOf(err) != KindForbidden {
		t.Fatalf("lead pressed a key in a shell: %v", err)
	}
	if got := p.Written(); len(got) != 0 || len(p.Sent()) != 0 {
		t.Fatalf("a refused input reached the shell: %q %q", got, p.Sent())
	}
	if err := f.TypeText(OwnerCaller, "sh", "ls", true); err != nil {
		t.Fatalf("owner typed into the shell: %v", err)
	}
	if got := string(p.Written()); got != "ls\r" {
		t.Fatalf("written %q", got)
	}
}

func TestAgentTypeIntoChatAgentRefused(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	lc := callerFor(t, f, e.chat, lead.ID)
	w, _ := f.LaunchAgent(lc, LaunchRequest{Name: "w1"})
	e.chat.Last(w.ID).State(model.StateIdle)
	if err := f.TypeText(lc, "w1", "hello", true); KindOf(err) != KindInvalid {
		t.Fatalf("lead typed into a chat agent: %v", err)
	}
	if err := f.SendKeys(lc, "w1", "esc"); KindOf(err) != KindInvalid {
		t.Fatalf("lead pressed a key in a chat agent: %v", err)
	}
	if got := e.chat.Last(w.ID).Sent(); len(got) != 0 {
		t.Fatalf("sent %q", got)
	}
}

// A full peer (another fleet machine, authenticated by its pair token) is owner-equivalent for
// agent operations only (launch/screen/type/keys/restart; tmx-lead, 3 Nov interop P0): it types
// and presses any key, like the owner, never mail-relay-restricted like a head or lead agent -
// but NEVER with the owner's unguarded strength (go-reviewer-o, #161 review, machine
// protection): Write(owner=true) is the literal owner's bypass of the pending-approval/menu
// gate and a shell's owner-only refusal, reserved for a human at the real keyboard. A peer's
// write still goes through that race-free gate, and a peer never reaches a shell at all.
// It is NEVER owner for owner-only routes (RemoveAgent here stands in for the ~50 c.Owner checks
// and every API s.ownerOnly route, all gated on the literal Owner bool, untouched by this change).
func TestPeerIsOwnerEquivalentForAgentOpsOnlyNeverOwnerOnly(t *testing.T) {
	_, f, _, w, p := typeTextLead(t)
	peer := PeerCaller("hosta")

	if peer.Owner {
		t.Fatal("PeerCaller must never set Owner")
	}

	if err := f.TypeText(peer, "w1", "/permissions", true); err != nil {
		t.Fatalf("peer typed: %v", err)
	}
	if got := string(p.Written()); got != "/permissions\r" {
		t.Fatalf("peer should type raw: got %q", got)
	}

	if err := f.SendKeys(peer, "w1", "down enter shift+tab"); err != nil {
		t.Fatalf("peer pressed keys: %v", err)
	}
	if got := string(p.Written()); got != "/permissions\r\x1b[B\r\x1b[Z" {
		t.Fatalf("peer should press any key: got %q", got)
	}

	// A peer's write is refused, race-free, the same as the owner's own would be - it never
	// bypasses a pending menu or approval (go-reviewer-o's must-fix).
	p.SetPending(true)
	if err := f.TypeText(peer, "w1", "x", true); KindOf(err) != KindForbidden {
		t.Fatalf("peer typed through a pending approval: %v", err)
	}
	if err := f.SendKeys(peer, "w1", "enter"); KindOf(err) != KindForbidden {
		t.Fatalf("peer pressed a key through a pending approval: %v", err)
	}
	p.SetPending(false)

	p.SetReplay([]byte("hello from the terminal"))
	if _, err := f.Screen(peer, "w1", 0); err != nil {
		t.Fatalf("peer screen: %v", err)
	}

	if _, err := f.RestartAgentWith(peer, "w1", RestartOptions{}); err != nil {
		t.Fatalf("peer restart: %v", err)
	}

	// A peer never reaches a shell at all: type, keys, screen.
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "owner-shell", Harness: model.HarnessShell}); err != nil {
		t.Fatalf("launch shell: %v", err)
	}
	if err := f.TypeText(peer, "owner-shell", "ls", true); KindOf(err) != KindForbidden {
		t.Fatalf("peer typed into a shell: %v", err)
	}
	if err := f.SendKeys(peer, "owner-shell", "enter"); KindOf(err) != KindForbidden {
		t.Fatalf("peer pressed a key in a shell: %v", err)
	}
	if _, err := f.Screen(peer, "owner-shell", 0); KindOf(err) != KindForbidden {
		t.Fatalf("peer screened a shell: %v", err)
	}

	// Launch: a peer launches like the owner (no CanLaunch/tier restriction - callerRecLocked
	// returns nil for a peer, same as the owner).
	if _, err := f.LaunchAgent(peer, LaunchRequest{Name: "peer-launched"}); err != nil {
		t.Fatalf("peer launch: %v", err)
	}

	// The hard boundary (tmx-lead, 3 Nov): never owner for an owner-only route.
	if err := f.RemoveAgent(peer, w.ID); KindOf(err) != KindForbidden {
		t.Fatalf("peer removed an agent (owner-only route): err=%v", err)
	}
}
