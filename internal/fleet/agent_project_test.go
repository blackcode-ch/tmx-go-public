package fleet

import (
	"encoding/json"
	"testing"
)

func TestAgentProjectPersistsAndRejectsPeers(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	w := shelveWorker(t, e, f, "page")
	patch := map[string]json.RawMessage{"what": json.RawMessage(`"durable"`)}
	if _, err := f.UpdateAgentProject(AgentCaller(w.ID), w.ID, patch); err != nil {
		t.Fatal(err)
	}
	var stored agentProjectRecord
	if err := f.st.AgentProjectView(w.ID, &stored); err != nil || string(stored.Brief["what"]) != `"durable"` {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	if _, err := f.AgentProject(PeerCaller("elsewhere"), w.ID); KindOf(err) != KindForbidden {
		t.Fatalf("peer read: %v", err)
	}
	if _, err := f.UpdateAgentProject(PeerCaller("elsewhere"), w.ID, patch); KindOf(err) != KindForbidden {
		t.Fatalf("peer write: %v", err)
	}
}
