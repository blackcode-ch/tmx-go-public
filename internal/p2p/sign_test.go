package p2p

import "testing"

// Sign makes a statement any node can check later, whoever passes it on (a group's admin
// certificate): signed by one host, verified by another, and broken by any change.
func TestHostSignAndVerify(t *testing.T) {
	a, b := startNode(t, "alex", nil, ""), startNode(t, "bo", nil, "")
	cert, err := a.Sign(Envelope{ID: "c1", Type: "chat.admin", ChatID: "g1", From: "someone-else", Body: []byte(`{"v":2,"admin":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if cert.From != a.PeerID() || cert.At == 0 || len(cert.Sig) == 0 {
		t.Fatalf("not signed as alex: %+v", cert)
	}
	if err := b.VerifyEnvelope(cert); err != nil {
		t.Fatalf("bo cannot verify alex's statement: %v", err)
	}
	tampered := cert
	tampered.Body = []byte(`{"v":2,"admin":"y"}`)
	if b.VerifyEnvelope(tampered) == nil {
		t.Fatal("a changed body still verifies")
	}
	forged := cert
	forged.From = b.PeerID() // alex's signature claimed as bo's
	if a.VerifyEnvelope(forged) == nil {
		t.Fatal("a signature verified for another signer")
	}
	if _, err := a.Sign(Envelope{Type: "chat.admin"}); err == nil {
		t.Fatal("signed an envelope without an ID")
	}
}
