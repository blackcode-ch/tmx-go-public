package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/gate"
)

func (f *deviceFixture) bootstrap(t *testing.T) string {
	t.Helper()
	w := f.request(t, "POST", "/api/devices/bootstrap", "localhost", map[string]string{"host": deviceTestHost}, nil, true)
	if w.Code == 428 {
		var v struct {
			Confirmation gate.Confirmation `json:"confirmation"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c, _ := f.s.gate.Wait(ctx, v.Confirmation.ID)
		if c.State != gate.StateConfirmed {
			t.Fatal("host proof failed")
		}
		w = f.request(t, "POST", "/api/devices/bootstrap?confirmation="+c.ID, "localhost", map[string]string{"host": deviceTestHost}, nil, true)
	}
	deviceResponse(t, w, 201)
	return f.privateBootstrapToken(t, w)
}

func (f *deviceFixture) privateBootstrapToken(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	// This is synthetic state under t.TempDir, never a real host credential.
	b, err := os.ReadFile(filepath.Join(filepath.Dir(f.s.devices.path), "owner-bootstrap-link"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(strings.TrimSpace(string(b)))
	if err != nil || u.Scheme != "https" || u.Host != deviceTestHost || u.Path != "/owner-bootstrap" {
		t.Fatal("bad private link")
	}
	q, _ := url.ParseQuery(u.Fragment)
	token := q.Get("bootstrap")
	if len(token) != 64 || strings.Contains(w.Body.String(), token) {
		t.Fatal("missing or exposed capability")
	}
	return token
}

func (f *deviceFixture) exchange(t *testing.T, token string) *http.Cookie {
	t.Helper()
	w := f.request(t, "POST", "/api/pair/bootstrap/exchange", deviceTestHost, map[string]string{"token": token}, nil, false)
	deviceResponse(t, w, 200)
	c := responseCookie(t, w, bootstrapCookie)
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge > 600 {
		t.Fatal("unsafe bootstrap cookie")
	}
	return c
}

func TestOwnerBootstrapOnePairOnly(t *testing.T) {
	f := newDeviceFixture(t)
	token := f.bootstrap(t)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/exchange", otherDeviceHost, map[string]string{"token": token}, nil, false), 403)
	c := f.exchange(t, token)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/exchange", deviceTestHost, map[string]string{"token": token}, nil, false), 403)
	for _, path := range []string{"/api/read", "/api/devices"} {
		deviceResponse(t, f.request(t, "GET", path, deviceTestHost, nil, c, false), 401)
	}
	for _, path := range []string{"/api/gate", "/api/gate/grace"} {
		deviceResponse(t, f.request(t, "PUT", path, deviceTestHost, map[string]bool{"enabled": false}, c, false), 401)
	}
	code, claim := f.start(t)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/approve", otherDeviceHost, map[string]string{"code": code}, c, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/approve", deviceTestHost, map[string]string{"code": "INVALID"}, c, false), 400)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/approve", deviceTestHost, map[string]string{"code": code}, c, false), 200)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/approve", deviceTestHost, map[string]string{"code": code}, c, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, nil, false), 400)
	w := f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false)
	deviceResponse(t, w, 200)
	device := responseCookie(t, w, deviceCookie)
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, device, false), 204)
	if len(f.auth.Prompts()) != 1 {
		t.Fatal("bootstrap did not require exactly one host proof at issuance")
	}
}

func TestOwnerBootstrapExpiryReissueAndRestart(t *testing.T) {
	for _, mode := range []string{"link expiry", "session expiry", "reissue", "restart"} {
		t.Run(mode, func(t *testing.T) {
			f := newDeviceFixture(t)
			token := f.bootstrap(t)
			if mode == "link expiry" {
				f.now = f.now.Add(bootstrapTTL)
				deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/exchange", deviceTestHost, map[string]string{"token": token}, nil, false), 403)
				return
			}
			f.now = f.now.Add(9 * time.Minute)
			c := f.exchange(t, token)
			switch mode {
			case "session expiry":
				f.now = f.now.Add(time.Minute)
			case "reissue":
				f.bootstrap(t)
			case "restart":
				f.s.devices = newDeviceStore(f.s.devices.path)
			}
			code, _ := f.start(t)
			deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/approve", deviceTestHost, map[string]string{"code": code}, c, false), 403)
		})
	}
}

func TestOwnerBootstrapConcurrentApproval(t *testing.T) {
	f := newDeviceFixture(t)
	c := f.exchange(t, f.bootstrap(t))
	first, _ := f.start(t)
	second, _ := f.start(t)
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, code := range []string{first, second} {
		wg.Add(1)
		go func(code string) {
			defer wg.Done()
			statuses <- f.request(t, "POST", "/api/pair/bootstrap/approve", deviceTestHost, map[string]string{"code": code}, c, false).Code
		}(code)
	}
	wg.Wait()
	a, b := <-statuses, <-statuses
	if !(a == 200 && b == 403 || a == 403 && b == 200) {
		t.Fatalf("concurrent approvals: %d %d", a, b)
	}
}

func TestOwnerBootstrapHostOnlyIssuanceAndCSRF(t *testing.T) {
	f := newDeviceFixture(t)
	deviceResponse(t, f.request(t, "POST", "/api/devices/bootstrap", deviceTestHost, map[string]string{"host": deviceTestHost}, nil, true), 403)
	deviceResponse(t, f.request(t, "POST", "/api/devices/bootstrap", "localhost", map[string]string{"host": deviceTestHost}, nil, false), 401)
	deviceResponse(t, f.request(t, "POST", "/api/devices/bootstrap", "localhost", map[string]string{"host": "untrusted.example"}, nil, true), 400)
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("Origin", "https://wrong.example") },
		func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") },
		func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1" },
	} {
		r := httptest.NewRequest("POST", "http://localhost/api/devices/bootstrap", strings.NewReader(`{"host":"`+deviceTestHost+`"}`))
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Authorization", "Bearer test-owner")
		mutate(r)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		deviceResponse(t, w, 403)
	}
	token := f.bootstrap(t)
	for _, origin := range []string{"", "https://wrong.example"} {
		r := httptest.NewRequest("POST", "https://"+deviceTestHost+"/api/pair/bootstrap/exchange", strings.NewReader(`{"token":"`+token+`"}`))
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("X-Forwarded-Proto", "https")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		deviceResponse(t, w, 403)
	}
	f.exchange(t, token) // rejected attempts did not consume it
}

func (f *deviceFixture) hostProof(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	w := f.request(t, "POST", path, "localhost", body, nil, true)
	v := deviceResponse(t, w, 428)
	var confirmation gate.Confirmation
	if err := json.Unmarshal(v["confirmation"], &confirmation); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, _ := f.s.gate.Wait(ctx, confirmation.ID)
	if c.State != gate.StateConfirmed {
		t.Fatal("host proof failed")
	}
	r := httptest.NewRequest("POST", "http://localhost"+path, nil)
	r.Header.Set("Authorization", "Bearer test-owner")
	r.Header.Set(UISessionHeader, strings.Repeat("a", 64))
	_, f.grace, _ = f.s.gate.SessionProof(c.ID, gateCaller(r))
	return f.request(t, "POST", path+"?confirmation="+c.ID, "localhost", body, nil, true)
}

func TestOwnerBootstrapRecoveryWithoutHelloOrPasskeys(t *testing.T) {
	f := newDeviceFixture(t)
	// The desktop proved itself before Hello failed; the root token alone is not
	// this surviving in-memory UI credential.
	deviceResponse(t, f.hostProof(t, "/api/devices/bootstrap", map[string]any{"host": deviceTestHost}), 201)
	deviceResponse(t, f.request(t, "POST", "/api/devices/bootstrap", "localhost", map[string]any{"host": deviceTestHost, "recovery": true}, nil, true), 403)
	f.auth.SetUnavailable(errors.New("host authenticator unavailable"))
	prompts := len(f.auth.Prompts())
	gatePath := filepath.Join(filepath.Dir(f.s.devices.path), "gate.json")
	before, err := os.ReadFile(gatePath)
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	f.s.recoveryNotice = func(host string, expires time.Time) error {
		notices++
		if host != deviceTestHost || !expires.Equal(f.now.Add(bootstrapTTL)) {
			t.Fatal("wrong notice")
		}
		return nil
	}
	body := map[string]any{"host": deviceTestHost, "recovery": true}
	credential := f.grace
	f.grace = ""
	deviceResponse(t, f.request(t, "POST", "/api/devices/bootstrap", "localhost", body, nil, true), 403)
	f.grace = strings.Repeat("b", 64)
	deviceResponse(t, f.request(t, "POST", "/api/devices/bootstrap", "localhost", body, nil, true), 403)
	f.grace = credential
	w := f.request(t, "POST", "/api/devices/bootstrap", "localhost", body, nil, true)
	deviceResponse(t, w, 201)
	if notices != 1 {
		t.Fatal("recovery was not loud")
	}
	c := f.exchange(t, f.privateBootstrapToken(t, w))
	code, claim := f.start(t)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/approve", deviceTestHost, map[string]string{"code": code}, c, false), 200)
	w = f.request(t, "POST", "/api/pair/claim", deviceTestHost, nil, claim, false)
	deviceResponse(t, w, 200)
	device := responseCookie(t, w, deviceCookie)
	if device.MaxAge > 600 {
		t.Fatal("recovery session outlives original deadline")
	}
	for _, path := range []string{"/api/gate/pin/session", "/api/protected", "/api/approvals/anything/decide", "/api/devices/bootstrap"} {
		deviceResponse(t, f.request(t, "POST", path, deviceTestHost, body, device, false), 403)
	}
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, device, false), 403)
	deviceResponse(t, f.request(t, "GET", "/api/devices/recovery", deviceTestHost, nil, device, false), 200)
	// Restart preserves the restricted flag; it does not promote the device.
	prior := f.s.devices
	f.s.devices = newDeviceStore(prior.path)
	f.s.devices.now = prior.now
	deviceResponse(t, f.request(t, "POST", "/api/gate/pin/session", deviceTestHost, map[string]string{"pin": testPIN}, device, false), 403)
	deadline := f.now
	f.now = f.now.Add(bootstrapTTL)
	deviceResponse(t, f.request(t, "GET", "/api/devices/recovery", deviceTestHost, nil, device, false), 401)
	f.now = deadline
	after, err := os.ReadFile(gatePath)
	if err != nil || !bytes.Equal(before, after) || len(f.auth.Prompts()) != prompts {
		t.Fatal("recovery changed or invoked host gate")
	}
	id := f.s.devices.state.Devices[0].ID
	// Restoring host authentication permits one fresh proof to upgrade, never grace.
	f.auth.SetUnavailable(nil)
	deviceResponse(t, f.hostProof(t, "/api/devices/"+id+"/upgrade", map[string]any{}), 200)
	deviceResponse(t, f.request(t, "GET", "/api/devices/recovery", deviceTestHost, nil, device, false), 200)
	deviceResponse(t, f.request(t, "GET", "/api/read", deviceTestHost, nil, device, false), 204)
}

func TestOwnerBootstrapStrictLocalAdministration(t *testing.T) {
	f := newDeviceFixture(t)
	for _, endpoint := range []struct{ method, path string }{{"POST", "/api/devices/bootstrap"}, {"POST", "/api/devices/approve"}, {"PUT", "/api/gate"}, {"PUT", "/api/gate/grace"}, {"POST", "/api/devices/id/upgrade"}} {
		for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
			r := httptest.NewRequest(endpoint.method, "http://localhost"+endpoint.path, strings.NewReader(`{}`))
			r.RemoteAddr = "127.0.0.1:1"
			r.Header.Set("Authorization", "Bearer test-owner")
			r.Header.Set(UISessionHeader, strings.Repeat("a", 64))
			r.Header.Set(header, "proxy")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			deviceResponse(t, w, 403)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/api/devices/bootstrap", strings.NewReader(`{"host":"`+deviceTestHost+`"}`))
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer test-owner")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	deviceResponse(t, w, 403)                                                                                                                 // file token without any UI session
	deviceResponse(t, f.request(t, "POST", "/api/devices/bootstrap", "localhost", map[string]string{"host": deviceTestHost}, nil, true), 428) // invented nonce still needs host proof
}

func TestOwnerBootstrapWriteFailureAndCorruptState(t *testing.T) {
	f := newDeviceFixture(t)
	c := f.exchange(t, f.bootstrap(t))
	// A regular temporary file cannot serve as a private output directory.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	f.s.devices.path = filepath.Join(blocker, "devices.json")
	deviceResponse(t, f.hostProof(t, "/api/devices/bootstrap", map[string]string{"host": deviceTestHost}), 503)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/approve", deviceTestHost, map[string]string{"code": "ANY"}, c, false), 403)
	f.s.devices.err = errors.New("corrupt device state")
	deviceResponse(t, f.request(t, "POST", "/api/devices/bootstrap", "localhost", map[string]string{"host": deviceTestHost}, nil, true), 503)
	deviceResponse(t, f.request(t, "POST", "/api/pair/bootstrap/exchange", deviceTestHost, map[string]string{"token": "ANY"}, nil, false), 503)
}
