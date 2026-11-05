package gate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// #187: the owner PIN answers every confirmation; the OS prompt is an optional shortcut at the
// machine. The owner must never be locked out, so every path keeps at least one way in.

// gateWith starts a gate on gate.json content (nil = no file: a new install).
func gateWith(t *testing.T, f *Fake, file []byte, pin *bool) (*Gate, config.Paths) {
	t.Helper()
	p := config.Paths{Home: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if file != nil {
		if err := os.WriteFile(p.SecretsDir()+"/gate.json", file, 0600); err != nil {
			t.Fatal(err)
		}
	}
	g := New(p, f, nil, FileMarker(p.Home+"/test-marker"))
	g.SetPINCheck(func() bool { return *pin })
	return g, p
}

// waitForPrompt blocks the OS prompt until its context ends (cancelled by a PIN answer).
func waitForPrompt(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func need(t *testing.T, g *Gate, a Action) Confirmation {
	t.Helper()
	var n *NeedConfirmation
	if err := g.Require(a, ""); !errors.As(err, &n) {
		t.Fatalf("Require = %v, want NeedConfirmation", err)
	}
	return n.Confirmation
}

func settled(t *testing.T, g *Gate, id string) Confirmation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _ := g.Wait(ctx, id)
	return c
}

// An install from before #187 (enabled, Windows Hello, no hello_shortcut): after the upgrade
// the PIN confirms at the machine AND Windows Hello still does.
func TestUpgradeKeepsHelloAndAddsPIN(t *testing.T) {
	pin := true
	f := &Fake{Answer: waitForPrompt}
	g, _ := gateWith(t, f, []byte(`{"enabled":true,"method":"windows-hello","changed_at":1}`), &pin)
	if !g.Enabled() || !g.HelloShortcut() {
		t.Fatalf("upgrade changed the gate: enabled=%v hello=%v", g.Enabled(), g.HelloShortcut())
	}
	st := g.Status()
	if !st.Available || !st.PINSet || !st.HelloShortcut || st.Method == "pin" {
		t.Fatalf("status: %+v", st)
	}

	a := Action{Reason: "delete x", Key: "k", Caller: "ui-1", Owner: true}
	c := need(t, g, a)
	if !c.PIN || !c.Hello || c.Method != "pin" {
		t.Fatalf("confirmation ways: %+v", c)
	}
	if err := g.ConfirmWithPIN(c.ID, "someone-else"); !errors.Is(err, ErrNoSuchConfirmation) {
		t.Fatalf("another caller answered: %v", err)
	}
	if err := g.ConfirmWithPIN(c.ID, "ui-1"); err != nil {
		t.Fatal(err)
	}
	if got := settled(t, g, c.ID); got.State != StateConfirmed {
		t.Fatalf("after the PIN: %+v", got)
	}
	if err := g.ConfirmWithPIN(c.ID, "ui-1"); !errors.Is(err, ErrNoSuchConfirmation) {
		t.Fatal("a confirmation was answered twice")
	}
	if err := g.Require(a, c.ID); err != nil {
		t.Fatalf("the PIN proof was refused: %v", err)
	}
	var again *NeedConfirmation
	if err := g.Require(a, c.ID); !errors.As(err, &again) {
		t.Fatalf("a PIN proof must be single use: %v", err)
	}
	g.Cancel(again.Confirmation.ID) // the re-ask it opened
	settled(t, g, again.Confirmation.ID)

	// Windows Hello still answers on its own.
	f.Answer = nil
	b := Action{Reason: "delete y", Key: "k2", Caller: "ui-1", Owner: true}
	c2 := need(t, g, b)
	if got := settled(t, g, c2.ID); got.State != StateConfirmed {
		t.Fatalf("Windows Hello after the upgrade: %+v", got)
	}
}

// Without a PIN the OS prompt is the only way in: it always opens, whatever the shortcut says.
func TestNoPINAlwaysOffersHello(t *testing.T) {
	pin := false
	f := &Fake{}
	g, _ := gateWith(t, f, []byte(`{"enabled":true,"hello_shortcut":false}`), &pin)
	c := need(t, g, Action{Reason: "delete x", Key: "k", Caller: "ui-1"})
	if c.PIN || !c.Hello {
		t.Fatalf("no PIN must mean the OS prompt only: %+v", c)
	}
	if err := g.ConfirmWithPIN(c.ID, "ui-1"); !errors.Is(err, ErrNoSuchConfirmation) {
		t.Fatal("a PIN answered although none is set")
	}
	if got := settled(t, g, c.ID); got.State != StateConfirmed {
		t.Fatalf("OS prompt: %+v", got)
	}
	if err := g.SetHelloShortcut(false); !errors.Is(err, ErrNeedPIN) {
		t.Fatalf("switched Windows Hello off without a PIN: %v", err)
	}
	f.SetUnavailable(errors.New("no Windows Hello"))
	if err := g.Require(Action{Reason: "delete z", Key: "k3"}, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no PIN and no OS prompt: %v", err)
	}
}

// Shortcut off: no OS prompt at all; the PIN answers. OS prompt unavailable: the PIN still does.
func TestPINOnlyAndHelloUnavailable(t *testing.T) {
	pin := true
	f := &Fake{}
	g, _ := gateWith(t, f, []byte(`{"enabled":true}`), &pin)
	if err := g.SetHelloShortcut(false); err != nil {
		t.Fatal(err)
	}
	c := need(t, g, Action{Reason: "delete x", Key: "k", Caller: "ui-1"})
	if !c.PIN || c.Hello {
		t.Fatalf("shortcut off: %+v", c)
	}
	if err := g.ConfirmWithPIN(c.ID, "ui-1"); err != nil {
		t.Fatal(err)
	}
	if got := settled(t, g, c.ID); got.State != StateConfirmed {
		t.Fatalf("after the PIN: %+v", got)
	}
	if len(f.Prompts()) != 0 {
		t.Fatalf("the OS prompt opened with the shortcut off: %q", f.Prompts())
	}

	if err := g.SetHelloShortcut(true); err != nil {
		t.Fatal(err)
	}
	f.SetUnavailable(errors.New("no Windows Hello"))
	c2 := need(t, g, Action{Reason: "delete y", Key: "k2", Caller: "ui-1"})
	if !c2.PIN || c2.Hello {
		t.Fatalf("OS prompt unavailable must leave the PIN: %+v", c2)
	}
	if st := g.Status(); !st.Available {
		t.Fatalf("gate unavailable although the PIN is set: %+v", st)
	}
}

// A new install starts with the shortcut off and writes it down; the choice survives a restart.
func TestNewInstallHelloOffAndPersists(t *testing.T) {
	pin := true
	g, p := gateWith(t, &Fake{}, nil, &pin)
	if g.HelloShortcut() {
		t.Fatal("a new install offers Windows Hello")
	}
	if err := g.SetHelloShortcut(true); err != nil {
		t.Fatal(err)
	}
	again := New(p, &Fake{}, nil, FileMarker(p.Home+"/test-marker"))
	if !again.HelloShortcut() {
		t.Fatal("the shortcut was lost on restart")
	}
}

// Rollback: gate.json keeps every field an older daemon reads; hello_shortcut is only added.
func TestGateFileStaysReadableForOlderDaemons(t *testing.T) {
	pin := true
	g, p := gateWith(t, &Fake{}, []byte(`{"enabled":true,"method":"windows-hello","grace_minutes":15}`), &pin)
	if err := g.SetHelloShortcut(false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p.SecretsDir() + "/gate.json")
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Enabled *bool `json:"enabled"`
		Grace   *int  `json:"grace_minutes"`
	}
	if json.Unmarshal(b, &old) != nil || old.Enabled == nil || !*old.Enabled || old.Grace == nil || *old.Grace != 15 {
		t.Fatalf("an older daemon would misread %s", b)
	}
}
