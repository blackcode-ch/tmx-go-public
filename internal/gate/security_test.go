package gate

import (
	"errors"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateMissingStateWithEnrolmentMarkerFailsClosed(t *testing.T) {
	g, p := newGate(t, &Fake{})
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(g.Path()); err != nil {
		t.Fatal(err)
	}
	if !New(p, &Fake{}, nil, FileMarker(p.Home+"/test-marker")).Enabled() {
		t.Fatal("removing gate.json disabled an enrolled gate")
	}
}

func TestGateResetAndUnreadableStateFailClosed(t *testing.T) {
	for _, mode := range []string{"reset", "unreadable", "home removed", "marker damaged"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			p := config.Paths{Home: filepath.Join(root, "state")}
			if e := p.Ensure(); e != nil {
				t.Fatal(e)
			}
			m := FileMarker(filepath.Join(root, "marker"))
			g := New(p, &Fake{}, nil, m)
			if e := g.SetEnabled(true); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "reset":
				if e := os.WriteFile(g.Path(), []byte(`{}`), 0600); e != nil {
					t.Fatal(e)
				}
			case "unreadable":
				if e := os.Remove(g.Path()); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(g.Path(), 0700); e != nil {
					t.Fatal(e)
				}
			case "home removed":
				if e := os.RemoveAll(p.Home); e != nil {
					t.Fatal(e)
				}
			case "marker damaged":
				if e := os.WriteFile(string(m), []byte("broken"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if st := New(p, &Fake{}, nil, m).Status(); !st.Enabled {
				t.Fatal("damaged state disabled enrolled gate")
			}
		})
	}
}

func TestOfflineRecoveryAndExplicitDisable(t *testing.T) {
	g, p := newGate(t, &Fake{})
	if e := g.SetEnabled(true); e != nil {
		t.Fatal(e)
	}
	if e := g.SetEnabled(false); e != nil {
		t.Fatal(e)
	}
	if New(p, &Fake{}, nil, g.marker).Enabled() {
		t.Fatal("explicit disable did not persist")
	}
	if e := g.SetEnabled(true); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(g.Path()); e != nil {
		t.Fatal(e)
	}
	noAuth := &Fake{Unavail: errors.New("no authenticator")}
	locked := New(p, noAuth, nil, g.marker)
	if err := locked.Require(Action{Key: "recovery-check"}, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatal("deleted enrolled state did not fail closed")
	}
	if e := os.Remove(g.marker.Location()); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(g.Path(), []byte(`{"enabled":false,"grace_minutes":0}`), 0600); e != nil {
		t.Fatal(e)
	}
	recovered := New(p, noAuth, nil, g.marker)
	if recovered.Enabled() {
		t.Fatal("offline recovery failed")
	}
	if err := recovered.Require(Action{Key: "recovery-check"}, ""); err != nil {
		t.Fatal("offline recovery still needs unavailable proof")
	}
}

func TestIncompleteStateWithoutMarkerFailsClosed(t *testing.T) {
	for _, state := range []string{"{}", "null", `{"enabled":null}`, "broken"} {
		t.Run(state, func(t *testing.T) {
			p := config.Paths{Home: t.TempDir()}
			if err := p.Ensure(); err != nil {
				t.Fatal(err)
			}
			if state != "missing" {
				if err := os.WriteFile(p.SecretsDir()+"/gate.json", []byte(state), 0600); err != nil {
					t.Fatal(err)
				}
			}
			g := New(p, &Fake{}, nil, FileMarker(p.Home+"/test-marker"))
			defer g.Close()
			if !g.Enabled() || g.GraceMinutes() != 0 {
				t.Fatal("missing or incomplete state failed open")
			}
		})
	}
}

type failedMarker struct{}

func (failedMarker) Read() (bool, error)   { return false, errors.New("unreadable") }
func (failedMarker) Absent() (bool, error) { return false, errors.New("unreadable") }
func (failedMarker) Set(bool) error        { return errors.New("unwritable") }
func (failedMarker) Location() string      { return "test marker" }
func TestMarkerIOFailureFailsClosed(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	g := New(p, &Fake{}, nil, failedMarker{})
	if !g.Enabled() {
		t.Fatal("marker read failure opened gate")
	}
	if g.SetEnabled(false) == nil || !g.Enabled() {
		t.Fatal("failed persistence disabled gate")
	}
}

func TestConfirmationReuseDifferentBodyOrCaller(t *testing.T) {
	g, _ := newGate(t, &Fake{})
	if e := g.SetEnabled(true); e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	a := Action{Key: "body-one", Caller: "ui-one", Owner: true, UI: true}
	id := confirm(t, g, a)
	if _, _, ok := g.SessionProof(id, "ui-two"); ok {
		t.Fatal("other caller read proof")
	}
	_, credential, ok := g.SessionProof(id, a.Caller)
	if !ok || credential == "" {
		t.Fatal("UI proof did not mint session credential")
	}
	for _, bad := range []Action{{Key: a.Key, Caller: "ui-two", Owner: true, Session: credential}, {Key: "body-two", Caller: a.Caller, Owner: true}} {
		if e := g.Require(bad, id); e == nil {
			t.Fatal("proof replay accepted")
		}
		g.Close()
	}
	if e := g.Require(a, id); e != nil {
		t.Fatal(e)
	}
	if e := g.Require(a, id); e == nil {
		t.Fatal("proof reused")
	}
	g.Close()
	// A subsequent proof may rotate the credential, so mint a fresh one explicitly.
	id = confirm(t, g, Action{Key: "fresh", Caller: a.Caller, Owner: true, UI: true, Fresh: true})
	_, credential, _ = g.SessionProof(id, a.Caller)
	if e := g.Require(Action{Key: "next", Caller: a.Caller, Owner: true, Session: credential}, ""); e != nil {
		t.Fatal(e)
	}
	if _, leak := g.Get(id); !leak {
		t.Fatal("missing public confirmation")
	}
	g.Lock()
	if _, secret, _ := g.SessionProof(id, a.Caller); secret != "" {
		t.Fatal("lock retained credential")
	}
	if e := g.Require(Action{Key: "next", Caller: a.Caller, Owner: true, Session: credential}, ""); e == nil {
		t.Fatal("lock accepted old credential")
	}
}

func TestGraceNotHonouredForFileTokenCaller(t *testing.T) {
	g, _ := newGate(t, &Fake{})
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	confirm(t, g, Action{Key: "proof", Owner: true})
	defer g.Close()
	if err := g.Require(Action{Key: "another action", Owner: true}, ""); err == nil {
		t.Fatal("file owner token inherited another request's proof")
	}
}

func TestGraceWindowDisclosedInOwnerPrompt(t *testing.T) {
	f := &Fake{}
	g, _ := newGate(t, f)
	defer g.Close()
	if err := g.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	confirm(t, g, Action{Key: "disclose", Reason: "allow this action", Caller: "ui", Owner: true, UI: true})
	if prompts := f.Prompts(); len(prompts) != 1 || !strings.Contains(prompts[0], "1 hour no-ask window for this requesting UI session") {
		t.Fatal("OS prompt did not disclose the grace window")
	}
}
