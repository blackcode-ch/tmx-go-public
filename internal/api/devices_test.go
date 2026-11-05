package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/ownerpin"
)

const deviceTestHost = "trial.example.ts.net:8443"
const otherDeviceHost = "other.example.ts.net:8443"

type deviceFixture struct {
	s       *Server
	handler http.Handler
	auth    *gate.Fake
	now     time.Time
	grace   string
	paths   config.Paths
}

func newDeviceFixture(t *testing.T) *deviceFixture {
	t.Helper()
	p := config.Paths{Home: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	fake := &gate.Fake{}
	g := gate.New(p, fake, nil, gate.FileMarker(filepath.Join(p.Home, "marker")))
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	f := &deviceFixture{now: time.Now(), auth: fake, paths: p}
	f.s = &Server{owner: "test-owner", port: 8787, gate: g, log: slog.New(slog.DiscardHandler), trustedHosts: []string{deviceTestHost, otherDeviceHost}, devices: newDeviceStore(filepath.Join(p.SecretsDir(), "devices.json")),
		pin: ownerpin.Open(filepath.Join(p.SecretsDir(), "owner-pin.json"))}
	f.s.devices.now = func() time.Time { return f.now }
	f.s.pin.Now = func() time.Time { return f.now }
	mux := http.NewServeMux()
	f.s.deviceRoutes(mux)
	f.s.gatePINRoutes(mux)
	mux.HandleFunc("PUT /api/gate", f.s.ownerOnly(f.s.putGate))
	mux.HandleFunc("PUT /api/gate/grace", f.s.ownerOnly(f.s.putGrace))
	mux.HandleFunc("POST /api/protected", f.s.ownerOnly(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if !decode(w, r, &payload) {
			return
		}
		if f.s.RequireOwner(w, r, "test owner action", payload) {
			w.WriteHeader(204)
		}
	}))
	mux.HandleFunc("GET /api/read", f.s.ownerOnly(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	mux.HandleFunc("POST /api/recordings/{id}/ticket", f.s.ownerOnly(f.s.recordingTicket))
	mux.HandleFunc("GET /api/recordings/{id}/segments/{n}", f.s.ownerOnly(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	f.handler = f.s.guard(mux)
	return f
}
func (f *deviceFixture) request(t *testing.T, method, path, host string, body any, cookie *http.Cookie, owner bool) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://"+host+path, bytes.NewReader(b))
	r.RemoteAddr = "127.0.0.1:4321"
	r.Header.Set(UISessionHeader, strings.Repeat("a", 64))
	r.Header.Set(GraceHeader, f.grace)
	if host != "localhost" {
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Origin", "https://"+host)
	}
	if owner {
		r.Header.Set("Authorization", "Bearer test-owner")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}
func deviceResponse(t *testing.T, w *httptest.ResponseRecorder, want int) map[string]json.RawMessage {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body)
	}
	v := map[string]json.RawMessage{}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
	}
	return v
}
func responseString(t *testing.T, v map[string]json.RawMessage, key string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(v[key], &s); err != nil {
		t.Fatal(err)
	}
	return s
}
func responseCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatal("missing cookie")
	return nil
}
func (f *deviceFixture) start(t *testing.T) (string, *http.Cookie) {
	w := f.request(t, "POST", "/api/pair/start", deviceTestHost, map[string]string{"name": "Test phone"}, nil, false)
	v := deviceResponse(t, w, 201)
	return responseString(t, v, "code"), responseCookie(t, w, pairingCookie)
}
func (f *deviceFixture) approve(t *testing.T, code string, want int) {
	t.Helper()
	body := map[string]string{"code": code}
	v := deviceResponse(t, f.request(t, "POST", "/api/devices/approve", "localhost", body, nil, true), 428)
	var c gate.Confirmation
	if err := json.Unmarshal(v["confirmation"], &c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, _ = f.s.gate.Wait(ctx, c.ID)
	if c.State != gate.StateConfirmed {
		t.Fatal("fake did not confirm")
	}
	if want == 200 {
		if strings.Contains(c.Reason, code) {
			t.Fatal("pairing code entered the logged confirmation reason")
		}
		prompts := f.auth.Prompts()
		if !strings.Contains(prompts[len(prompts)-1], code) || !strings.Contains(prompts[len(prompts)-1], deviceTestHost) {
			t.Fatal("authenticator prompt omitted pairing code/host")
		}
	}
	deviceResponse(t, f.request(t, "POST", "/api/devices/approve?confirmation="+c.ID, "localhost", body, nil, true), want)
}
func (f *deviceFixture) paired(t *testing.T) *http.Cookie {
	code, claim := f.start(t)
	f.approve(t, code, 200)
	w := f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false)
	deviceResponse(t, w, 200)
	return responseCookie(t, w, deviceCookie)
}
func TestDevicePairingPersistenceReplayAndRevocation(t *testing.T) {
	f := newDeviceFixture(t)
	code, claim := f.start(t)
	deviceResponse(t, f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false), 202)
	f.approve(t, code, 200)
	deviceResponse(t, f.request(t, "POST", "/api/pair/claim", otherDeviceHost, nil, claim, false), 400)
	deviceResponse(t, f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, nil, false), 400)
	w := f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false)
	v := deviceResponse(t, w, 200)
	cookie := responseCookie(t, w, deviceCookie)
	if !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" || cookie.MaxAge <= 0 || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie attributes")
	}
	deviceResponse(t, f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false), 400)
	deviceResponse(t, f.request(t, "GET", "/api/read", otherDeviceHost, nil, cookie, false), 401)
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, cookie, false), 204)
	f.s.devices = newDeviceStore(f.s.devices.path)
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, cookie, false), 204)
	req := httptest.NewRequest("GET", "http://"+deviceTestHost+"/api/read", nil)
	req.RemoteAddr = "127.0.0.1:42"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(cookie)
	ctx, cleanup, ok := f.s.deviceSession(req)
	if !ok {
		t.Fatal("missing active session")
	}
	defer cleanup()
	id := responseString(t, v, "id")
	f.hostRevoke(t, id)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel active connection")
	}
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, cookie, false), 401)
	f.s.devices = newDeviceStore(f.s.devices.path)
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, cookie, false), 401)
}
func TestDevicePairingExpiryAndRateLimit(t *testing.T) {
	f := newDeviceFixture(t)
	code, claim := f.start(t)
	f.now = f.now.Add(pairTTL)
	f.approve(t, code, 400)
	deviceResponse(t, f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false), 400)
	for i := 0; i < 10; i++ {
		f.start(t)
	}
	deviceResponse(t, f.request(t, "POST", "/api/pair/start", deviceTestHost, nil, nil, false), 429)
}

func TestDeviceMediaCannotOutliveRevocation(t *testing.T) {
	f := newDeviceFixture(t)
	cookie := f.paired(t)
	v := deviceResponse(t, f.request(t, "POST", "/api/recordings/test/ticket", deviceTestHost, nil, cookie, false), 200)
	if responseString(t, v, "ticket") != deviceMediaTicket {
		t.Fatal("paired device minted an independent media ticket")
	}
	path := "/api/recordings/test/segments/0?ticket=" + deviceMediaTicket
	deviceResponse(t, f.request(t, "GET", path, deviceTestHost, nil, nil, false), 401)
	deviceResponse(t, f.request(t, "GET", path, otherDeviceHost, nil, cookie, false), 401)
	deviceResponse(t, f.request(t, "GET", path, deviceTestHost, nil, cookie, false), 204)
	f.hostRevoke(t, f.s.devices.state.Devices[0].ID)
	deviceResponse(t, f.request(t, "GET", path, deviceTestHost, nil, cookie, false), 401)
}

func TestBrowserCannotChangeHostGate(t *testing.T) {
	f := newDeviceFixture(t)
	cookie := f.paired(t)
	prompts := len(f.auth.Prompts())
	code, _ := f.start(t)
	deviceResponse(t, f.request(t, "POST", "/api/devices/approve", deviceTestHost, map[string]string{"code": code}, cookie, false), 403)
	for _, enabled := range []bool{false, true} {
		deviceResponse(t, f.request(t, "PUT", "/api/gate", deviceTestHost, map[string]bool{"enabled": enabled}, cookie, false), 403)
	}
	for _, minutes := range []int{0, 120} {
		deviceResponse(t, f.request(t, "PUT", "/api/gate/grace", deviceTestHost, map[string]int{"grace_minutes": minutes}, cookie, false), 403)
	}
	if !f.s.gate.Enabled() || len(f.auth.Prompts()) != prompts {
		t.Fatal("remote request changed or prompted the host gate")
	}
}

// hostRevoke revokes a device at the machine: the host gate (the fake) answers the 428.
func (f *deviceFixture) hostRevoke(t *testing.T, id string) {
	t.Helper()
	path := "/api/devices/" + id
	v := deviceResponse(t, f.request(t, "DELETE", path, "localhost", nil, nil, true), 428)
	var c gate.Confirmation
	if err := json.Unmarshal(v["confirmation"], &c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, _ = f.s.gate.Wait(ctx, c.ID); c.State != gate.StateConfirmed {
		t.Fatal("host proof failed")
	}
	deviceResponse(t, f.request(t, "DELETE", path+"?confirmation="+c.ID, "localhost", nil, nil, true), 204)
}
