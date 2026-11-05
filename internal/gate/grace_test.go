package gate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// clock is a fake time the test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// graceGate is an enabled gate on a fake clock; notified counts the change notifications.
func graceGate(t *testing.T, f *Fake) (*Gate, *clock, func() int) {
	t.Helper()
	g, _ := newGate(t, f)
	c := &clock{t: time.Date(2026, 10, 28, 14, 42, 0, 0, time.UTC)}
	g.now = c.now
	var mu sync.Mutex
	n := 0
	g.SetNotify(func(Status) {
		mu.Lock()
		defer mu.Unlock()
		n++
	})
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	return g, c, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

func owner(g *Gate, reason string) Action {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Action{Reason: reason, Key: reason, Owner: true, Caller: "ui", UI: true, Session: g.proofSession}
}

func TestGraceOpensWithAProofAndExpires(t *testing.T) {
	f := &Fake{}
	g, c, notified := graceGate(t, f)
	if st := g.Status(); st.GraceMinutes != DefaultGrace || st.GraceUntil != 0 || len(st.GraceSteps) != len(GraceSteps) {
		t.Fatalf("a fresh gate: %+v", st)
	}
	before := notified()
	a := owner(g, "allow sudo true for head")
	id := confirm(t, g, a)
	if err := g.Require(a, id); err != nil {
		t.Fatalf("the proof itself: %v", err)
	}
	if notified() == before {
		t.Fatal("opening the window told nobody")
	}
	want := c.now().Add(time.Hour).UnixMilli()
	if st := g.Status(); st.GraceUntil != want {
		t.Fatalf("grace_until %d, want %d", st.GraceUntil, want)
	}
	// A second action, another key, no confirmation: passes without a prompt.
	prompts := len(f.Prompts())
	c.add(59 * time.Minute)
	if err := g.Require(owner(g, "delete the project Web"), ""); err != nil {
		t.Fatalf("inside the window: %v", err)
	}
	if err := g.Require(Action{Reason: "make bob the admin", Key: "admin", Always: true, Owner: true, Caller: "ui", UI: true, Session: owner(g, "").Session}, ""); err != nil {
		t.Fatalf("an Always action inside the window: %v", err)
	}
	if len(f.Prompts()) != prompts {
		t.Fatal("the window still prompted")
	}
	// Over: it asks again.
	c.add(time.Minute)
	if g.GraceUntil() != (time.Time{}) || g.Status().GraceUntil != 0 {
		t.Fatal("the window outlived its hour")
	}
	var need *NeedConfirmation
	if !errors.As(g.Require(owner(g, "delete the project Shop"), ""), &need) {
		t.Fatal("after the window: want NeedConfirmation")
	}
}

func TestGraceOnlyForTheOwner(t *testing.T) {
	g, _, _ := graceGate(t, &Fake{})
	confirm(t, g, owner(g, "allow x"))
	var need *NeedConfirmation
	// An action without Owner (an agent's or Companion's request, if one ever got here) asks.
	if !errors.As(g.Require(Action{Reason: "allow y", Key: "y"}, ""), &need) {
		t.Fatal("a request without the owner's token passed on the window")
	}
}

func TestGraceOffAlwaysAsks(t *testing.T) {
	f := &Fake{}
	g, _, _ := graceGate(t, f)
	if err := g.SetGrace(0); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"allow a", "allow b", "allow c"} {
		a := owner(g, r)
		id := confirm(t, g, a)
		if err := g.Require(a, id); err != nil {
			t.Fatal(err)
		}
		if g.Status().GraceUntil != 0 {
			t.Fatal("off opened a window")
		}
	}
	if len(f.Prompts()) != 3 {
		t.Fatalf("off: %d prompts for 3 actions", len(f.Prompts()))
	}
	if err := g.SetGrace(7); !errors.Is(err, ErrGraceStep) {
		t.Fatalf("7 minutes: %v", err)
	}
}

func TestGraceLockNow(t *testing.T) {
	g, _, notified := graceGate(t, &Fake{})
	confirm(t, g, owner(g, "allow x"))
	before := notified()
	g.Lock()
	if notified() == before || g.Status().GraceUntil != 0 {
		t.Fatal("Lock left the window open or told nobody")
	}
	var need *NeedConfirmation
	if !errors.As(g.Require(owner(g, "allow y"), ""), &need) {
		t.Fatal("after Lock: want NeedConfirmation")
	}
}

func TestGraceIsNotExtendedByUse(t *testing.T) {
	g, c, _ := graceGate(t, &Fake{})
	confirm(t, g, owner(g, "allow x"))
	until := g.GraceUntil()
	for i := 0; i < 5; i++ {
		c.add(10 * time.Minute)
		if err := g.Require(owner(g, "allow y"), ""); err != nil {
			t.Fatalf("minute %d: %v", (i+1)*10, err)
		}
		if !g.GraceUntil().Equal(until) {
			t.Fatal("passing on the window moved its end")
		}
	}
	c.add(10 * time.Minute)
	if !g.GraceUntil().IsZero() {
		t.Fatal("use kept the window open past its hour")
	}
	// A new proof renews it.
	confirm(t, g, owner(g, "allow z"))
	if got := g.GraceUntil(); !got.Equal(c.now().Add(time.Hour)) {
		t.Fatalf("a new proof: until %v", got)
	}
}

func TestGraceFreshActionsStillAsk(t *testing.T) {
	f := &Fake{}
	g, _, _ := graceGate(t, f)
	confirm(t, g, owner(g, "allow x"))
	off := Action{Reason: "turn off the owner gate", Key: "off", Fresh: true, Owner: true, Caller: "ui", UI: true, Session: owner(g, "").Session}
	longer := Action{Reason: "stop asking for 2 hours", Key: "longer", Always: true, Fresh: true, Owner: true, Caller: "ui", UI: true, Session: owner(g, "").Session}
	for _, a := range []Action{off, longer} {
		var need *NeedConfirmation
		if !errors.As(g.Require(a, ""), &need) {
			t.Fatalf("%q inside the window: want NeedConfirmation", a.Reason)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, _ := g.Wait(ctx, need.Confirmation.ID)
		cancel()
		if err := g.Require(a, c.ID); err != nil {
			t.Fatalf("%q with its proof: %v", a.Reason, err)
		}
	}
	// Turning grace on from off is longer too: it is the API that marks it Fresh, the gate
	// asks whatever the window says.
	if err := g.SetGrace(0); err != nil {
		t.Fatal(err)
	}
	var need *NeedConfirmation
	if !errors.As(g.Require(longer, ""), &need) {
		t.Fatal("turning grace on: want NeedConfirmation")
	}
	g.Cancel(need.Confirmation.ID)
}

func TestGraceShorterNeedsNoProofAndShortensTheOpenWindow(t *testing.T) {
	f := &Fake{}
	g, c, _ := graceGate(t, f)
	confirm(t, g, owner(g, "allow x"))
	prompts := len(f.Prompts())
	c.add(20 * time.Minute)
	if err := g.SetGrace(30); err != nil { // the API asks nothing for this
		t.Fatal(err)
	}
	if got, want := g.GraceUntil(), c.now().Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("shortened window ends %v, want %v", got, want)
	}
	if err := g.SetGrace(15); err != nil {
		t.Fatal(err)
	}
	if !g.GraceUntil().IsZero() {
		t.Fatal("a window shorter than its age stayed open")
	}
	if len(f.Prompts()) != prompts {
		t.Fatal("shortening prompted")
	}
}

func TestGraceRestartLocksAndKeepsTheLength(t *testing.T) {
	f := &Fake{}
	g, _, _ := graceGate(t, f)
	if err := g.SetGrace(240); err != nil {
		t.Fatal(err)
	}
	confirm(t, g, owner(g, "allow x"))
	if g.GraceUntil().IsZero() {
		t.Fatal("no window")
	}
	// A restart: a new Gate on the same state file.
	g2 := New(config.Paths{Home: filepath.Dir(filepath.Dir(g.Path()))}, f, nil, g.marker)
	st := g2.Status()
	if !st.Enabled || st.GraceMinutes != 240 || st.GraceUntil != 0 {
		t.Fatalf("after a restart: %+v", st)
	}
	var need *NeedConfirmation
	if !errors.As(g2.Require(owner(g, "allow y"), ""), &need) {
		t.Fatal("a restart kept the window open")
	}
	g2.Cancel(need.Confirmation.ID)
	// Turning the gate off and on keeps the length.
	if err := g2.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if g2.GraceMinutes() != 240 {
		t.Fatal("SetEnabled dropped the length")
	}
	b, _ := os.ReadFile(g2.Path())
	if !strings.Contains(string(b), `"grace_minutes": 240`) {
		t.Fatalf("gate.json: %s", b)
	}
}

func TestGraceDamagedFileAsksEveryTime(t *testing.T) {
	f := &Fake{}
	g, p := newGate(t, f)
	if err := os.WriteFile(g.Path(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := New(p, f, nil, FileMarker(p.Home+"/test-marker")).Status(); !st.Enabled || st.GraceMinutes != 0 {
		t.Fatalf("damaged gate.json: %+v", st)
	}
	// An old gate.json without the field: the default.
	if err := os.WriteFile(g.Path(), []byte(`{"enabled": true, "method": "touchid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := New(p, f, nil, FileMarker(p.Home+"/test-marker")).Status(); !st.Enabled || st.GraceMinutes != DefaultGrace {
		t.Fatalf("old gate.json: %+v", st)
	}
}

func TestGraceOpensWhileTheGateIsOff(t *testing.T) {
	// The gate off: only Always actions ask, and their proof opens the window for the next one.
	g, _ := newGate(t, &Fake{})
	handover := Action{Reason: "make bob the admin of Team", Key: "h1", Always: true, Owner: true, Caller: "ui", UI: true, Session: owner(g, "").Session}
	confirm(t, g, handover)
	if err := g.Require(Action{Reason: "make eve the admin of Team", Key: "h2", Always: true, Owner: true, Caller: "ui", UI: true, Session: owner(g, "").Session}, ""); err != nil {
		t.Fatalf("the second handover inside the window: %v", err)
	}
	on := Action{Reason: "turn on the owner gate", Key: "on", Always: true, Fresh: true, Owner: true, Caller: "ui", UI: true, Session: owner(g, "").Session}
	var need *NeedConfirmation
	if !errors.As(g.Require(on, ""), &need) {
		t.Fatal("turning the gate on inside the window must ask")
	}
	g.Cancel(need.Confirmation.ID)
}

func TestGraceLabel(t *testing.T) {
	for m, want := range map[int]string{0: "off", 5: "5 minutes", 60: "1 hour", 120: "2 hours", 1440: "24 hours"} {
		if got := GraceLabel(m); got != want {
			t.Errorf("GraceLabel(%d) = %q, want %q", m, got, want)
		}
	}
	for _, m := range GraceSteps {
		if !ValidGrace(m) {
			t.Errorf("step %d invalid", m)
		}
	}
	if ValidGrace(-1) || ValidGrace(61) || ValidGrace(1441) {
		t.Error("a length outside the steps was valid")
	}
}
