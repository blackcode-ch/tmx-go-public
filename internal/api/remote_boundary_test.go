package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
)

// Keep the handler's own refusal even if peer route registration changes later.
func TestRemoteHandlerRefusesPeer(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/remote", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, fleet.PeerCaller("other")))
	w := httptest.NewRecorder()
	(&Server{}).remote(w, r)
	if w.Code != 403 {
		t.Fatalf("peer relay: %d", w.Code)
	}
}
