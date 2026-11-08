package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Exercise the exact pairing approval gate through a paired phone cookie. No
// host authenticator prompt is allowed, including when the host gate is off.
func TestP2PPhoneApprovalUsesPINWithoutDesktop(t *testing.T) {
	f := newDeviceFixture(t)
	cookie := f.paired(t)
	f.setPIN(t, testPIN)
	st, err := store.Open(f.paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fl, err := fleet.New(fleet.Options{Store: st, Paths: f.paths})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fl.Close(context.Background()) })
	f.s.f = fl
	ap := model.Approval{ID: "pairing", Kind: model.ApprovalGuard, Tool: fleet.ToolP2PInvite, Description: "Create an invite for Bob, valid 24 h, 1 use", State: model.ApprovalPending, CreatedAt: model.Now()}
	if err := st.PutApproval(ap); err != nil {
		t.Fatal(err)
	}
	previous := f.handler
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/test-pairing", f.s.ownerOnly(func(w http.ResponseWriter, r *http.Request) {
		var d fleet.DecideRequest
		if !decode(w, r, &d) {
			return
		}
		if f.s.gateDecide(w, r, ap.ID, d) {
			w.WriteHeader(204)
		}
	}))
	mux.Handle("/", previous)
	f.handler = f.s.guard(mux)
	if err := f.s.gate.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	prompts := len(f.auth.Prompts())
	for _, decision := range []string{"allow", "allow_always"} {
		body := map[string]string{"decision": decision, "scope": "any"}
		v := deviceResponse(t, f.request(t, "POST", "/api/test-pairing", deviceTestHost, body, cookie, false), 428)
		id, method := f.confirmation(t, v)
		if method != "pin" {
			t.Fatal("not phone PIN", method)
		}
		deviceResponse(t, f.request(t, "POST", "/api/gate/browser/"+id, deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
		deviceResponse(t, f.request(t, "POST", "/api/test-pairing?confirmation="+id, deviceTestHost, body, cookie, false), 204)
		// The proof is single use and does not silently allow a later approval.
		deviceResponse(t, f.request(t, "POST", "/api/test-pairing?confirmation="+id, deviceTestHost, body, cookie, false), 428)
	}
	deviceResponse(t, f.request(t, "POST", "/api/test-pairing", deviceTestHost, map[string]string{"decision": "deny"}, cookie, false), 204)
	if len(f.auth.Prompts()) != prompts {
		t.Fatal("pairing required a desktop prompt")
	}
}
