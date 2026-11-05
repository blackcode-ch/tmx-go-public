package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tagged adds one more published release (its own files under /assets/<tag>/), signed by key.
func (g *fakeGitHub) tagged(tag string, key ed25519.PrivateKey, prerelease bool) {
	name := "tmxg_" + tag + "_linux_amd64"
	bin := []byte("binary " + tag)
	h := sha256.Sum256(bin)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(h[:]), name)
	g.mu.Lock()
	defer g.mu.Unlock()
	r := release{TagName: tag, Prerelease: prerelease, HTMLURL: "https://example.invalid/" + tag}
	add := func(n string, b []byte) {
		g.files[tag+"/"+n] = b
		r.Assets = append(r.Assets, asset{Name: n, Size: int64(len(b)), URL: g.srv.URL + "/assets/" + tag + "/" + n})
	}
	add(name, bin)
	add("SHA256SUMS", []byte(sums))
	add("SHA256SUMS.sig", []byte(SignReleaseLine(FleetKeyID, key, "o/r", tag, []byte(sums))+"\n"))
	g.releases[tag] = r
}

// InstallTag installs exactly the tag asked for (not the newest), and refuses anything that is
// not a published, newer fleet release on this channel, before installing anything.
func TestInstallTag(t *testing.T) {
	g := newFakeGitHub(t)
	key, pub := newKey(t)
	for _, tag := range []string{"fleet-v0.1.0", "fleet-v0.2.0", "fleet-v0.3.0"} {
		g.tagged(tag, key, false)
	}
	g.tagged("fleet-v0.4.0-rc1", key, true)
	exe := filepath.Join(t.TempDir(), "bin", "tmx")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "linux", false)
	ctx := context.Background()

	for tag, want := range map[string]string{
		"v0.2.0":                  "not a fleet release tag",
		"fleet-v0.2.0-3-gabcdef0": "not a fleet release tag",
		"fleet-v0.9.0":            "no published fleet release fleet-v0.9.0",
		"fleet-v0.4.0-rc1":        "no published fleet release fleet-v0.4.0-rc1 on the stable channel",
		"fleet-v0.1.0":            "not newer than the running fleet-v0.1.1",
	} {
		if _, err := h.u.InstallTag(ctx, tag, true); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", tag, err, want)
		}
	}
	select {
	case <-h.restarts:
		t.Fatal("a refused tag installed")
	default:
	}
	h.u.mu.Lock()
	want := h.u.want
	h.u.mu.Unlock()
	if want != "" {
		t.Fatalf("a refused tag stayed pinned: %q", want)
	}

	go h.u.Run(ctx)
	if _, err := h.u.InstallTag(ctx, "fleet-v0.2.0", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.restarts:
	case <-time.After(10 * time.Second):
		t.Fatalf("no install: %+v", h.u.Status())
	}
	if b, _ := os.ReadFile(exe); string(b) != "binary fleet-v0.2.0" {
		t.Fatalf("installed %q, want fleet-v0.2.0 (not the newest)", b)
	}
	// The pin lasts one install (cleared as install returns, just after the restart); without it
	// the newest is found again.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		h.u.mu.Lock()
		w := h.u.want
		h.u.mu.Unlock()
		if w == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pin %q outlived the install", w)
		}
	}
	if r, err := h.u.latestRelease(ctx, "o/r", "stable"); err != nil || r.TagName != "fleet-v0.3.0" {
		t.Fatalf("without a pin: %s %v", r.TagName, err)
	}
}

// One pinned tag at a time: while an install of one tag is pending, another tag is refused, so an
// approval for one tag never installs another (review of #122). The same tag again is fine.
func TestInstallTagOneAtATime(t *testing.T) {
	g := newFakeGitHub(t)
	key, pub := newKey(t)
	for _, tag := range []string{"fleet-v0.2.0", "fleet-v0.3.0"} {
		g.tagged(tag, key, false)
	}
	exe := filepath.Join(t.TempDir(), "bin", "tmx")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "linux", false)
	ctx := context.Background()
	if _, err := h.u.InstallTag(ctx, "fleet-v0.2.0", false); err != nil { // queued, Run not started
		t.Fatal(err)
	}
	if _, err := h.u.InstallTag(ctx, "fleet-v0.3.0", false); err == nil || !strings.Contains(err.Error(), "fleet-v0.2.0 is already pending") {
		t.Fatalf("a second tag while one is pending: %v", err)
	}
	if _, err := h.u.InstallTag(ctx, "fleet-v0.2.0", false); err != nil {
		t.Fatalf("the same tag again: %v", err)
	}
}

// The staged binary must report exactly the version: "fleet-v0.2.0-dirty" is not fleet-v0.2.0
// (the probation's "to" must equal the running version, rollback.StartProbation).
func TestCheckWantsTheExactVersion(t *testing.T) {
	g := newFakeGitHub(t)
	_, pub := newKey(t)
	exe := filepath.Join(t.TempDir(), "bin", "tmx")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, pub, "fleet-v0.1.1", exe, "linux", false)
	for out, ok := range map[string]bool{
		"tmx fleet-v0.2.0":          true,
		"tmx fleet-v0.2.0\n":        true,
		"tmx fleet-v0.2.0-dirty":    false,
		"tmx fleet-v0.2.0-1-gabcd1": false,
		"tmx xfleet-v0.2.0":         false,
	} {
		h.u.o.Probe = func(context.Context, string) (string, error) { return out, nil }
		err := h.u.check(context.Background(), h.u.target, staged{version: "fleet-v0.2.0", path: exe})
		if (err == nil) != ok {
			t.Errorf("%q: %v", out, err)
		}
	}
}
