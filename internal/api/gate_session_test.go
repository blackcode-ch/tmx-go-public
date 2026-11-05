package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/guard"
)

func TestGateSessionCannotBeInheritedByFileTokenOrOtherUI(t *testing.T) {
	d := apitest.New(t)
	if err := d.Gate.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := d.Gate.SetGrace(60); err != nil {
		t.Fatal(err)
	}
	owner := tc.New(d.URL, d.OwnerToken)
	owner.Header = http.Header{api.UISessionHeader: {strings.Repeat("ab", 32)}}
	status, conf := call(t, owner, "PUT", "/api/safety/guard", guard.Settings{Enabled: false, OffFor: []string{}}, nil)
	if status != http.StatusPreconditionRequired {
		t.Fatalf("proof request: %d", status)
	}
	proof := confirmed(t, owner, conf)
	if owner.Header.Get(api.GraceHeader) == "" {
		t.Fatal("missing UI credential")
	}
	for _, nonce := range []string{"", strings.Repeat("cd", 32)} {
		other := tc.New(d.URL, d.OwnerToken)
		other.Header = http.Header{api.UISessionHeader: {nonce}, api.GraceHeader: {owner.Header.Get(api.GraceHeader)}}
		for _, method := range []string{"GET", "DELETE"} {
			if s, _ := call(t, other, method, "/api/gate/confirmations/"+conf.ID, nil, nil); s != http.StatusNotFound {
				t.Fatalf("other session %s proof: %d", method, s)
			}
		}
		var st gate.Status
		if s, _ := call(t, other, "GET", "/api/gate", nil, &st); s != 0 || st.GraceUntil != 0 {
			t.Fatal("other session inherited displayed grace")
		}
		other.Header.Set(api.ConfirmHeader, conf.ID)
		if s, _ := call(t, other, "PUT", "/api/safety/guard", guard.Settings{Enabled: false, OffFor: []string{}}, nil); s == 0 {
			t.Fatal("other session replayed proof or grace")
		}
		// Cancel the fake prompt from the rejected replay before the next attempt.
		d.Gate.Close()
	}
	if s, _ := call(t, proof, "PUT", "/api/safety/guard", guard.Settings{Enabled: false, OffFor: []string{}}, nil); s != 0 {
		t.Fatalf("own proof: %d", s)
	}
	if s, _ := call(t, owner, "POST", "/api/gate/lock", nil, nil); s != 0 {
		t.Fatal(s)
	}
	if s, _ := call(t, owner, "PUT", "/api/safety/guard", guard.Settings{Enabled: true, OffFor: []string{}}, nil); s != 0 {
		t.Fatal(s)
	}
	if s, _ := call(t, owner, "PUT", "/api/safety/guard", guard.Settings{Enabled: false, OffFor: []string{}}, nil); s != http.StatusPreconditionRequired {
		t.Fatalf("lock retained grace: %d", s)
	}
}
