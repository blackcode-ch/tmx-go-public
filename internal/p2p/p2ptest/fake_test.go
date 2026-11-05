package p2ptest

import (
	"context"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// The fake's signatures are bound to the signer and to every field, like the real ones: a
// node can only sign as itself, and a changed statement no longer verifies.
func TestFakeSignAndVerify(t *testing.T) {
	n := NewNetwork()
	alex, bo := n.NewNode("alex"), n.NewNode("bo")
	cert, err := alex.Sign(p2p.Envelope{ID: "c1", Type: "chat.admin", ChatID: "g1", From: bo.ID(), Body: []byte(`{"admin":"bo"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if cert.From != alex.ID() || cert.At == 0 {
		t.Fatalf("not signed as alex: %+v", cert)
	}
	if err := bo.VerifyEnvelope(cert); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*p2p.Envelope){
		"body": func(e *p2p.Envelope) { e.Body = []byte(`{"admin":"cleo"}`) },
		"chat": func(e *p2p.Envelope) { e.ChatID = "g2" },
		"at":   func(e *p2p.Envelope) { e.At++ },
		"from": func(e *p2p.Envelope) { e.From = bo.ID() },
		"who":  func(e *p2p.Envelope) { e.From = "peer-nobody" },
	} {
		c := cert
		mut(&c)
		if bo.VerifyEnvelope(c) == nil {
			t.Errorf("tampered %s still verifies", name)
		}
	}
	// Bo cannot make alex's signature: his own over the same content is a different one.
	again, _ := bo.Sign(p2p.Envelope{ID: "c1", Type: "chat.admin", ChatID: "g1", Body: []byte(`{"admin":"bo"}`), At: cert.At})
	again.From = alex.ID()
	if bo.VerifyEnvelope(again) == nil {
		t.Fatal("bo signed as alex")
	}
}

// Envelopes that Send delivers verify too; Dropped from a send hook acknowledges without
// delivering (an older tmx that does not know the type).
func TestFakeSendSignsAndDrops(t *testing.T) {
	n := NewNetwork()
	alex, bo := n.NewNode("alex"), n.NewNode("bo")
	inv, _ := alex.CreateInvite(p2p.InviteOptions{Kind: p2p.InviteDM})
	if _, err := bo.Join(context.Background(), inv.Link); err != nil {
		t.Fatal(err)
	}
	if err := alex.Send(context.Background(), bo.ID(), p2p.Envelope{ID: "m1", Type: "chat.msg", Body: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	got := <-bo.Incoming()
	if err := bo.VerifyEnvelope(got); err != nil {
		t.Fatalf("a sent envelope does not verify: %v", err)
	}
	alex.SetSendHook(func(string, p2p.Envelope) error { return Dropped })
	if err := alex.Send(context.Background(), bo.ID(), p2p.Envelope{ID: "m2", Type: "chat.new", Body: []byte(`{}`)}); err != nil {
		t.Fatalf("dropped send: %v", err)
	}
	select {
	case e := <-bo.Incoming():
		t.Fatalf("delivered %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}
