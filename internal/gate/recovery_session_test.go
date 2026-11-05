package gate

import (
	"testing"
	"time"
)

func TestRecoverySessionBindingExpiryAndLock(t *testing.T) {
	g, clock, _ := graceGate(t, &Fake{})
	defer g.Close()
	if g.RecoverySession("ui", "invented") {
		t.Fatal("unproven session accepted")
	}
	a := owner(g, "prove UI")
	id := confirm(t, g, a)
	_, credential, ok := g.SessionProof(id, "ui")
	if !ok || credential == "" || !g.RecoverySession("ui", credential) {
		t.Fatal("proven UI not bound")
	}
	if g.RecoverySession("other", credential) || g.RecoverySession("ui", "") {
		t.Fatal("credential crossed UI boundary")
	}
	clock.add(time.Hour)
	if g.RecoverySession("ui", credential) {
		t.Fatal("expired credential accepted")
	}
	id = confirm(t, g, owner(g, "prove again"))
	_, credential, _ = g.SessionProof(id, "ui")
	g.Lock()
	if g.RecoverySession("ui", credential) {
		t.Fatal("lock retained recovery authority")
	}
}
