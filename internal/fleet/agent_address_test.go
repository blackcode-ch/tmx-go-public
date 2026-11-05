package fleet

import (
	"strings"
	"testing"
)

func TestRoomAgentResolutionAndMail(t *testing.T) {
	_, f, peer, lead, caller := peerEnv(t)
	for _, address := range []string{lead.Name + "@testbox", "tmx:testbox:" + lead.Name, strings.ToUpper(lead.Name) + "@TESTBOX", "TMX:TESTBOX:" + strings.ToUpper(lead.Name)} {
		a, err := f.Agent(address)
		if err != nil || a.ID != lead.ID {
			t.Fatalf("resolve %s: %+v %v", address, a, err)
		}
		m, err := f.SendMessage(OwnerCaller, SendRequest{To: address, Body: "hello"})
		if err != nil || m.To != lead.ID {
			t.Fatalf("local mail %s: %+v %v", address, m, err)
		}
	}
	for _, address := range []string{"bob@hostc", "tmx:hostc:bob", "BOB@HOSTC", "TMX:HOSTC:BOB"} {
		m, err := f.SendMessage(caller, SendRequest{To: address, Body: "hello"})
		if err != nil || m.To != PeerParty("bob", "hostc") {
			t.Fatalf("peer mail %s: %+v %v", address, m, err)
		}
		if _, err := f.Agent(address); KindOf(err) != KindNotFound {
			t.Fatalf("remote resolved locally: %v", err)
		}
	}
	if len(peer.Sent()) != 4 {
		t.Fatalf("peer sends: %d", len(peer.Sent()))
	}
	if _, err := f.SendMessage(Caller{}, SendRequest{To: "tmx:testbox:" + lead.Name, Body: "anonymous"}); KindOf(err) != KindForbidden {
		t.Fatalf("anonymous: %v", err)
	}
	if _, err := f.SendMessage(caller, SendRequest{To: "tmx:unknown:bob", Body: "unknown"}); KindOf(err) != KindNotFound {
		t.Fatalf("unknown peer: %v", err)
	}
}
