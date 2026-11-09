package transfers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type pythonWire struct {
	Payload string `json:"payload"`
	Start   struct {
		Request, Response Record
		Status            int
	} `json:"start"`
	Incoming struct {
		Body, Response Record
		Method, Path   string
		Status         int
	} `json:"incoming"`
	Offered Record `json:"offered"`
	List    Record `json:"list"`
}

func loadPythonWire(t *testing.T) pythonWire {
	t.Helper()
	b, e := os.ReadFile("testdata/python_wire.json")
	if e != nil {
		t.Fatal(e)
	}
	var v pythonWire
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func assertWire(t *testing.T, got, want Record) {
	t.Helper()
	got = clone(got)
	want = clone(want)
	if !reflect.DeepEqual(got, want) {
		a, _ := json.MarshalIndent(got, "", "  ")
		b, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("wire differs\ngot %s\nwant %s", a, b)
	}
}
func normalizeRecord(r, want Record) Record {
	r = clone(r)
	for _, k := range []string{"id", "created", "updated", "src", "dest", "ticket", "remote_id", "out_id"} {
		if v, ok := want[k]; ok {
			r[k] = v
		}
	}
	return r
}

func TestPythonToGoGoldenAdmission(t *testing.T) {
	fixture := loadPythonWire(t)
	s, _ := newTestService(t, []byte(fixture.Payload))
	b := clone(fixture.Incoming.Body)
	b["dest"] = t.TempDir()
	r, e := s.Incoming(b, "source")
	if e != nil {
		t.Fatal(e)
	}
	if fixture.Incoming.Status != 202 || fixture.Incoming.Path != "/api/transfers/incoming" || fixture.Incoming.Method != "POST" {
		t.Fatal("bad reference fixture")
	}
	assertWire(t, normalizeRecord(r, fixture.Incoming.Response), fixture.Incoming.Response)
	final := waitState(t, s, r.str("id"))
	if final.str("state") != "placed" {
		t.Fatal(final)
	}
	got, e := os.ReadFile(filepath.Join(b.str("dest"), b.str("name")))
	if e != nil || string(got) != fixture.Payload {
		t.Fatalf("payload %q: %v", got, e)
	}
}
func TestGoToPythonGoldenOffer(t *testing.T) {
	fixture := loadPythonWire(t)
	s, p := newTestService(t, nil)
	p.name, p.known = "source", "target"
	src := filepath.Join(t.TempDir(), "hello.txt")
	if e := os.WriteFile(src, []byte(fixture.Payload), 0600); e != nil {
		t.Fatal(e)
	}
	b := clone(fixture.Start.Request)
	b["src"], b["dest"] = src, t.TempDir()
	r, e := s.Start(context.Background(), b, "source")
	if e != nil {
		t.Fatal(e)
	}
	assertWire(t, normalizeRecord(r, fixture.Start.Response), fixture.Start.Response)
	final := waitState(t, s, r.str("id"))
	if final.str("state") != "offered" {
		t.Fatal(final)
	}
	p.mu.Lock()
	offer := clone(p.offers[0])
	p.mu.Unlock()
	assertWire(t, normalizeRecord(offer, fixture.Incoming.Body), fixture.Incoming.Body)
	assertWire(t, normalizeRecord(final, fixture.Offered), fixture.Offered)
}
