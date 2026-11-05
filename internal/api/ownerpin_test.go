package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/ownerpin"
	"github.com/blackcode-ch/tmx-go/internal/peers"
)

// The owner PIN replaces every passkey in the browser (ownerpin.go). User story: as the owner,
// on any of my devices, I approve owner steps and pair my phone with ONE short PIN, the same
// as in the Python tmx, so that passkeys and token prompts never lock me out again.

const testPIN = "482915"

func (f *deviceFixture) setPIN(t *testing.T, pin string) {
	t.Helper()
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin", "localhost", map[string]string{"pin": pin}, nil, true), 200)
}

func (f *deviceFixture) confirmation(t *testing.T, v map[string]json.RawMessage) (id, method string) {
	t.Helper()
	var c struct{ ID, Method string }
	if err := json.Unmarshal(v["confirmation"], &c); err != nil {
		t.Fatal(err)
	}
	return c.ID, c.Method
}

func TestPINSetOnlyAtTheMachine(t *testing.T) {
	f := newDeviceFixture(t)
	cookie := f.paired(t)
	// A browser can't set the first PIN; a bad format is refused.
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin", deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin", "localhost", map[string]string{"pin": "12ab56"}, nil, true), 400)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin", "localhost", map[string]string{"pin": "12345"}, nil, true), 400)
	f.setPIN(t, testPIN)
	// A change from a browser needs the current PIN.
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin", deviceTestHost, map[string]string{"pin": "135790"}, cookie, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin", deviceTestHost, map[string]string{"pin": "135790", "old_pin": "000000"}, cookie, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin", deviceTestHost, map[string]string{"pin": "135790", "old_pin": testPIN}, cookie, false), 200)
	// The lost-PIN reset: the owner at the machine sets a new one without the old.
	f.setPIN(t, testPIN)
	v := deviceResponse(t, f.request(t, "GET", "/api/gate/pin", deviceTestHost, nil, cookie, false), 200)
	if string(v["set"]) != "true" || string(v["at_machine"]) != "false" || v["hash"] != nil || v["salt"] != nil {
		t.Fatalf("status leaked or wrong: %v", v)
	}
	// Without the owner's credentials nothing about the PIN is reachable.
	deviceResponse(t, f.request(t, "GET", "/api/gate/pin", deviceTestHost, nil, nil, false), 401)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin", "localhost", map[string]string{"pin": "111111"}, nil, false), 401)
	// The record is a peer route: a browser never gets it.
	if w := f.request(t, "GET", "/api/gate/pin/record", deviceTestHost, nil, cookie, false); w.Code == 200 {
		t.Fatal("browser read the PIN record")
	}
}

func TestOwnerActionWithPIN(t *testing.T) {
	f := newDeviceFixture(t)
	cookie := f.paired(t)
	other := f.paired(t)
	hostPrompts := len(f.auth.Prompts())
	payload := map[string]string{"value": "first"}
	// No PIN yet: the gate tells the browser so (never a passkey).
	v := deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, payload, cookie, false), 428)
	id, method := f.confirmation(t, v)
	if method != "pin" {
		t.Fatal("method", method)
	}
	deviceResponse(t, f.request(t, "POST", "/api/gate/approvals/"+id+"/pin", deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 409)
	f.setPIN(t, testPIN)
	v = deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, payload, cookie, false), 428)
	id, _ = f.confirmation(t, v)
	deviceResponse(t, f.request(t, "POST", "/api/gate/browser/"+id, deviceTestHost, map[string]string{"pin": "000000"}, cookie, false), 403)
	// Another paired device can't confirm this device's action, even with the PIN.
	deviceResponse(t, f.request(t, "POST", "/api/gate/browser/"+id, deviceTestHost, map[string]string{"pin": testPIN}, other, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/gate/browser/"+id, deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	deviceResponse(t, f.request(t, "POST", "/api/gate/browser/"+id, deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 403) // single use
	deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+id, otherDeviceHost, payload, cookie, false), 401)
	deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+id, deviceTestHost, map[string]string{"value": "changed"}, cookie, false), 428)
	deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+id, deviceTestHost, payload, cookie, false), 204)
	deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+id, deviceTestHost, payload, cookie, false), 428)
	// A confirmed action must be used within 5 minutes.
	v = deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, payload, cookie, false), 428)
	id, _ = f.confirmation(t, v)
	deviceResponse(t, f.request(t, "POST", "/api/gate/approvals/"+id+"/pin", deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	f.now = f.now.Add(5 * time.Minute)
	deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+id, deviceTestHost, payload, cookie, false), 428)
	if len(f.auth.Prompts()) != hostPrompts {
		t.Fatal("a browser action prompted the host OS")
	}
	// Revoking the device ends its confirmed action too.
	v = deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, payload, cookie, false), 428)
	id, _ = f.confirmation(t, v)
	deviceResponse(t, f.request(t, "POST", "/api/gate/browser/"+id, deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	f.hostRevoke(t, f.s.devices.state.Devices[0].ID)
	deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+id, deviceTestHost, payload, cookie, false), 401)
}

func TestPINSessionIdleAndFresh(t *testing.T) {
	f := newDeviceFixture(t)
	cookie := f.paired(t)
	f.setPIN(t, testPIN)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/session", deviceTestHost, map[string]string{"pin": "999999"}, cookie, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/session", deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, map[string]string{}, cookie, false), 204)
	f.now = f.now.Add(50 * time.Minute)
	deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, map[string]string{}, cookie, false), 204) // activity keeps it
	f.now = f.now.Add(61 * time.Minute)
	deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, map[string]string{}, cookie, false), 428) // idle 1 h
	// Max 12 h even when used all the time.
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/session", deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	for i := 0; i < 12; i++ {
		f.now = f.now.Add(59 * time.Minute)
		f.request(t, "POST", "/api/protected", deviceTestHost, map[string]string{}, cookie, false)
	}
	f.now = f.now.Add(59 * time.Minute)
	deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, map[string]string{}, cookie, false), 428)
	// A fresh-only action needs a PIN in the last 5 minutes.
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/session", deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	f.s.devices.mu.Lock()
	dev := f.s.devices.state.Devices[0].ID
	if !f.s.devices.pinSessionOK(dev, true) {
		t.Fatal("fresh right after the PIN")
	}
	f.now = f.now.Add(5 * time.Minute)
	if f.s.devices.pinSessionOK(dev, true) || !f.s.devices.pinSessionOK(dev, false) {
		t.Fatal("fresh window")
	}
	f.s.devices.mu.Unlock()
}

// QR -> PIN -> done: a new phone pairs itself with the owner PIN, no code at the host.
func TestPairPhoneWithPIN(t *testing.T) {
	f := newDeviceFixture(t)
	_, claim := f.start(t)
	deviceResponse(t, f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": testPIN}, claim, false), 409) // no PIN set yet
	f.setPIN(t, testPIN)
	prompts := len(f.auth.Prompts())
	deviceResponse(t, f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": testPIN}, nil, false), 400) // no pairing cookie
	deviceResponse(t, f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": "000000"}, claim, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false), 202)
	deviceResponse(t, f.request(t, "POST", "/api/pair/pin", otherDeviceHost, map[string]string{"pin": testPIN}, claim, false), 400) // other host
	deviceResponse(t, f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": testPIN}, claim, false), 200)
	w := f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false)
	deviceResponse(t, w, 200)
	cookie := responseCookie(t, w, deviceCookie)
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, cookie, false), 204)
	if len(f.auth.Prompts()) != prompts {
		t.Fatal("PIN pairing prompted the host OS")
	}
	// An expired pairing can't be approved.
	_, late := f.start(t)
	f.now = f.now.Add(pairTTL)
	deviceResponse(t, f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": testPIN}, late, false), 400)
}

// Wrong PINs slow down, never lock out for good; the owner at the machine clears it at once.
func TestPINBackoffNeverALockout(t *testing.T) {
	f := newDeviceFixture(t)
	f.setPIN(t, testPIN)
	cookie := f.paired(t)
	_, claim := f.start(t)
	for i := 0; i < 5; i++ {
		deviceResponse(t, f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": "000000"}, claim, false), 403)
	}
	w := f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": testPIN}, claim, false)
	v := deviceResponse(t, w, 429)
	if w.Header().Get("Retry-After") == "" || v["wait_s"] == nil {
		t.Fatal("no wait given")
	}
	// Unknown sources share a wait, but an already-paired phone remains able to sign in.
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/session", deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	// A browser can't clear the waits; the owner at the machine can.
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/clear", deviceTestHost, nil, cookie, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/clear", "localhost", nil, nil, true), 200)
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/session", deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	deviceResponse(t, f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": testPIN}, claim, false), 200)
	// And without clearing, the wait ends by itself.
	_, claim = f.start(t)
	for i := 0; i < 5; i++ {
		f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": "000000"}, claim, false)
	}
	f.now = f.now.Add(61 * time.Second)
	deviceResponse(t, f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": "000000"}, claim, false), 403)
}

func TestGateStatusSaysPIN(t *testing.T) {
	f := newDeviceFixture(t)
	r, _ := http.NewRequest("GET", "http://"+deviceTestHost+"/api/gate", nil)
	st := f.s.gateStatus(r)
	if st.Method != "pin" || st.Available {
		t.Fatalf("%+v", st)
	}
	f.setPIN(t, testPIN)
	if st = f.s.gateStatus(r); !st.Available || st.Unavailable != "" {
		t.Fatalf("%+v", st)
	}
}

// pullPIN adopts a peer's newer record (the 5-minute pull) and ignores an older one.
func TestPullPINFromPeer(t *testing.T) {
	now := time.Now()
	newer := map[string]any{"salt": "000102030405060708090a0b0c0d0e0f", "hash": "53e59f1f9f56c2317d99f7a1e76fd1d43677ccb19c2c125e054f9a81281e0aec", "set": float64(now.Unix())}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/gate/pin/record" || r.Header.Get(peers.HeaderOnBehalf) != "daemon" || r.Header.Get(peers.HeaderToken) == "" {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"record": newer})
	}))
	defer srv.Close()
	home := t.TempDir()
	cfg := config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{{Name: "hosta", URL: srv.URL}}}
	pc := peers.New(cfg, config.Paths{Home: home}, nil)
	pc.SetTransport(srv.Client().Transport)
	if _, err := pc.Tokens().Store("hosta", peers.NewPairToken(), "test"); err != nil {
		t.Fatal(err)
	}
	s := &Server{peers: pc, pin: ownerpin.Open(filepath.Join(home, "secrets", "owner-pin.json")), log: slog.New(slog.DiscardHandler)}
	s.pin.Now = func() time.Time { return now.Add(-time.Hour) }
	if _, err := s.pin.Set("999999"); err != nil { // set an hour ago: the peer's is newer
		t.Fatal(err)
	}
	s.pin.Now = func() time.Time { return now }
	s.pullPIN(context.Background())
	if s.pin.Verify("x", "123456") != nil {
		t.Fatal("newer peer record not adopted")
	}
	newer["set"] = 1.0
	newer["hash"] = "00" + newer["hash"].(string)[2:]
	s.pullPIN(context.Background())
	if s.pin.Verify("x", "123456") != nil {
		t.Fatal("older record replaced ours")
	}
}

// Revoke needs the owner's fresh proof (audit 3 Nov): from a browser the PIN, at the machine
// the host gate. A device may always revoke itself.
func TestRevokeNeedsPIN(t *testing.T) {
	f := newDeviceFixture(t)
	f.setPIN(t, testPIN)
	phone := f.paired(t)
	laptop := f.paired(t)
	target := f.s.devices.state.Devices[0].ID // the phone
	v := deviceResponse(t, f.request(t, "DELETE", "/api/devices/"+target, deviceTestHost, nil, laptop, false), 428)
	id, method := f.confirmation(t, v)
	if method != "pin" || len(f.s.devices.state.Devices) != 2 {
		t.Fatal("revoked without the PIN")
	}
	deviceResponse(t, f.request(t, "POST", "/api/gate/approvals/"+id+"/pin", deviceTestHost, map[string]string{"pin": "000000"}, laptop, false), 403)
	deviceResponse(t, f.request(t, "DELETE", "/api/devices/"+target+"?confirmation="+id, deviceTestHost, nil, laptop, false), 428)
	deviceResponse(t, f.request(t, "POST", "/api/gate/approvals/"+id+"/pin", deviceTestHost, map[string]string{"pin": testPIN}, laptop, false), 200)
	deviceResponse(t, f.request(t, "DELETE", "/api/devices/"+target+"?confirmation="+id, deviceTestHost, nil, laptop, false), 204)
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, phone, false), 401)
	// The laptop gives itself up without a PIN.
	deviceResponse(t, f.request(t, "DELETE", "/api/devices/"+f.s.devices.state.Devices[0].ID, deviceTestHost, nil, laptop, false), 204)
	if len(f.s.devices.state.Devices) != 0 {
		t.Fatal("self-revoke")
	}
}

// Review of #146: a burst of parallel wrong PINs on the public pairing route gets at most 5
// checked guesses before the wait; the rest are told to wait, unchecked.
func TestParallelPairGuesses(t *testing.T) {
	f := newDeviceFixture(t)
	f.setPIN(t, testPIN)
	_, claim := f.start(t)
	var wg sync.WaitGroup
	var wrong, waited atomic.Int32
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch f.request(t, "POST", "/api/pair/pin", deviceTestHost, map[string]string{"pin": "000000"}, claim, false).Code {
			case 403:
				wrong.Add(1)
			case 429:
				waited.Add(1)
			}
		}()
	}
	wg.Wait()
	if wrong.Load() > 5 || wrong.Load()+waited.Load() != 60 {
		t.Fatalf("checked %d, waited %d", wrong.Load(), waited.Load())
	}
}

// A local client's UI nonce is its own choice: a fresh one per guess is never a fresh source.
func TestLocalPINSourceIgnoresTheNonce(t *testing.T) {
	src := func(nonce string) string {
		r, _ := http.NewRequest("POST", "http://localhost/api/gate/confirmations/x/pin", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set(UISessionHeader, nonce)
		return pinSource(r, "")
	}
	if a, b := src(strings.Repeat("a", 64)), src(strings.Repeat("b", 64)); a != b || a != "local" {
		t.Fatalf("local sources %q, %q", a, b)
	}
}

// Rotating UI nonces and restarting the store cannot replenish the guess budget.
func TestPINFreshNoncesStayThrottledAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-pin.json")
	now := time.Now()
	s := &Server{pin: ownerpin.Open(path), log: slog.New(slog.DiscardHandler)}
	s.pin.Now = func() time.Time { return now }
	if _, err := s.pin.Set(testPIN); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if i == 10 {
			s = &Server{pin: ownerpin.Open(path), log: slog.New(slog.DiscardHandler)}
			s.pin.Now = func() time.Time { return now }
		}
		r := httptest.NewRequest("POST", "http://localhost/api/gate/confirmations/x/pin", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set(UISessionHeader, fmt.Sprintf("%064x", i+1))
		w := httptest.NewRecorder()
		if s.verifyPIN(w, pinSource(r, ""), "000000") {
			t.Fatal("wrong PIN accepted")
		}
		want := http.StatusTooManyRequests
		if i < ownerpin.GlobalFree {
			want = http.StatusForbidden
		}
		if w.Code != want {
			t.Fatalf("guess %d: status %d, want %d", i+1, w.Code, want)
		}
		if want == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Fatalf("guess %d: missing retry delay", i+1)
		}
	}
	// A different source after restart must share the persisted machine-wide wait.
	w := httptest.NewRecorder()
	if s.verifyPIN(w, "pair:another-host", testPIN) || w.Code != http.StatusTooManyRequests {
		t.Fatalf("fresh source bypassed machine backoff: %d", w.Code)
	}
}

// The phone keeps the same PIN-only session request, isolated from unpaired attackers.
func TestPairedPhonePINSignInIsolatedAndFinite(t *testing.T) {
	f := newDeviceFixture(t)
	f.setPIN(t, testPIN)
	phone := f.paired(t)
	other := f.paired(t)
	signin := func(cookie *http.Cookie, pin string, want int) {
		t.Helper()
		deviceResponse(t, f.request(t, "POST", "/api/gate/pin/session", deviceTestHost, map[string]string{"pin": pin}, cookie, false), want)
	}
	restart := func() {
		f.s.pin = ownerpin.Open(filepath.Join(f.paths.SecretsDir(), "owner-pin.json"))
		f.s.pin.Now = func() time.Time { return f.now }
	}
	signin(phone, testPIN, 200)
	for i := 0; i < 20; i++ {
		if i == 10 {
			restart()
		}
		r := httptest.NewRequest("POST", "http://localhost/api/pair/pin", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set(UISessionHeader, fmt.Sprintf("%064x", i+1))
		w := httptest.NewRecorder()
		f.s.verifyPIN(w, pinSource(r, ""), "000000")
		want := 403
		if i >= ownerpin.GlobalFree {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("unpaired guess %d: %d want %d", i, w.Code, want)
		}
		signin(phone, testPIN, 200) // This must not reset the attacker's persisted wait.
	}
	for i := 0; i < 3; i++ {
		signin(phone, "000000", 403)
	}
	f.now = f.now.Add(time.Minute)
	signin(phone, testPIN, 200)
	for i := 0; i < ownerpin.GlobalFree; i++ {
		signin(phone, "000000", 403)
	}
	signin(phone, testPIN, 429)
	signin(other, testPIN, 200)
	restart()
	signin(phone, testPIN, 429)
	signin(other, testPIN, 200)
	f.now = f.now.Add(ownerpin.GlobalFirstWait + time.Second)
	signin(phone, testPIN, 200)
}
