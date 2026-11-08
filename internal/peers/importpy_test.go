package peers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func pythonState(t *testing.T, peersJSON, configJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if peersJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "secrets", "peers.json"), []byte(peersJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func prodConfig() config.Config {
	return config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{
		{Name: "hosta", URL: "https://hosta.example.ts.net"}, {Name: "hostc", URL: "https://hostc.example.ts.net"}}}
}

func statuses(rows []Imported) map[string]string {
	m := map[string]string{}
	for _, r := range rows {
		m[r.Name] = r.Status
	}
	return m
}

func TestImportPythonTokens(t *testing.T) {
	hosta, hostc, other := NewPairToken(), NewPairToken(), NewPairToken()
	// Python's real shape: extra fields, a float old_until, an unrelated peer that is not ours.
	py := pythonState(t, `{"hosta":{"token":"`+hosta+`","role":"full","old_token":"x","old_until":1793619881.5,"stored":1,"sent":2},
		"hostc":{"token":"`+hostc+`","role":"full"},"clara-mac":{"token":"`+other+`","role":"full"}}`,
		`{"host":"hostb","port":8777,"fleet_token":"fleet-`+strings.Repeat("f", 40)+`"}`)
	p := config.Paths{Home: t.TempDir()}
	rows, err := ImportPython(prodConfig(), p, py)
	if err != nil {
		t.Fatal(err)
	}
	if s := statuses(rows); s["hosta"] != ImportImported || s["hostc"] != ImportImported || len(s) != 2 {
		t.Fatalf("%+v", rows)
	}
	ts := NewTokens(p.PeersFile())
	if ts.For("hosta") != hosta || ts.For("hostc") != hostc || ts.For("clara-mac") != "" {
		t.Fatal("tokens not imported as they were, or a peer we don't have was imported")
	}
	if who, ok := ts.Identify(hosta); !ok || who != "hosta" {
		t.Fatal("imported token does not authenticate hosta")
	}
	// The fleet token is retired fleet-wide (pair tokens only): never imported, even when present.
	if _, err := os.Stat(filepath.Join(p.SecretsDir(), "fleet-token")); !os.IsNotExist(err) {
		t.Fatalf("the fleet token was imported: %v", err)
	}
	for _, r := range rows {
		if r.Fingerprint == "" || strings.Contains(r.Fingerprint, hosta[:8]) || len(r.Fingerprint) != 12 {
			t.Fatalf("not a fingerprint: %+v", r)
		}
	}
	// Again: nothing changes.
	rows, err = ImportPython(prodConfig(), p, py)
	if err != nil {
		t.Fatal(err)
	}
	if s := statuses(rows); s["hosta"] != ImportUnchanged || s["hostc"] != ImportUnchanged {
		t.Fatalf("%+v", rows)
	}
}

func TestImportPythonNeverOverwrites(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	mine := NewPairToken()
	if _, err := NewTokens(p.PeersFile()).Store("hosta", mine, "hosta"); err != nil {
		t.Fatal(err)
	}
	py := pythonState(t, `{"hosta":{"token":"`+NewPairToken()+`","role":"full"},"hostc":{"token":"short","role":"full"}}`, `{"fleet_token":"theirs-`+strings.Repeat("t", 40)+`"}`)
	rows, err := ImportPython(prodConfig(), p, py)
	if err != nil {
		t.Fatal(err)
	}
	s := statuses(rows)
	if s["hosta"] != ImportConflict || s["hostc"] != ImportInvalid {
		t.Fatalf("%+v", rows)
	}
	if NewTokens(p.PeersFile()).For("hosta") != mine {
		t.Fatal("overwrote our token")
	}
}

func TestImportPythonRefusals(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	py := pythonState(t, "", `{"fleet_token":null}`)
	trial := prodConfig()
	trial.Profile = config.ProfileTrial
	if _, err := ImportPython(trial, p, py); err == nil {
		t.Fatal("the trial imported secrets")
	}
	rows, err := ImportPython(prodConfig(), p, py) // no peers.json: nothing to import
	if err != nil {
		t.Fatal(err)
	}
	if s := statuses(rows); s["hosta"] != ImportMissing || s["hostc"] != ImportMissing {
		t.Fatalf("%+v", rows)
	}
	bad := pythonState(t, `{"hosta": {"role": "full", "token": "`+strings.Repeat("s", 50)+`"`, `{}`)
	if _, err := ImportPython(prodConfig(), p, bad); err == nil || strings.Contains(err.Error(), "sss") {
		t.Fatalf("a damaged peers.json: %v", err)
	}
}

// Python's role "cloud" (a cloud box, limited to a few routes there) is never imported as a full
// peer here, and neither is an entry with no role at all (Python reads that as "full"; here a
// full peer takes an explicit "role": "full").
func TestImportPythonSkipsCloudPeers(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	hostc := NewPairToken()
	py := pythonState(t, `{"hosta":{"token":"`+NewPairToken()+`","role":"cloud"},"hostc":{"token":"`+hostc+`"}}`, `{}`)
	rows, err := ImportPython(prodConfig(), p, py)
	if err != nil {
		t.Fatal(err)
	}
	if s := statuses(rows); s["hosta"] != ImportSkipped || s["hostc"] != ImportSkipped {
		t.Fatalf("%+v", rows)
	}
	if NewTokens(p.PeersFile()).For("hosta") != "" {
		t.Fatal("a cloud token was imported")
	}
	if NewTokens(p.PeersFile()).For("hostc") == hostc {
		t.Fatal("a peer with no role was imported as a full peer")
	}
}

// The import never follows a link: the state folder, its secrets folder and peers.json must be
// what they say (security-review #108).
func TestImportPythonRefusesLinks(t *testing.T) {
	real := pythonState(t, `{"hosta":{"token":"`+NewPairToken()+`","role":"full"}}`, `{}`)
	p := config.Paths{Home: t.TempDir()}
	if _, err := ImportPython(prodConfig(), p, "relative/dir"); err == nil {
		t.Fatal("a relative state folder was accepted")
	}

	// peers.json that is a folder, not a file.
	notFile := pythonState(t, "", `{}`)
	if err := os.Mkdir(filepath.Join(notFile, "secrets", "peers.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportPython(prodConfig(), p, notFile); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a non-regular peers.json: %v", err)
	}

	links := map[string]func(dir string) error{
		"state folder": func(dir string) error { return os.Symlink(real, filepath.Join(dir, "home")) },
		"secrets folder": func(dir string) error {
			if err := os.Mkdir(filepath.Join(dir, "home"), 0o700); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(real, "secrets"), filepath.Join(dir, "home", "secrets"))
		},
		"peers.json": func(dir string) error {
			if err := os.MkdirAll(filepath.Join(dir, "home", "secrets"), 0o700); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(real, "secrets", "peers.json"), filepath.Join(dir, "home", "secrets", "peers.json"))
		},
	}
	for name, mk := range links {
		dir := t.TempDir()
		if err := mk(dir); err != nil {
			t.Logf("%s: cannot create a symlink here (%v); the plain-file check above still ran", name, err)
			continue
		}
		if _, err := ImportPython(prodConfig(), p, filepath.Join(dir, "home")); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("a linked %s was followed: %v", name, err)
		}
	}
	if NewTokens(p.PeersFile()).For("hosta") != "" {
		t.Fatal("a token was imported through a link")
	}
}
