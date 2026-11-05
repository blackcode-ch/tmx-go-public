package fleet

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestCodexFullAccessOwnerOptIn(t *testing.T) {
	env := newUnguardedEnv(t)
	fleet := env.started()
	for _, full := range []bool{false, true} {
		name := "safe-codex"
		if full {
			name = "opted-in-codex"
		}
		agent, err := fleet.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, fleet), Name: name, Harness: model.HarnessCodex, CodexFullAccess: full})
		if err != nil {
			t.Fatal(err)
		}
		if env.chat.Last(agent.ID).Spec.CodexFullAccess != full {
			t.Fatal("per-agent opt-in not propagated")
		}
		stored, err := env.st.GetAgent(agent.ID)
		if err != nil || stored.CodexFullAccess {
			t.Fatalf("full access must not survive daemon restart: %v", err)
		}
	}
	readonly, err := fleet.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, fleet), Name: "readonly-codex", Harness: model.HarnessCodex, Profile: "reviewer", CodexFullAccess: true})
	if err != nil || env.chat.Last(readonly.ID).Spec.CodexFullAccess {
		t.Fatalf("full access overrides read-only profile: %v", err)
	}
	head := mustAgent(t, fleet, HeadName)
	caller := callerFor(t, fleet, env.chat, head.ID)
	if _, err := fleet.LaunchAgent(caller, LaunchRequest{Name: "self-escalation", Harness: model.HarnessCodex, CodexFullAccess: true}); KindOf(err) != KindForbidden {
		t.Fatalf("agent may opt into full access: %v", err)
	}
}
