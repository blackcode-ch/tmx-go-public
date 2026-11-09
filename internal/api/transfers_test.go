package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/antechamber"
	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/peers"
	"github.com/blackcode-ch/tmx-go/internal/transfers"
)

// Exercise both real HTTP adapters and pair-token clients, including the target's pull.
func TestGoToGoTransfer(t *testing.T) {
	da, db := apitest.New(t), apitest.New(t)
	var ha, hb http.Handler
	sa := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ha.ServeHTTP(w, r) }))
	defer sa.Close()
	sb := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hb.ServeHTTP(w, r) }))
	defer sb.Close()
	ca := peers.New(config.Config{Profile: config.ProfileProduction, Machine: "alpha", Peers: []config.Peer{{Name: "beta", URL: sb.URL}}}, config.Paths{Home: da.Home}, nil)
	cb := peers.New(config.Config{Profile: config.ProfileProduction, Machine: "beta", Peers: []config.Peer{{Name: "alpha", URL: sa.URL}}}, config.Paths{Home: db.Home}, nil)
	ca.SetTransport(sb.Client().Transport)
	cb.SetTransport(sa.Client().Transport)
	pair := peers.NewPairToken()
	if _, e := ca.Tokens().Store("beta", pair, "test"); e != nil {
		t.Fatal(e)
	}
	if _, e := cb.Tokens().Store("alpha", pair, "test"); e != nil {
		t.Fatal(e)
	}
	makeService := func(home string, p *peers.Client) *transfers.Service {
		chamber, e := antechamber.New(filepath.Join(home, "antechamber"), p.Machine())
		if e != nil {
			t.Fatal(e)
		}
		chamber.Isolate = false
		chamber.AV = antechamber.NoAV{}
		svc, e := transfers.New(transfers.Options{Home: home, Install: t.TempDir(), Peers: p, Antechamber: chamber, DiskSpace: func(string) (uint64, uint64, error) { return 1 << 40, 2 << 40, nil }})
		if e != nil {
			t.Fatal(e)
		}
		return svc
	}
	a, b := makeService(da.Home, ca), makeService(db.Home, cb)
	defer a.Close()
	defer b.Close()
	ha = api.New(api.Options{Fleet: da.Fleet, OwnerToken: da.OwnerToken, Peers: ca, Transfers: a}).Handler()
	hb = api.New(api.Options{Fleet: db.Fleet, OwnerToken: db.OwnerToken, Peers: cb, Transfers: b}).Handler()
	src := filepath.Join(t.TempDir(), "hello.txt")
	payload := []byte("hello between Go machines\n")
	if e := os.WriteFile(src, payload, 0600); e != nil {
		t.Fatal(e)
	}
	dest := t.TempDir()
	var out map[string]any
	if e := cb.Call(context.Background(), "alpha", "POST", "/api/transfers", map[string]any{"src": src, "to": "beta", "dest": dest}, &out); e != nil {
		t.Fatal(e)
	}
	id, _ := out["id"].(string)
	if len(id) != 16 {
		t.Fatal(out)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if e := cb.Call(context.Background(), "alpha", "GET", "/api/transfers/"+id, nil, &out); e != nil {
			t.Fatal(e)
		}
		if out["state"] == "placed" {
			break
		}
		if out["state"] == "failed" || out["state"] == "refused" {
			t.Fatal(out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if out["state"] != "placed" {
		t.Fatalf("copy did not finish: %v", out)
	}
	got, e := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if e != nil || !bytes.Equal(got, payload) {
		t.Fatalf("copy=%q err=%v", got, e)
	}
	var listed map[string]any
	if e = ca.Call(context.Background(), "beta", "GET", "/api/transfers", nil, &listed); e != nil {
		t.Fatal(e)
	}
	if listed["host"] != "beta" || len(listed["transfers"].([]any)) != 1 {
		t.Fatal(listed)
	}
	// No owner bearer access, forged identity, browser origin, or missing credentials.
	for _, tc := range []struct{ name, from, token, origin string }{
		{"owner", "", da.OwnerToken, ""}, {"forged", "intruder", pair, ""}, {"browser", "beta", pair, "http://localhost"}, {"anonymous", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", sa.URL+"/api/transfers/incoming", bytes.NewBufferString(`{}`))
			if tc.from != "" {
				req.Header.Set(peers.HeaderFrom, tc.from)
				req.Header.Set(peers.HeaderToken, tc.token)
			} else if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			res, e := sa.Client().Do(req)
			if e != nil {
				t.Fatal(e)
			}
			defer res.Body.Close()
			if res.StatusCode < 400 {
				t.Fatalf("untrusted request accepted: %d", res.StatusCode)
			}
		})
	}
	// Range is on the exact Python export route, restricted to its recipient.
	export, e := a.Exports.Prepare(src, "beta", dest, "0123456789abcdef", a.Home, a.Install)
	if e != nil {
		t.Fatal(e)
	}
	res, e := cb.ExportStream(context.Background(), "alpha", export.Ticket, 6)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	tail, _ := io.ReadAll(res.Body)
	if res.StatusCode != 206 || !bytes.Equal(tail, payload[6:]) {
		t.Fatalf("range %d %q", res.StatusCode, tail)
	}
}

func TestTransferIncomingRejectsInvalidJSON(t *testing.T) {
	// The real peer mux validates body decoding before touching filesystem state.
	s := newPeerSetup(t)
	var out json.RawMessage
	code := peerDo(t, s.url, "POST", "/api/transfers/incoming", "hosta", s.pair, nil, map[string]any{}, &out)
	if code != 501 {
		t.Fatalf("unconfigured service: %d %s", code, out)
	}
}
