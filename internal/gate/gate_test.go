package gate

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func newGate(t *testing.T, f *Fake) (*Gate, config.Paths) {
	t.Helper()
	p := config.Paths{Home: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.SecretsDir()+"/gate.json", []byte(`{"enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	return New(p, f, nil, FileMarker(p.Home+"/test-marker")), p
}

func confirm(t *testing.T, g *Gate, a Action) string {
	t.Helper()
	err := g.Require(a, "")
	var need *NeedConfirmation
	if !errors.As(err, &need) {
		t.Fatalf("Require = %v, want NeedConfirmation", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _ := g.Wait(ctx, need.Confirmation.ID)
	if c.State != StateConfirmed {
		t.Fatalf("confirmation %+v", c)
	}
	return c.ID
}

func TestExplicitOffAndEnablingNeedsProof(t *testing.T) {
	f := &Fake{}
	g, p := newGate(t, f)
	if g.Enabled() {
		t.Fatal("explicitly disabled fixture must be off")
	}
	if err := g.Require(Action{Reason: "delete x", Key: "k"}, ""); err != nil {
		t.Fatalf("off gate blocked: %v", err)
	}
	on := Action{Reason: "turn the owner gate on", Key: "on", Always: true}
	id := confirm(t, g, on)
	if err := g.Require(on, id); err != nil {
		t.Fatalf("confirmed proof refused: %v", err)
	}
	if err := g.Require(on, id); err == nil {
		t.Fatal("a proof must be single use")
	}
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !New(p, f, nil, FileMarker(p.Home+"/test-marker")).Enabled() {
		t.Fatal("state not persisted")
	}
}

func TestProofIsBoundToItsAction(t *testing.T) {
	g, _ := newGate(t, &Fake{})
	_ = g.SetEnabled(true)
	a := Action{Reason: "allow sudo true", Key: "approve-1"}
	id := confirm(t, g, a)
	if err := g.Require(Action{Reason: "delete project", Key: "delete-1"}, id); err == nil {
		t.Fatal("a proof for one action let another through")
	}
	g.Cancel(firstPending(g))
	if err := g.Require(a, id); err != nil {
		t.Fatalf("proof refused for its own action: %v", err)
	}
	if err := g.Require(a, "made-up-id"); err == nil {
		t.Fatal("an unknown confirmation id was accepted")
	}
}

func firstPending(g *Gate) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, c := range g.confs {
		if c.State == StatePending {
			return id
		}
	}
	return ""
}

func TestFailedCancelledAndBusy(t *testing.T) {
	block := make(chan struct{})
	f := &Fake{Answer: func(ctx context.Context, r string) error {
		if r == "wrong" {
			return errors.New("Authentication failed")
		}
		select {
		case <-block:
			return nil
		case <-ctx.Done():
			return ErrCancelled
		}
	}}
	g, _ := newGate(t, f)
	_ = g.SetEnabled(true)

	var need *NeedConfirmation
	if !errors.As(g.Require(Action{Reason: "wrong", Key: "w"}, ""), &need) {
		t.Fatal("want NeedConfirmation")
	}
	c, _ := g.Wait(context.Background(), need.Confirmation.ID)
	if c.State != StateFailed || c.Error == "" {
		t.Fatalf("failed proof: %+v", c)
	}
	if g.Require(Action{Reason: "wrong", Key: "w"}, c.ID) == nil {
		t.Fatal("a failed proof let the action through")
	}
	_, _ = g.Wait(context.Background(), firstPending(g))

	if !errors.As(g.Require(Action{Reason: "slow", Key: "s"}, ""), &need) {
		t.Fatal("want NeedConfirmation")
	}
	// the same action again while the prompt is open: the same confirmation, no second prompt
	var again *NeedConfirmation
	if !errors.As(g.Require(Action{Reason: "slow", Key: "s"}, ""), &again) || again.Confirmation.ID != need.Confirmation.ID {
		t.Fatal("a retry opened a second prompt")
	}
	if err := g.Require(Action{Reason: "other", Key: "o"}, ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("second action while a prompt is open: %v", err)
	}
	g.Cancel(need.Confirmation.ID)
	if c, _ := g.Get(need.Confirmation.ID); c.State != StateFailed {
		t.Fatalf("cancelled: %+v", c)
	}
	close(block)
}

func TestUnavailableAndDamagedFile(t *testing.T) {
	f := &Fake{Unavail: errors.New("no GUI session")}
	g, p := newGate(t, f)
	if st := g.Status(); st.Available || st.Unavailable == "" || st.File != g.Path() {
		t.Fatalf("status %+v", st)
	}
	if err := g.Require(Action{Reason: "turn on", Key: "on", Always: true}, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("enabling without an authenticator: %v", err)
	}
	if err := os.WriteFile(g.Path(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !New(p, f, nil, FileMarker(p.Home+"/test-marker")).Enabled() {
		t.Fatal("a damaged gate.json must count as on")
	}
	_ = os.Remove(g.Path())
	_ = os.Remove(p.Home + "/test-marker")
	if New(p, f, nil, FileMarker(p.Home+"/test-marker")).Enabled() {
		t.Fatal("missing state with no marker must initialize explicit off")
	}
}

func TestPromptRateLimit(t *testing.T) {
	f := &Fake{Answer: func(context.Context, string) error { return errors.New("no") }}
	g, _ := newGate(t, f)
	_ = g.SetEnabled(true)
	for i := 0; i < maxPerMin; i++ {
		var need *NeedConfirmation
		if !errors.As(g.Require(Action{Reason: "x", Key: "k"}, ""), &need) {
			t.Fatal("want NeedConfirmation")
		}
		g.Wait(context.Background(), need.Confirmation.ID)
	}
	if err := g.Require(Action{Reason: "x", Key: "k"}, ""); !errors.Is(err, ErrTooMany) {
		t.Fatalf("want ErrTooMany, got %v", err)
	}
}
