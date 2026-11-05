package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
)

// tmx update against a daemon with an updater (a dev build: it never checks or installs). Gate
// off: as before, no prompt. Gate on: the changes and an install by hand wait for the owner's
// confirmation and retry with it; the token never shows; a refusal changes nothing.
func TestUpdateCommandsThroughTheGate(t *testing.T) {
	d := apitest.New(t)
	url, _ := d.WithUpdater(t)
	useHome(t, d.Home, urlPort(t, url))
	auto := func() string {
		if a := d.UpdateSettings().Auto; a != nil && !*a {
			return "off"
		}
		return "on"
	}
	tokenFile := config.Paths{Home: d.Home}.GitHubTokenFile()
	const secret = "github_pat_cli-secret-0123456789"

	out, errs, code := tmx(t, "update", "auto", "off")
	if code != 0 || !strings.Contains(out, "auto off") || errs != "" || auto() != "off" {
		t.Fatalf("auto off, gate off: %d %q %q", code, out, errs)
	}
	if got := d.GateAuth.Prompts(); len(got) != 0 {
		t.Fatalf("the gate is off but asked: %q", got)
	}

	if err := d.Gate.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	// v1: automatic installs stay off; refused before anyone is asked.
	n0 := len(d.GateAuth.Prompts())
	out, errs, code = tmx(t, "update", "auto", "on")
	if code != 1 || !strings.Contains(errs, "automatic updates are off in this version") || auto() != "off" || len(d.GateAuth.Prompts()) != n0 {
		t.Fatalf("auto on: %d %q %q", code, out, errs)
	}
	out, errs, code = tmx(t, "update", "channel", "latest")
	if code != 0 || !strings.Contains(out, "channel latest") || !strings.Contains(errs, "tmx is trying to take updates from fleet releases including prereleases.") {
		t.Fatalf("channel: %d %q %q", code, out, errs)
	}
	out, errs, code = tmx(t, "update", "token", secret)
	if _, err := os.Stat(tokenFile); code != 0 || err != nil || !strings.Contains(errs, "tmx is trying to save a GitHub token for updates.") ||
		strings.Contains(out+errs, secret) {
		t.Fatalf("token: %d %q %q", code, out, errs)
	}
	out, errs, code = tmx(t, "update", "now")
	if code != 0 || !strings.Contains(errs, "tmx is trying to install the newest release now and restart") || !strings.Contains(out, "updates off") {
		t.Fatalf("now: %d %q %q", code, out, errs)
	}
	// Status and check only look: no prompt.
	n := len(d.GateAuth.Prompts())
	for _, sub := range []string{"status", "check"} {
		if out, errs, code := tmx(t, "update", sub); code != 0 || errs != "" {
			t.Fatalf("%s: %d %q %q", sub, code, out, errs)
		}
	}
	if len(d.GateAuth.Prompts()) != n {
		t.Fatal("status or check asked for a confirmation")
	}
	for _, p := range d.GateAuth.Prompts() {
		if strings.Contains(p, secret) {
			t.Fatalf("a prompt shows the token: %q", p)
		}
	}

	// Refused on this machine: a clear error, nothing changes.
	d.GateAuth.SetAnswer(func(context.Context, string) error { return errors.New("Authentication failed") })
	if _, errs, code := tmx(t, "update", "auto", "off"); code != 1 || !strings.Contains(errs, "not confirmed (Authentication failed)") || auto() != "off" {
		t.Fatalf("refused: %d %q", code, errs)
	}
	if _, errs, code := tmx(t, "update", "token", "-"); code != 1 || !strings.Contains(errs, "tmx is trying to remove the GitHub token for updates.") {
		t.Fatalf("token removal refused: %d %q", code, errs)
	}
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatal("a refused removal removed the token")
	}
}

// tmx update keys: shows the pinned keys by fingerprint; pin replaces them through the owner's
// confirmation (always asked); bad arguments are a usage error. tmx update install needs a tag.
func TestUpdateKeysCommand(t *testing.T) {
	d := apitest.New(t)
	url, _ := d.WithUpdater(t)
	useHome(t, d.Home, urlPort(t, url))
	pub := make([]byte, 32)
	pub[0] = 7
	key := base64.StdEncoding.EncodeToString(pub)
	h := sha256.Sum256(pub)
	fp := hex.EncodeToString(h[:])[:12]

	if out, errs, code := tmx(t, "update", "keys"); code != 0 || !strings.Contains(out, "trusted release keys") {
		t.Fatalf("show: %d %q %q", code, out, errs)
	}
	out, errs, code := tmx(t, "update", "keys", "pin", "k2:"+key)
	if code != 0 || !strings.Contains(out, "k2  ed25519  "+fp) || !strings.Contains(errs, "replace the keys updates are trusted with: pin k2 "+fp) {
		t.Fatalf("pin: %d %q %q", code, out, errs)
	}
	if ks := d.UpdateSettings().FleetKeys; len(ks) != 1 || ks[0].ID != "k2" || ks[0].Key != key {
		t.Fatalf("not pinned: %+v", ks)
	}
	for _, args := range [][]string{{"update", "keys", "pin"}, {"update", "keys", "pin", "nocolon"}, {"update", "keys", "nope"}, {"update", "install"}, {"update", "auto", "maybe"}} {
		if _, _, code := tmx(t, args...); code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
}
