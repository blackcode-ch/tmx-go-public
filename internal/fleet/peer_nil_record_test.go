package fleet

import "testing"

// go-reviewer-o2's nil-record sweep, 3 Nov: callerRecLocked returns (nil, nil) for both the
// owner and a peer machine (peers are owner-equivalent for agent-operation routes), but several
// callers that pre-date peers only ever expected a nil record from the OWNER and then
// dereference it unconditionally - a peer reaching them through servePeer's ordinary-mux
// fallthrough panics instead of being refused. These two must refuse a peer outright, before
// callerRecLocked ever runs.

func TestAskOwnerRefusesPeer(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	if _, err := f.AskOwner(PeerCaller("hosta"), AskRequest{Question: "from a peer?"}); KindOf(err) != KindForbidden {
		t.Fatalf("peer ask: %v", err)
	}
}

func TestRequestProcessRefusesPeer(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	if _, err := f.RequestProcess(PeerCaller("hosta"), ProcessRequest{Purpose: "look around", Command: "ls"}); KindOf(err) != KindForbidden {
		t.Fatalf("peer process request: %v", err)
	}
}
