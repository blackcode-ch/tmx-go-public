package transfers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type relayPeer struct {
	testPeer
	relayed Record
	pulled  bool
}

func (p *relayPeer) Known(h string) bool { return h == "relay" }
func (p *relayPeer) Names() []string     { return []string{"relay"} }
func (p *relayPeer) Call(ctx context.Context, host, method, path string, body, out any) error {
	if host != "relay" {
		return fmt.Errorf("not a direct peer")
	}
	if path == "/api/fleet" {
		return json.Unmarshal([]byte(`{"hosts":[{"name":"source","online":true}]}`), out)
	}
	if path == "/api/remote" {
		p.relayed = body.(Record)
		if strings.HasSuffix(p.relayed.str("path"), "/done") {
			return nil
		}
		return json.Unmarshal([]byte(`{"id":"0123456789abcdef","state":"queued"}`), out)
	}
	return fmt.Errorf("unexpected path: %s", path)
}
func (p *relayPeer) RelayExportStream(ctx context.Context, via, source, ticket string, offset int64) (*http.Response, error) {
	if via != "relay" || source != "source" {
		return nil, fmt.Errorf("bad relay route")
	}
	p.pulled = true
	return p.testPeer.ExportStream(ctx, source, ticket, offset)
}
func TestPythonRelayWire(t *testing.T) {
	data := []byte("through a fleet relay\n")
	s, _ := newTestService(t, data)
	p := &relayPeer{testPeer: testPeer{data: data}}
	s.Peers = p
	via, err := s.Route(context.Background(), "source")
	if err != nil || via != "relay" {
		t.Fatalf("route=%q err=%v", via, err)
	}
	b := offerFor(data, t.TempDir())
	r, err := s.Incoming(b, "relay")
	if err != nil {
		t.Fatal(err)
	}
	if r.str("via") != "relay" {
		t.Fatal(r)
	}
	r = waitState(t, s, r.str("id"))
	if r.str("state") != "placed" {
		t.Fatal(r)
	}
	s.wg.Wait() // wait for the completion callback before inspecting captured calls
	if !p.pulled || p.relayed.str("host") != "source" || p.relayed.str("method") != "POST" || p.relayed.str("path") != "/api/exports/"+b.str("ticket")+"/done" {
		t.Fatalf("relay completion %v", p.relayed)
	}
	b["backup"] = true
	_, err = s.Incoming(b, "relay")
	requireStatus(t, err, 403)
	for _, path := range []string{"/api/config", "/api/gate/pin/record", "/api/remote", "/api/transfers/../../config", "/api/transfers?secret=1"} {
		_, err = s.RelayCall(context.Background(), "relay", "POST", path, nil)
		requireStatus(t, err, 403)
	}
}
