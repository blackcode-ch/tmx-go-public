package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/gate"
)

// #187, lockout = SEV-1: on an existing install (gate on, Windows Hello, a PIN, a paired phone)
// The owner's PIN and his paired phone work before AND after the upgrade; the PIN now also works
// at the machine; Windows Hello stays as a shortcut until he switches it off.
func TestPINAndPhoneSurviveTheUpgrade(t *testing.T) {
	f := newDeviceFixture(t)
	phone := f.paired(t) // paired the old way: Windows Hello at the machine
	f.setPIN(t, testPIN)
	payload := map[string]string{"value": "x"}

	phoneAction := func(stage string) {
		t.Helper()
		v := deviceResponse(t, f.request(t, "POST", "/api/protected", deviceTestHost, payload, phone, false), 428)
		id, method := f.confirmation(t, v)
		if method != "pin" {
			t.Fatalf("%s: phone method %q", stage, method)
		}
		deviceResponse(t, f.request(t, "POST", "/api/gate/approvals/"+id+"/pin", deviceTestHost, map[string]string{"pin": testPIN}, phone, false), 200)
		deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+id, deviceTestHost, payload, phone, false), 204)
	}
	phoneAction("before the upgrade")

	// The upgrade: the daemon restarts on the gate.json an older version wrote.
	gateFile := filepath.Join(f.paths.SecretsDir(), "gate.json")
	if err := os.WriteFile(gateFile, []byte(`{"enabled":true,"method":"windows-hello","changed_at":1,"grace_minutes":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.s.gate.Close()
	g := gate.New(f.paths, f.auth, nil, gate.FileMarker(filepath.Join(f.paths.Home, "marker")))
	t.Cleanup(g.Close)
	g.SetPINCheck(f.s.pin.IsSet)
	f.s.gate = g
	if !g.Enabled() || !g.HelloShortcut() {
		t.Fatalf("the upgrade changed the gate: enabled=%v hello=%v", g.Enabled(), g.HelloShortcut())
	}

	hostPrompts := len(f.auth.Prompts())
	phoneAction("after the upgrade")
	if len(f.auth.Prompts()) != hostPrompts {
		t.Fatal("a phone action prompted the host")
	}

	// At the machine: the PIN answers (Windows Hello is open too, but not answered here).
	f.auth.Answer = func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
	localAction := func(wantHello bool) {
		t.Helper()
		v := deviceResponse(t, f.request(t, "POST", "/api/protected", "localhost", payload, nil, true), 428)
		var c gate.Confirmation
		if err := json.Unmarshal(v["confirmation"], &c); err != nil {
			t.Fatal(err)
		}
		if !c.PIN || c.Hello != wantHello {
			t.Fatalf("local confirmation ways: %+v (want hello=%v)", c, wantHello)
		}
		// A paired device can't use the machine's PIN route; a wrong PIN is refused.
		deviceResponse(t, f.request(t, "POST", "/api/gate/confirmations/"+c.ID+"/pin", deviceTestHost, map[string]string{"pin": testPIN}, phone, false), 403)
		deviceResponse(t, f.request(t, "POST", "/api/gate/confirmations/"+c.ID+"/pin", "localhost", map[string]string{"pin": "000000"}, nil, true), 403)
		deviceResponse(t, f.request(t, "POST", "/api/gate/confirmations/"+c.ID+"/pin", "localhost", map[string]string{"pin": testPIN}, nil, true), 200)
		deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+c.ID, "localhost", payload, nil, true), 204)
		deviceResponse(t, f.request(t, "POST", "/api/gate/confirmations/"+c.ID+"/pin", "localhost", map[string]string{"pin": testPIN}, nil, true), 403) // single use
	}
	localAction(true)

	// Windows Hello alone still works after the upgrade.
	f.auth.Answer = nil
	v := deviceResponse(t, f.request(t, "POST", "/api/protected", "localhost", payload, nil, true), 428)
	id, _ := f.confirmation(t, v)
	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	c, _ := g.Wait(ctx, id)
	cancel()
	if c.State != gate.StateConfirmed {
		t.Fatalf("Windows Hello after the upgrade: %+v", c)
	}
	deviceResponse(t, f.request(t, "POST", "/api/protected?confirmation="+id, "localhost", payload, nil, true), 204)

	// Switching Windows Hello off: a fresh proof (answered with the PIN), at the machine only.
	f.auth.Answer = func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
	off := map[string]bool{"enabled": false}
	deviceResponse(t, f.request(t, "PUT", "/api/gate/hello", deviceTestHost, off, phone, false), 403)
	v = deviceResponse(t, f.request(t, "PUT", "/api/gate/hello", "localhost", off, nil, true), 428)
	id, _ = f.confirmation(t, v)
	deviceResponse(t, f.request(t, "POST", "/api/gate/confirmations/"+id+"/pin", "localhost", map[string]string{"pin": testPIN}, nil, true), 200)
	deviceResponse(t, f.request(t, "PUT", "/api/gate/hello?confirmation="+id, "localhost", off, nil, true), 200)
	if g.HelloShortcut() {
		t.Fatal("Windows Hello still on")
	}
	prompts := len(f.auth.Prompts())
	localAction(false)
	phoneAction("with Windows Hello off")
	if len(f.auth.Prompts()) != prompts {
		t.Fatal("the OS prompt opened with Windows Hello off")
	}
}

// Without a PIN, Windows Hello can't be switched off: it would be the last way in.
func TestHelloCannotGoOffWithoutPIN(t *testing.T) {
	f := newDeviceFixture(t)
	f.s.gate.SetPINCheck(f.s.pin.IsSet)
	deviceResponse(t, f.request(t, "PUT", "/api/gate/hello", "localhost", map[string]bool{"enabled": false}, nil, true), 409)
	// A new install starts with the shortcut off, yet with no PIN the OS prompt still opens.
	if f.s.gate.HelloShortcut() || f.s.pin.IsSet() {
		t.Fatalf("fixture: hello=%v pin=%v", f.s.gate.HelloShortcut(), f.s.pin.IsSet())
	}
	v := deviceResponse(t, f.request(t, "POST", "/api/protected", "localhost", map[string]string{"value": "x"}, nil, true), 428)
	var c gate.Confirmation
	if err := json.Unmarshal(v["confirmation"], &c); err != nil {
		t.Fatal(err)
	}
	if c.PIN || !c.Hello {
		t.Fatalf("no PIN must leave the OS prompt: %+v", c)
	}
}
