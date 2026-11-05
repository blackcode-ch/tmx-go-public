package peers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

func TestFingerprintMatchesPython(t *testing.T) {
	// hashlib.sha256(b"abc").hexdigest()[:12]
	if got := Fingerprint("abc"); got != "ba7816bf8f01" {
		t.Fatal(got)
	}
	if tok := NewPairToken(); len(tok) != 43 || strings.ContainsAny(tok, "+/=") {
		t.Fatalf("not token_urlsafe(32): %q", tok)
	}
}

func TestTokensRotationAndIdentify(t *testing.T) {
	ts := NewTokens(filepath.Join(t.TempDir(), "peers.json"))
	now := time.Unix(1_000_000, 0)
	ts.now = func() time.Time { return now }
	if _, err := ts.Store("hosta", "short", "hosta"); err == nil {
		t.Fatal("accepted a short token")
	}
	a, b := NewPairToken(), NewPairToken()
	if fp, err := ts.Store("Hosta", a, "hosta"); err != nil || fp != Fingerprint(a) {
		t.Fatal(fp, err)
	}
	if ts.For("hosta") != a {
		t.Fatal("For")
	}
	if p, ok := ts.Identify(a); !ok || p != "hosta" {
		t.Fatal("Identify")
	}
	if _, ok := ts.Identify(b); ok {
		t.Fatal("unknown token identified")
	}
	if _, err := ts.Store("hosta", b, "hosta"); err != nil {
		t.Fatal(err)
	}
	if p, ok := ts.Identify(a); !ok || p != "hosta" {
		t.Fatal("the previous token stopped working inside the overlap")
	}
	now = now.Add(rotationOverlap + time.Second)
	if _, ok := ts.Identify(a); ok {
		t.Fatal("the previous token still works after the overlap")
	}
	if _, ok := ts.Identify(""); ok {
		t.Fatal("empty token identified")
	}
}

// fakePython is a Python tmx daemon's peer API, as far as this package uses it.
type fakePython struct {
	t    *testing.T
	name string

	mu           sync.Mutex
	pairTok      string
	incoming     []fleet.PeerOutbound
	answer       func(m fleet.PeerOutbound) (int, any)
	wrongPairAck bool // answer a fingerprint that doesn't match what was actually stored
}

func (p *fakePython) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tok, from := r.Header.Get(HeaderToken), r.Header.Get(HeaderFrom)
	if from != "hostb" || r.Header.Get(HeaderOnBehalf) != "daemon" {
		http.Error(w, `{"error":"who?"}`, 401)
		return
	}
	paired := p.pairTok != "" && tok == p.pairTok
	write := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/api/peers/pair-token":
		if !paired {
			write(401, map[string]string{"error": "bad token"})
			return
		}
		var b struct{ Token string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		p.pairTok = b.Token
		fp := Fingerprint(b.Token)
		if p.wrongPairAck {
			fp = Fingerprint(b.Token + "-not-what-we-stored")
		}
		write(200, map[string]string{"fingerprint": fp})
	case "/api/sessions":
		if !paired {
			write(401, map[string]string{"error": "bad token"})
			return
		}
		write(200, map[string]any{"host": p.name, "commit": "abc1234\x07", "sessions": []map[string]any{
			{"name": "tmx-lead", "host": "spoofed", "state": "idle", "title": "lead‮"},
			{"name": "../bad", "state": "busy"},
		}})
	case "/api/messages/incoming":
		if !paired {
			write(401, map[string]string{"error": "bad token"})
			return
		}
		var m fleet.PeerOutbound
		_ = json.NewDecoder(r.Body).Decode(&m)
		p.incoming = append(p.incoming, m)
		if p.answer != nil {
			code, v := p.answer(m)
			write(code, v)
			return
		}
		write(201, map[string]string{"id": m.ID, "state": "queued"})
	default:
		write(404, map[string]string{"error": "no route"})
	}
}

// newPair is this daemon ("hostb") and a Python peer sharing a pair token, as after
// tmx peers import-python (paired=false: no token at all).
func newPair(t *testing.T, name string, paired ...bool) (*Client, *fakePython) {
	t.Helper()
	py := &fakePython{t: t, name: name}
	srv := httptest.NewTLSServer(py)
	t.Cleanup(srv.Close)
	p := config.Paths{Home: t.TempDir()}
	cfg := config.Config{Profile: config.ProfileProduction, Machine: "hostb",
		Peers: []config.Peer{{Name: name, URL: srv.URL}}}
	c := New(cfg, p, nil)
	c.http.Transport = srv.Client().Transport
	if len(paired) == 0 || paired[0] {
		py.pairTok = NewPairToken()
		if _, err := c.tokens.Store(name, py.pairTok, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return c, py
}

func TestSendUsesThePairToken(t *testing.T) {
	c, py := newPair(t, "hostc")
	m := fleet.PeerOutbound{ID: "0123456789ab", Sender: "worker", SenderHost: "hostb", To: "b-lead", Body: "hi", SHA256: "x"}
	if err := c.Send("Hostc", m); err != nil {
		t.Fatal(err)
	}
	if len(py.incoming) != 1 || py.incoming[0].Body != "hi" {
		t.Fatalf("%+v", py.incoming)
	}
}

// RotateWith generates a fresh token, pushes it to a peer sorting after this machine
// ("hostb" < "hostc"), and stores it locally only once the peer's own fingerprint confirms
// it received exactly that token.
func TestRotateWithStoresOnlyAfterTheirConfirmation(t *testing.T) {
	c, py := newPair(t, "hostc")
	before := c.tokens.For("hostc")
	fp, err := c.RotateWith(context.Background(), "Hostc")
	if err != nil {
		t.Fatal(err)
	}
	after := c.tokens.For("hostc")
	if after == before {
		t.Fatal("the token was not rotated")
	}
	if Fingerprint(after) != fp {
		t.Fatalf("stored token does not match the returned fingerprint: %s vs %s", Fingerprint(after), fp)
	}
	if py.pairTok != after {
		t.Fatal("the peer's own token does not match what we stored")
	}
	// The old token keeps working for the overlap (TestTokensRotationAndIdentify already
	// proves the mechanism itself; this just confirms Store, not a bypass, was used).
	if p, ok := c.tokens.Identify(before); !ok || p != "hostc" {
		t.Fatal("the previous token stopped working inside the overlap")
	}
}

// Rotating toward a peer sorting BEFORE this machine is refused locally - DESIGN.md's rule
// (the lower-sorting name generates and rotates) means hosta generates ours, not the other
// way round, and that peer's own route would refuse it anyway; refusing it here first means no
// network call and no new token is ever generated for nothing.
func TestRotateWithRefusesAPeerThatSortsFirst(t *testing.T) {
	c, py := newPair(t, "hosta")
	before := c.tokens.For("hosta")
	if _, err := c.RotateWith(context.Background(), "hosta"); err == nil {
		t.Fatal("rotated toward a peer that generates our token, not the other way round")
	}
	if c.tokens.For("hosta") != before || py.pairTok != before {
		t.Fatal("something changed despite the refusal")
	}
}

// A fingerprint mismatch (the peer claims it stored something other than what was sent) must
// never be stored locally: the two machines would then disagree about the current token.
func TestRotateWithNeverStoresOnAFingerprintMismatch(t *testing.T) {
	c, py := newPair(t, "hostc")
	before := c.tokens.For("hostc")
	py.wrongPairAck = true
	if _, err := c.RotateWith(context.Background(), "hostc"); err == nil {
		t.Fatal("stored a token the peer did not actually confirm")
	}
	if c.tokens.For("hostc") != before {
		t.Fatal("a mismatched rotation still changed the locally stored token")
	}
}

// An unpaired peer (no token yet: tmx peers import-python has not run) is refused before any
// network call, same as Send.
func TestRotateWithUnpairedPeerIsNeverCalled(t *testing.T) {
	c, _ := newPair(t, "hostc", false)
	if _, err := c.RotateWith(context.Background(), "hostc"); err == nil {
		t.Fatal("rotated with no pair token at all")
	}
}

// The fleet token is retired fleet-wide: an unpaired peer is never called, with any token.
func TestUnpairedPeerIsNeverCalled(t *testing.T) {
	c, py := newPair(t, "hosta", false)
	err := c.Send("hosta", fleet.PeerOutbound{ID: "0123456789ab"})
	var refused Refused
	if !errors.As(err, &refused) || !strings.Contains(refused.Msg, "import-python") {
		t.Fatalf("unpaired send: %v", err)
	}
	c.PollAll(context.Background())
	if st := c.Statuses()[0]; st.OK || st.Paired {
		t.Fatalf("%+v", st)
	}
	py.mu.Lock()
	defer py.mu.Unlock()
	if len(py.incoming) != 0 {
		t.Fatal("an unpaired peer was called")
	}
}

func TestPollCleansAndPinsHost(t *testing.T) {
	c, _ := newPair(t, "hosta")
	c.PollAll(context.Background())
	st := c.Statuses()
	if len(st) != 1 || !st[0].OK || st[0].Commit != "abc1234" || len(st[0].Sessions) != 1 {
		t.Fatalf("%+v", st)
	}
	s := st[0].Sessions[0]
	if s.Name != "tmx-lead" || s.Host != "hosta" || s.Title != "lead" {
		t.Fatalf("not cleaned: %+v", s)
	}
}

func TestPollRefusesWrongHost(t *testing.T) {
	c, py := newPair(t, "hosta")
	py.name = "hostc"
	c.PollAll(context.Background())
	if st := c.Statuses()[0]; st.OK || !strings.Contains(st.Error, "not \"hosta\"") {
		t.Fatalf("%+v", st)
	}
}

func TestSendClassifiesAnswers(t *testing.T) {
	c, py := newPair(t, "hostc")
	m := fleet.PeerOutbound{ID: "0123456789ab", Sender: "w", SenderHost: "hostb", To: "x", Body: "b"}
	for _, tc := range []struct {
		code      int
		body      any
		retry     bool
		wantError bool
	}{
		{201, map[string]string{"id": "0123456789ab"}, false, false},
		{201, map[string]string{"id": "ffffffffffff"}, false, true},                  // not our ID
		{202, map[string]string{"id": "ev-1", "state": "held"}, false, true},         // quarantined
		{409, map[string]string{"error": "message damaged in transit"}, false, true}, // refusal
		{503, map[string]string{"error": "busy"}, true, true},
	} {
		py.answer = func(fleet.PeerOutbound) (int, any) { return tc.code, tc.body }
		err := c.Send("hostc", m)
		if (err != nil) != tc.wantError || errors.Is(err, fleet.ErrPeerUnreachable) != tc.retry {
			t.Errorf("HTTP %d: %v", tc.code, err)
		}
	}
	if err := c.Send("nowhere", m); err == nil || errors.Is(err, fleet.ErrPeerUnreachable) {
		t.Fatal("unknown host")
	}
}

func TestSendUnreachableIsRetryable(t *testing.T) {
	c, _ := newPair(t, "hostc")
	c.peers["hostc"] = config.Peer{Name: "hostc", URL: "https://127.0.0.1:1"}
	err := c.Send("hostc", fleet.PeerOutbound{ID: "0123456789ab"})
	if !errors.Is(err, fleet.ErrPeerUnreachable) {
		t.Fatal(err)
	}
}

func TestTrialHasNoPeers(t *testing.T) {
	c := New(config.Config{Profile: config.ProfileTrial, Machine: "hostb",
		Peers: []config.Peer{{Name: "hosta", URL: "https://h.example.ts.net"}}}, config.Paths{Home: t.TempDir()}, nil)
	if c.Known("hosta") || len(c.Statuses()) != 0 {
		t.Fatal("the trial talks to peers")
	}
}
