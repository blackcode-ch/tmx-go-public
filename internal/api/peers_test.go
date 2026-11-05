package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peers"
	"github.com/blackcode-ch/tmx-go/internal/summarize"
)

// retiredFleetToken stands for the fleet token retired on 1 Nov 2026: never accepted.
const retiredFleetToken = "fleet-token-0123456789-0123456789-0123456789"

// pyPeer plays the Python daemon on "hosta" (it sorts before "hostb", so it hands us the
// pair token) and records the mail this daemon posts to it.
type pyPeer struct {
	mu   sync.Mutex
	mail []fleet.PeerOutbound
	pins []map[string]any // owner PIN records pushed to it
}

func (p *pyPeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/sessions":
		_ = json.NewEncoder(w).Encode(map[string]any{"host": "hosta", "commit": "abc", "sessions": []map[string]any{{"name": "tmx-lead", "state": "idle"}}})
	case "/api/messages/incoming":
		var m fleet.PeerOutbound
		_ = json.NewDecoder(r.Body).Decode(&m)
		p.mu.Lock()
		p.mail = append(p.mail, m)
		p.mu.Unlock()
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": m.ID, "state": "queued"})
	case "/api/gate/pin/record":
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		p.mu.Lock()
		p.pins = append(p.pins, b)
		p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]bool{"adopted": true})
	default:
		w.WriteHeader(404)
	}
}

func (p *pyPeer) got() []fleet.PeerOutbound {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]fleet.PeerOutbound(nil), p.mail...)
}

type peerSetup struct {
	d    *apitest.Daemon
	url  string // the daemon, with peer routes
	py   *pyPeer
	pair string // the hosta pair token, as tmx peers import-python brought it
	pc   *peers.Client
}

func newPeerSetup(t *testing.T) *peerSetup {
	t.Helper()
	d := apitest.New(t)
	py := &pyPeer{}
	pySrv := httptest.NewTLSServer(py)
	t.Cleanup(pySrv.Close)
	paths := config.Paths{Home: d.Home}
	cfg := config.Config{Profile: config.ProfileProduction, Machine: "hostb",
		Peers: []config.Peer{{Name: "hosta", URL: pySrv.URL}, {Name: "hostc", URL: "https://hostc.example.ts.net"}}}
	pc := peers.New(cfg, paths, nil)
	pc.SetTransport(pySrv.Client().Transport)
	pair := peers.NewPairToken()
	if _, err := pc.Tokens().Store("hosta", pair, "python-import"); err != nil {
		t.Fatal(err)
	}
	return &peerSetup{d: d, url: d.WithPeers(t, pc), py: py, pair: pair, pc: pc}
}

// peerDo calls the daemon as a peer machine.
func peerDo(t *testing.T, base, method, path, from, token string, hdr map[string]string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if token != "" {
		req.Header.Set(peers.HeaderToken, token)
	}
	if from != "" {
		req.Header.Set(peers.HeaderFrom, from)
	}
	req.Header.Set(peers.HeaderOnBehalf, "daemon")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func sum(body string) string {
	s := sha256.Sum256([]byte(body))
	return hex.EncodeToString(s[:])
}

func incoming(id, body, to string) map[string]any {
	return map[string]any{"id": id, "sender": "tmx-lead", "sender_host": "hosta", "to": to, "body": body,
		"reply_to": nil, "urgent": false, "sent": 1.0, "sha256": sum(body)}
}

func TestPeerAuthBoundaries(t *testing.T) {
	s := newPeerSetup(t)
	msg := incoming("0123456789ab", "hello from hosta", "head")
	cases := []struct {
		name, method, path, from, token string
		hdr                             map[string]string
		body                            any
		want                            int
	}{
		{"no token", "GET", "/api/sessions", "hosta", "", nil, nil, 401},
		{"unknown machine", "GET", "/api/sessions", "mallory", s.pair, nil, nil, 401},
		{"owner token is not a peer token", "GET", "/api/sessions", "hosta", s.d.OwnerToken, nil, nil, 401},
		{"a browser", "GET", "/api/sessions", "hosta", s.pair, map[string]string{"Origin": "https://evil.example"}, nil, 403},
		// on-behalf names whom the peer acts for on ITS OWN machine; it never changes the
		// caller here (always fleet.PeerCaller, never Owner) - tmx-lead, 3 Nov P1: Hosta's
		// real traffic relays on-behalf of its own agents too (agent:<name>), not just
		// daemon/operator, and that must keep working.
		{"a peer relaying for one of its own agents", "GET", "/api/sessions", "hosta", s.pair, map[string]string{peers.HeaderOnBehalf: "agent:x@hosta"}, nil, 200},
		// The fleet token is retired fleet-wide: refused everywhere, also for an unpaired peer.
		{"retired fleet token reads", "GET", "/api/sessions", "hosta", retiredFleetToken, nil, nil, 401},
		{"retired fleet token, unpaired peer", "GET", "/api/sessions", "hostc", retiredFleetToken, nil, nil, 401},
		{"retired fleet token mails", "POST", "/api/messages/incoming", "hosta", retiredFleetToken, nil, msg, 401},
		{"retired fleet token pairs", "POST", "/api/peers/pair-token", "hostc", retiredFleetToken, nil, map[string]string{"token": peers.NewPairToken()}, 401},
		{"the pair token reads", "GET", "/api/sessions", "hosta", s.pair, nil, nil, 200},
	}
	for _, tc := range cases {
		if got := peerDo(t, s.url, tc.method, tc.path, tc.from, tc.token, tc.hdr, tc.body, nil); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	// The pair token opens nothing else, and is only good for its own machine.
	if got := peerDo(t, s.url, "GET", "/api/sessions", "hostc", s.pair, nil, nil, nil); got != 401 {
		t.Errorf("pair token claimed by another machine: %d", got)
	}
	req, _ := http.NewRequest("GET", s.url+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+s.pair)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 401 {
		t.Errorf("pair token reached an owner route: %v %v", res.StatusCode, err)
	}
	// A remote Host without the trusted https proxy is refused before any peer check.
	req, _ = http.NewRequest("GET", s.url+"/api/sessions", nil)
	req.Host = "hostb.example.ts.net"
	req.Header.Set(peers.HeaderToken, s.pair)
	req.Header.Set(peers.HeaderFrom, "hosta")
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 403 {
		t.Errorf("remote host: %v %v", res.StatusCode, err)
	}
}

// A peer forwarding a read for its OWN local operator (Python's relay-invites/autonomy
// polling: on_behalf "operator") reaches the ordinary mux like any other peer call, and gets a
// clean 404 for a route Go does not implement yet - not an on-behalf 403 (tmx-lead's #103:
// "answer 404, not 401, or work").
//
// Hard condition (rule 1, tmx-lead): on-behalf is purely informational (whom the peer acts for
// on ITS OWN machine) and never changes the caller here - it is always fleet.PeerCaller, never
// Owner, whatever on-behalf says. So (a) the owner driving Hostb's agents from Hosta's
// dashboard (relayed with on_behalf=operator) gets agent operations only (launch/screen work),
// never owner-only settings (gate/PIN/devices still 403); (b) Hosta relaying its OWN agents'
// or devices' reads (on_behalf=agent:<name> or device:<id>, tmx-lead's 3 Nov P1: this broke
// /api/health, /api/sessions, relay-invites polling) keeps working the same way.
func TestPeerOnBehalfOperatorReachesMuxAgentRelayStillRefused(t *testing.T) {
	s := newPeerSetup(t)
	operator := map[string]string{peers.HeaderOnBehalf: "operator"}

	if code := peerDo(t, s.url, "GET", "/api/pending", "hosta", s.pair, operator, nil, nil); code != 404 {
		t.Errorf("on-behalf operator, unimplemented route /api/pending: %d, want 404", code)
	}
	if code := peerDo(t, s.url, "GET", "/api/autonomy", "hosta", s.pair, operator, nil, nil); code != 404 {
		t.Errorf("on-behalf operator, unimplemented route /api/autonomy: %d, want 404", code)
	}

	// on_behalf=operator never becomes Owner: owner-only routes still 403.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/gate/pin"}, {"GET", "/api/devices"}, {"PUT", "/api/safety/guard"}, {"GET", "/api/config"},
	} {
		if code := peerDo(t, s.url, tc.method, tc.path, "hosta", s.pair, operator, nil, nil); code != 403 {
			t.Errorf("on-behalf operator on owner-only %s %s: %d, want 403", tc.method, tc.path, code)
		}
	}

	// on_behalf=operator still gets agent operations, like any full peer.
	var launched model.Agent
	if code := peerDo(t, s.url, "POST", "/api/agents", "hosta", s.pair, operator,
		map[string]string{"name": "via-operator", "mode": "terminal"}, &launched); code != 201 {
		t.Fatalf("on-behalf operator launch: %d", code)
	}
	if p := s.d.Term.Last(launched.ID); p != nil {
		p.SetReplay([]byte("hello from the terminal"))
	}
	if code := peerDo(t, s.url, "GET", "/api/agents/"+launched.ID+"/screen", "hosta", s.pair, operator, nil, nil); code != 200 {
		t.Errorf("on-behalf operator screen: %d, want 200", code)
	}

	// on_behalf=agent:<name> and device:<id> are peer calls too (tmx-lead's P1): 404 on an
	// unimplemented route, never the on-behalf 403 that broke Hosta's relayed reads.
	if code := peerDo(t, s.url, "GET", "/api/pending", "hosta", s.pair,
		map[string]string{peers.HeaderOnBehalf: "agent:x@hosta"}, nil, nil); code != 404 {
		t.Errorf("on-behalf agent relay: %d, want 404", code)
	}
	if code := peerDo(t, s.url, "GET", "/api/pending", "hosta", s.pair,
		map[string]string{peers.HeaderOnBehalf: "device:x"}, nil, nil); code != 404 {
		t.Errorf("on-behalf device relay: %d, want 404", code)
	}

	// Never Owner, whatever on-behalf says: still 403 on an owner-only route.
	if code := peerDo(t, s.url, "GET", "/api/gate/pin", "hosta", s.pair,
		map[string]string{peers.HeaderOnBehalf: "agent:x@hosta"}, nil, nil); code != 403 {
		t.Errorf("on-behalf agent relay on owner-only route: %d, want 403", code)
	}
}

func TestPeerSessionsAndHealth(t *testing.T) {
	s := newPeerSetup(t)
	var out struct {
		Host     string           `json:"host"`
		Commit   *string          `json:"commit"`
		Sessions []map[string]any `json:"sessions"`
	}
	if code := peerDo(t, s.url, "GET", "/api/sessions", "hosta", s.pair, nil, nil, &out); code != 200 || out.Host != "hostb" || out.Commit == nil {
		t.Fatalf("%d %+v", code, out)
	}
	found := false
	for _, row := range out.Sessions {
		if row["name"] == "head" && row["host"] == "hostb" {
			found = true
		}
	}
	if !found {
		t.Fatalf("head not listed: %+v", out.Sessions)
	}
	var health map[string]any
	if code := peerDo(t, s.url, "GET", "/api/health", "", "", nil, nil, &health); code != 200 || health["host"] != "hostb" {
		t.Fatalf("health %d %v", code, health)
	}
}

func TestPeerMailBothWays(t *testing.T) {
	s := newPeerSetup(t)
	owner := client{t: t, base: s.url, token: s.d.OwnerToken}

	// Spoofing another machine's agent, a damaged body and a bad ID are refused.
	spoof := incoming("0123456789ab", "hi", "head")
	spoof["sender_host"] = "hostc"
	damaged := incoming("0123456789ab", "hi", "head")
	damaged["sha256"] = sum("other")
	badID := incoming("NOT-HEX", "hi", "head")
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{"spoof": {spoof, 403}, "damaged": {damaged, 409}, "bad id": {badID, 400}, "no agent": {incoming("0123456789ac", "hi", "nobody"), 404}} {
		if got := peerDo(t, s.url, "POST", "/api/messages/incoming", "hosta", s.pair, nil, tc.body, nil); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}

	// Delivered with the wire ID echoed; the same ID again is the same message.
	in := incoming("0123456789ab", "please review PR 104", "head")
	var res map[string]string
	if code := peerDo(t, s.url, "POST", "/api/messages/incoming", "hosta", s.pair, nil, in, &res); code != 201 || res["id"] != "0123456789ab" {
		t.Fatalf("incoming %d %v", code, res)
	}
	if code := peerDo(t, s.url, "POST", "/api/messages/incoming", "hosta", s.pair, nil, in, &res); code != 201 || res["id"] != "0123456789ab" {
		t.Fatalf("retry %d %v", code, res)
	}
	changed := incoming("0123456789ab", "a different body", "head")
	if code := peerDo(t, s.url, "POST", "/api/messages/incoming", "hosta", s.pair, nil, changed, nil); code != 409 {
		t.Fatalf("same id, other body: %d", code)
	}
	var msgs []model.Message
	if code := owner.do("GET", "/api/messages?agent=head", nil, &msgs); code != 200 {
		t.Fatal(code)
	}
	var local model.Message
	n := 0
	for _, m := range msgs {
		if m.From == fleet.PeerParty("tmx-lead", "hosta") {
			local, n = m, n+1
		}
	}
	if n != 1 || local.Body == "" {
		t.Fatalf("peer mail stored %d times: %+v", n, msgs)
	}

	// The owner answers it: out to hosta with the original wire ID as reply_to.
	var sent model.Message
	if code := owner.do("POST", "/api/messages", map[string]string{"body": "done", "reply_to": local.ID}, &sent); code != 200 && code != 201 {
		t.Fatalf("reply %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(s.py.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := s.py.got()
	if len(got) != 1 {
		t.Fatalf("hosta got %d messages", len(got))
	}
	m := got[0]
	if m.To != "tmx-lead" || m.SenderHost != "hostb" || m.Body != "done" || m.ReplyTo == nil || *m.ReplyTo != "0123456789ab" || m.SHA256 != sum("done") {
		t.Fatalf("wire message: %+v", m)
	}
	if strings.ContainsAny(m.ID, "ghijklmnopqrstuvwxyz") || len(m.ID) < 12 {
		t.Fatalf("wire id %q is not hex", m.ID)
	}

	// The owner's view of the peers.
	var st struct {
		Machine string         `json:"machine"`
		Peers   []peers.Status `json:"peers"`
	}
	if code := owner.do("GET", "/api/peers", nil, &st); code != 200 || st.Machine != "hostb" || len(st.Peers) != 2 || st.Peers[0].Name != "hosta" || !st.Peers[0].Paired || st.Peers[1].Paired {
		t.Fatalf("peers %d %+v", code, st)
	}
}

// A rotation comes over the current pair token from the machine that generated it, the one whose
// name sorts first (the Python rule): hosta may rotate, hostc may not (it sorts after hostb).
func TestPeerPairTokenRotation(t *testing.T) {
	s := newPeerSetup(t)
	next := peers.NewPairToken()
	var fp map[string]string
	if code := peerDo(t, s.url, "POST", "/api/peers/pair-token", "hosta", s.pair, nil, map[string]string{"token": next, "role": "full"}, &fp); code != 200 || fp["fingerprint"] != peers.Fingerprint(next) {
		t.Fatalf("rotation %d %v", code, fp)
	}
	for _, tok := range []string{next, s.pair} { // the old one still works during the overlap
		if code := peerDo(t, s.url, "GET", "/api/sessions", "hosta", tok, nil, nil, nil); code != 200 {
			t.Fatalf("after rotation: %d", code)
		}
	}
	vt := peers.NewPairToken()
	paths := config.Paths{Home: s.d.Home}
	if _, err := peers.NewTokens(paths.PeersFile()).Store("hostc", vt, "python-import"); err != nil {
		t.Fatal(err)
	}
	if code := peerDo(t, s.url, "POST", "/api/peers/pair-token", "hostc", vt, nil, map[string]string{"token": peers.NewPairToken()}, nil); code != 403 {
		t.Fatalf("hostc rotated the token hostb generates: %d", code)
	}
	// Owner material (go-reviewer-o/tmx-lead, 3 Nov): rotating the pair token is the peer
	// machine's own act, never a relayed agent, device or operator.
	for _, ob := range []string{"agent:x@hosta", "device:x", "operator"} {
		if code := peerDo(t, s.url, "POST", "/api/peers/pair-token", "hosta", s.pair,
			map[string]string{peers.HeaderOnBehalf: ob}, map[string]string{"token": peers.NewPairToken()}, nil); code != 403 {
			t.Errorf("pair-token rotation on-behalf %q: %d, want 403", ob, code)
		}
	}
}

// The owner PIN between machines (Python's Ship 1 protocol): a peer pushes its newer record,
// reads ours; nobody else reaches the record; a PIN set here is pushed to the peers.
func TestPeerOwnerPINSync(t *testing.T) {
	s := newPeerSetup(t)
	var got struct {
		Record map[string]any `json:"record"`
	}
	if code := peerDo(t, s.url, "GET", "/api/gate/pin/record", "hosta", s.pair, nil, nil, &got); code != 200 || got.Record != nil {
		t.Fatalf("empty record: %d %v", code, got.Record)
	}
	// GET also reveals owner material (the scrypt hash): machine-only too, same as POST.
	for _, ob := range []string{"agent:x@hosta", "device:x", "operator"} {
		if code := peerDo(t, s.url, "GET", "/api/gate/pin/record", "hosta", s.pair,
			map[string]string{peers.HeaderOnBehalf: ob}, nil, nil); code != 403 {
			t.Errorf("GET pin/record on-behalf %q: %d, want 403", ob, code)
		}
	}
	// Vector 1 (confirmed against Python): PIN 123456.
	rec := map[string]any{"salt": "000102030405060708090a0b0c0d0e0f", "hash": "53e59f1f9f56c2317d99f7a1e76fd1d43677ccb19c2c125e054f9a81281e0aec", "set": float64(time.Now().Unix())}
	for _, tc := range []struct {
		name, from, token string
		hdr               map[string]string
		body              any
		want              int
	}{
		{"no token", "hosta", "", nil, map[string]any{"record": rec}, 401},
		{"owner token", "hosta", s.d.OwnerToken, nil, map[string]any{"record": rec}, 401},
		{"a browser", "hosta", s.pair, map[string]string{"Origin": "https://evil.example"}, map[string]any{"record": rec}, 403},
		// Owner material (go-reviewer-o/tmx-lead, 3 Nov): this route pushes the owner's PIN
		// record, so unlike an ordinary relayed read, only the peer machine itself may call
		// it (no on-behalf, or "daemon") - never a relayed agent, device or operator, however
		// that peer authenticated.
		{"a peer relaying for one of its own agents", "hosta", s.pair, map[string]string{peers.HeaderOnBehalf: "agent:x@hosta"}, map[string]any{"record": rec}, 403},
		{"a peer relaying for one of its own devices", "hosta", s.pair, map[string]string{peers.HeaderOnBehalf: "device:x"}, map[string]any{"record": rec}, 403},
		{"a peer relaying for its operator", "hosta", s.pair, map[string]string{peers.HeaderOnBehalf: "operator"}, map[string]any{"record": rec}, 403},
		{"the machine itself, on-behalf daemon", "hosta", s.pair, map[string]string{peers.HeaderOnBehalf: "daemon"}, map[string]any{"record": rec}, 200},
		{"malformed", "hosta", s.pair, nil, map[string]any{"record": map[string]any{"salt": "00", "hash": "11", "set": 1}}, 400},
		{"the peer", "hosta", s.pair, nil, map[string]any{"record": rec}, 200},
	} {
		if code := peerDo(t, s.url, "POST", "/api/gate/pin/record", tc.from, tc.token, tc.hdr, tc.body, nil); code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, code, tc.want)
		}
	}
	if code := peerDo(t, s.url, "GET", "/api/gate/pin/record", "hosta", s.pair, nil, nil, &got); code != 200 || got.Record["hash"] != rec["hash"] {
		t.Fatalf("adopted record: %v", got.Record)
	}
	// An older record never replaces a newer one.
	old := map[string]any{"salt": "ffeeddccbbaa99887766554433221100", "hash": "b2f4376c5db628fa24d3e8a0e087a2e838653543d225e400a76b78608611502c", "set": 1.0}
	var res map[string]bool
	peerDo(t, s.url, "POST", "/api/gate/pin/record", "hosta", s.pair, nil, map[string]any{"record": old}, &res)
	if res["adopted"] {
		t.Fatal("older record adopted")
	}
	// The owner at the machine sets a new PIN (the old one is not needed there): pushed to hosta.
	b, _ := json.Marshal(map[string]string{"pin": "24681357"})
	req, _ := http.NewRequest("POST", s.url+"/api/gate/pin", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+s.d.OwnerToken)
	req.Header.Set("X-Tmx-UI-Session", strings.Repeat("b", 64))
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("set at the machine: %v %v", res.StatusCode, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.py.mu.Lock()
		n := len(s.py.pins)
		s.py.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PIN not pushed to the peer")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.py.mu.Lock()
	pushed, _ := s.py.pins[0]["record"].(map[string]any)
	s.py.mu.Unlock()
	if len(pushed) != 3 || len(pushed["salt"].(string)) != 32 || len(pushed["hash"].(string)) != 64 || pushed["set"].(float64) <= rec["set"].(float64)-1 {
		t.Fatalf("pushed %v", pushed)
	}
}

// The summary route between our own machines (summaries.via_peer / serve_peers). Serving: only with the peer's pair
// token, only the peers this daemon serves, and the generate goes to its own loopback Ollama. Asking: a peer still
// on the Python daemon has no such route, and the reason says so.
func TestPeerSummarizeLocal(t *testing.T) {
	s := newPeerSetup(t)
	var gens int
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gens++
		_, _ = w.Write([]byte(`{"response":"{\"summary\":\"ok\"}"}`))
	}))
	t.Cleanup(ollama.Close)
	in := summarize.PeerGenerate{Model: summarize.DefaultLocalModel, Prompt: "Transcript: hi"}
	path := "/api/peer/summarize-local"

	s.d.SetSummaries(config.SummaryConfig{LocalURL: ollama.URL})
	if c := peerDo(t, s.url, "POST", path, "hosta", s.pair, nil, in, nil); c != 403 {
		t.Fatalf("a peer this daemon does not serve: %d", c)
	}
	s.d.SetSummaries(config.SummaryConfig{LocalURL: ollama.URL, ServePeers: []string{"hosta"}})
	if c := peerDo(t, s.url, "POST", path, "hosta", "", nil, in, nil); c != 401 {
		t.Fatalf("without the pair token: %d", c)
	}
	if c := peerDo(t, s.url, "POST", path, "hosta", s.d.OwnerToken, nil, in, nil); c != 401 {
		t.Fatalf("the owner token is not a peer token: %d", c)
	}
	if c := peerDo(t, s.url, "POST", path, "hosta", s.pair, nil, summarize.PeerGenerate{Model: "llama3:70b", Prompt: "p"}, nil); c != 400 {
		t.Fatalf("another model: %d", c)
	}
	if gens != 0 {
		t.Fatalf("a refused request reached Ollama")
	}
	var out summarize.PeerAnswer
	if c := peerDo(t, s.url, "POST", path, "hosta", s.pair, nil, in, &out); c != 200 || !strings.Contains(out.Response, "ok") || gens != 1 {
		t.Fatalf("served: %d %+v (%d generates)", c, out, gens)
	}

	// asking: hosta is still the Python daemon (pyPeer answers 404 to anything it does not know)
	if _, err := s.pc.SummarizeLocal(t.Context(), "hosta", in); err == nil || !strings.Contains(err.Error(), "no summary route yet") {
		t.Fatalf("a Python peer: %v", err)
	}
}

// A full peer reaches the ordinary agent-operation routes, same standing as the owner there
// (tmx-lead, 3 Nov interop P0: launch/screen/type/keys/restart), but the hard boundary holds:
// it is NEVER owner for an owner-only route (gate/PIN, devices, pairing/invites, safety,
// secrets/accounts, update, config - every one gated on the literal Owner bool, untouched by
// this change). go-reviewer-o's block criterion for this PR.
func TestPeerReachesAgentOpsButNeverOwnerOnlyRoutes(t *testing.T) {
	s := newPeerSetup(t)

	var launched model.Agent
	if code := peerDo(t, s.url, "POST", "/api/agents", "hosta", s.pair, nil,
		map[string]string{"name": "peer-launched", "mode": "terminal"}, &launched); code != 201 {
		t.Fatalf("peer launch: %d", code)
	}
	if p := s.d.Term.Last(launched.ID); p != nil {
		p.SetReplay([]byte("hello from the terminal"))
	}
	if code := peerDo(t, s.url, "GET", "/api/agents/"+launched.ID+"/screen", "hosta", s.pair, nil, nil, nil); code != 200 {
		t.Fatalf("peer screen: %d", code)
	}
	if code := peerDo(t, s.url, "POST", "/api/agents/"+launched.ID+"/restart", "hosta", s.pair, nil, nil, nil); code != 200 {
		t.Fatalf("peer restart: %d", code)
	}

	// Owner-only routes: gate/PIN, devices, pairing/invites, safety, secrets/accounts, update,
	// config. A full peer gets 403 on every one of them, exactly like an agent would.
	ownerOnly := []struct{ method, path string }{
		{"GET", "/api/gate/pin"},
		{"POST", "/api/gate/pin"},
		{"GET", "/api/devices"},
		{"POST", "/api/devices/approve"},
		{"GET", "/api/safety"},
		{"PUT", "/api/safety/guard"},
		{"GET", "/api/gate"},
		{"PUT", "/api/gate"},
		{"DELETE", "/api/agents/" + launched.ID},
		{"GET", "/api/config"},
		{"PUT", "/api/config"},
		{"GET", "/api/p2p"},
		{"GET", "/api/system/tools"},
	}
	for _, tc := range ownerOnly {
		if code := peerDo(t, s.url, tc.method, tc.path, "hosta", s.pair, nil, nil, nil); code != 403 {
			t.Errorf("peer on owner-only %s %s: %d, want 403", tc.method, tc.path, code)
		}
	}
}
