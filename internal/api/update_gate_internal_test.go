package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// With the gate on, the gated update handlers answer 428 before they touch the updater: here
// it is nil, so reaching it (an install started without a proof) would panic.
func TestGatedUpdateHandlersStopBeforeTheUpdater(t *testing.T) {
	paths := config.Paths{Home: t.TempDir()}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	g := gate.New(paths, &gate.Fake{}, nil, gate.FileMarker(paths.Home+"/test-marker"))
	defer g.Close()
	if err := g.SetGrace(0); err != nil {
		t.Fatal(err)
	}
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	s := &Server{gate: g}
	for _, r := range []struct {
		method, path, body string
		h                  http.HandlerFunc
	}{
		{"PUT", "/api/update", `{"auto":false}`, s.putUpdate},
		{"POST", "/api/update/install", `{"now":true}`, s.installUpdate},
		{"POST", "/api/update/install", ``, s.installUpdate},
		{"PUT", "/api/update/token", `{"token":"github_pat_x"}`, s.putUpdateToken},
	} {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		req.Host = "localhost" // this test exercises the host OS gate
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, fleet.Caller{Owner: true}))
		w := httptest.NewRecorder()
		r.h(w, req)
		var need struct {
			Confirmation gate.Confirmation `json:"confirmation"`
		}
		if w.Code != http.StatusPreconditionRequired || json.Unmarshal(w.Body.Bytes(), &need) != nil || need.Confirmation.ID == "" {
			t.Fatalf("%s %s %q: %d %s", r.method, r.path, r.body, w.Code, w.Body)
		}
		// One prompt at a time: let the fake answer this one before the next request.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, _ := g.Wait(ctx, need.Confirmation.ID)
		cancel()
		if c.State != gate.StateConfirmed {
			t.Fatalf("%s: %+v", r.path, c)
		}
	}
}

// PUT /api/update/keys never reaches the updater's save without a fresh owner proof: a daemon
// without a gate refuses it (501), Companion and agents are refused, and with the gate off it
// still asks.
func TestUpdateKeysRefusedWithoutAProof(t *testing.T) {
	paths := config.Paths{Home: t.TempDir()}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	saved := false
	u := update.New(update.Options{Paths: paths, Version: "fleet-v0.1.0",
		Settings: func() config.UpdateConfig { return config.UpdateConfig{} },
		Save:     func(config.UpdateConfig) error { saved = true; return nil }})
	body := `{"fleet_keys":[{"id":"k2","type":"ed25519","key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `"}]}`
	put := func(s *Server, c fleet.Caller) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/update/keys", strings.NewReader(body))
		req.Host = "localhost"
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, c))
		w := httptest.NewRecorder()
		s.putUpdateKeys(w, req)
		return w
	}
	if w := put(&Server{updater: u}, fleet.Caller{Owner: true}); w.Code != http.StatusNotImplemented {
		t.Fatalf("no gate: %d %s", w.Code, w.Body)
	}
	g := gate.New(paths, &gate.Fake{}, nil, gate.FileMarker(paths.Home+"/test-marker"))
	defer g.Close()
	s := &Server{gate: g, updater: u}
	for _, c := range []fleet.Caller{{Owner: true, Companion: true}, {AgentID: "a1"}, {Owner: true, AgentID: "a1"}} {
		if w := put(s, c); w.Code != http.StatusForbidden {
			t.Fatalf("%+v: %d %s", c, w.Code, w.Body)
		}
	}
	if w := put(s, fleet.Caller{Owner: true}); w.Code != http.StatusPreconditionRequired { // the gate is off
		t.Fatalf("gate off: %d %s", w.Code, w.Body)
	}
	if saved {
		t.Fatal("the keys were saved without a proof")
	}
}

// PUT /api/update/drill never arms without a fresh owner proof: no gate = 501, Companion and
// agents refused, gate off still asks.
func TestUpdateDrillRefusedWithoutAProof(t *testing.T) {
	paths := config.Paths{Home: t.TempDir()}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	u := update.New(update.Options{Paths: paths, Version: "fleet-v0.1.0",
		Settings: func() config.UpdateConfig { return config.UpdateConfig{} }})
	put := func(s *Server, c fleet.Caller) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/update/drill", strings.NewReader(`{"arm":true}`))
		req.Host = "localhost"
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, c))
		w := httptest.NewRecorder()
		s.putUpdateDrill(w, req)
		return w
	}
	if w := put(&Server{updater: u}, fleet.Caller{Owner: true}); w.Code != http.StatusNotImplemented {
		t.Fatalf("no gate: %d %s", w.Code, w.Body)
	}
	g := gate.New(paths, &gate.Fake{}, nil, gate.FileMarker(paths.Home+"/test-marker"))
	defer g.Close()
	s := &Server{gate: g, updater: u}
	for _, c := range []fleet.Caller{{Owner: true, Companion: true}, {AgentID: "a1"}, {Owner: true, AgentID: "a1"}} {
		if w := put(s, c); w.Code != http.StatusForbidden {
			t.Fatalf("%+v: %d %s", c, w.Code, w.Body)
		}
	}
	if w := put(s, fleet.Caller{Owner: true}); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("gate off: %d %s", w.Code, w.Body)
	}
	if u.Status().DrillArmed {
		t.Fatal("armed without a proof")
	}
}
