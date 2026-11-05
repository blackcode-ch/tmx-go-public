package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func edKey(id, pub string) config.FleetKey {
	return config.FleetKey{ID: id, Type: config.KeyEd25519, Key: pub}
}

func statement(t *testing.T, repo string, serial uint64, keys ...config.FleetKey) []byte {
	t.Helper()
	b, err := MarshalKeysStatement(KeysStatement{Repo: repo, Serial: serial, FleetKeys: keys, Require: []string{config.KeyEd25519}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every check of a rotation statement refuses on its own.
func TestKeysStatementRefusals(t *testing.T) {
	k1, p1 := newKey(t)
	k2, p2 := newKey(t)
	_, p3 := newKey(t)
	_, p4 := newKey(t)
	_, p5 := newKey(t)
	pins := []config.FleetKey{edKey("k1", p1)}
	const repo = "o/r"
	good := statement(t, repo, 1, edKey("k1", p1), edKey("k2", p2))
	sign := func(id string, k ed25519.PrivateKey, serial uint64, b []byte) []byte {
		return []byte(SignKeysLine(id, k, repo, serial, b) + "\n")
	}
	if st, err := VerifyKeys(pins, nil, repo, good, sign("k1", k1, 1, good)); err != nil || st.Serial != 1 || len(st.FleetKeys) != 2 {
		t.Fatalf("good statement: %+v %v", st, err)
	}
	five := statement(t, repo, 1, edKey("k1", p1), edKey("k2", p2), edKey("k3", p3), edKey("k4", p4), edKey("k5", p5))
	empty := statement(t, repo, 1)
	other := statement(t, "x/y", 1, edKey("k2", p2))
	zero := statement(t, repo, 0, edKey("k2", p2))
	extra := []byte(strings.Replace(string(good), `"serial"`, `"note": "x", "serial"`, 1))
	twice := append(append([]byte(nil), good...), good...)
	edited := []byte(strings.Replace(string(good), `"serial": 1`, `"serial": 2`, 1))
	asRelease := []byte("k1:" + config.KeyEd25519 + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(k1, signatureMessage(FleetSignatureContext, repo, keysTag(1), good))))
	for _, c := range []struct {
		name     string
		keys, sg []byte
	}{
		{"unsigned", good, nil},
		{"garbage signature file", good, []byte("not a signature")},
		{"signed only by a key not pinned", good, sign("k2", k2, 1, good)},
		{"pinned id, other key", good, sign("k1", k2, 1, good)},
		{"a release signature", good, asRelease},
		{"signed for another serial", good, sign("k1", k1, 2, good)},
		{"edited after signing", edited, sign("k1", k1, 1, good)},
		{"more than 4 keys", five, sign("k1", k1, 1, five)},
		{"no key left", empty, sign("k1", k1, 1, empty)},
		{"another repository", other, []byte(SignKeysLine("k1", k1, "x/y", 1, other))},
		{"serial 0", zero, sign("k1", k1, 0, zero)},
		{"unknown field", extra, sign("k1", k1, 1, extra)},
		{"two objects", twice, sign("k1", k1, 1, twice)},
		{"not JSON", []byte("fleet_keys: k2"), sign("k1", k1, 1, []byte("fleet_keys: k2"))},
		{"too big", append(good, make([]byte, maxKeysFile)...), nil},
	} {
		if _, err := VerifyKeys(pins, nil, repo, c.keys, c.sg); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// The require policy applies: a statement needs every required type.
	if _, err := VerifyKeys(pins, []string{config.KeyEd25519, config.KeyWebAuthn}, repo, good, sign("k1", k1, 1, good)); err == nil {
		t.Error("require [ed25519 webauthn]: an ed25519-only statement accepted")
	}
}

// rotHarness is a harness whose keys come from config.json "update" (fleet_keys), as in a daemon.
func rotHarness(t *testing.T, g *fakeGitHub, pins ...config.FleetKey) *harness {
	exe := filepath.Join(t.TempDir(), "bin", "tmx")
	writeExe(t, exe, "fleet-v0.1.1")
	h := newHarness(t, g, "", "fleet-v0.1.1", exe, "linux", false)
	h.u.o.Keys = nil
	h.mu.Lock()
	h.settings.FleetKeys = pins
	h.mu.Unlock()
	h.u.mu.Lock() // New saw no usable key; look again as a start with these settings would
	if h.u.offReason() == "" {
		h.u.st.State, h.u.st.Reason = StateIdle, ""
	}
	h.u.mu.Unlock()
	return h
}

// release puts tag on the stable channel, SHA256SUMS signed by (id, key), plus KEYS/KEYS.sig if given.
func (g *fakeGitHub) release(tag, id string, key ed25519.PrivateKey, keys, keysSig []byte) {
	name := "tmxg_" + tag + "_linux_amd64"
	bin := []byte("binary " + tag)
	h := sha256.Sum256(bin)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(h[:]), name)
	g.mu.Lock()
	defer g.mu.Unlock()
	r := release{TagName: tag, HTMLURL: "https://example.invalid/" + tag}
	add := func(n string, b []byte) {
		g.files[tag+"/"+n] = b
		r.Assets = append(r.Assets, asset{Name: n, Size: int64(len(b)), URL: g.srv.URL + "/assets/" + tag + "/" + n})
	}
	add(name, bin)
	add("SHA256SUMS", []byte(sums))
	add("SHA256SUMS.sig", []byte(SignReleaseLine(id, key, "o/r", tag, []byte(sums))+"\n"))
	if keys != nil {
		add("KEYS", keys)
	}
	if keysSig != nil {
		add("KEYS.sig", keysSig)
	}
	g.releases = map[string]release{"stable": r}
}

func (h *harness) stageLatest(t *testing.T) error {
	t.Helper()
	s := h.u.Check(context.Background())
	if s.State == StateError {
		return fmt.Errorf("check: %s", s.Error)
	}
	h.u.mu.Lock()
	r, v := h.u.rel, h.u.ver
	h.u.mu.Unlock()
	got, err := h.u.stage(context.Background(), h.u.target, r, v.String())
	if err == nil {
		os.RemoveAll(got.cleanup)
	}
	return err
}

func (h *harness) pinned() (ids string, serial uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return keyIDs(h.settings.FleetKeys), h.settings.KeysSerial
}

// v1 → {v1, v2} → {v2}: a release signed by v2 is accepted only after a statement signed by v1
// pins v2; once a statement signed by v2 drops v1, v1's releases are refused, and replaying the
// first statement changes nothing.
func TestRotationRoundTrip(t *testing.T) {
	g := newFakeGitHub(t)
	k1, p1 := newKey(t)
	k2, p2 := newKey(t)
	h := rotHarness(t, g, edKey("k1", p1))

	g.release("fleet-v0.2.0", "k2", k2, nil, nil)
	if err := h.stageLatest(t); err == nil {
		t.Fatal("a release signed by a key not pinned yet was accepted")
	}

	s1 := statement(t, "o/r", 1, edKey("k1", p1), edKey("k2", p2))
	// A forged statement (signed by v2 itself) refuses the release and pins nothing.
	g.release("fleet-v0.2.0", "k2", k2, s1, []byte(SignKeysLine("k2", k2, "o/r", 1, s1)))
	if err := h.stageLatest(t); err == nil {
		t.Fatal("a self-signed statement was accepted")
	}
	if ids, n := h.pinned(); ids != "k1" || n != 0 {
		t.Fatalf("after a forged statement: %s %d", ids, n)
	}
	// KEYS without KEYS.sig refuses the release.
	g.release("fleet-v0.2.0", "k1", k1, s1, nil)
	if err := h.stageLatest(t); err == nil {
		t.Fatal("KEYS without KEYS.sig accepted")
	}

	g.release("fleet-v0.2.0", "k2", k2, s1, []byte(SignKeysLine("k1", k1, "o/r", 1, s1)))
	if err := h.stageLatest(t); err != nil {
		t.Fatalf("statement by v1, release by v2: %v", err)
	}
	if ids, n := h.pinned(); ids != "k1,k2" || n != 1 {
		t.Fatalf("after statement 1: %s %d", ids, n)
	}

	s2 := statement(t, "o/r", 2, edKey("k2", p2))
	g.release("fleet-v0.3.0", "k2", k2, s2, []byte(SignKeysLine("k2", k2, "o/r", 2, s2)))
	if err := h.stageLatest(t); err != nil {
		t.Fatalf("statement 2 by v2: %v", err)
	}
	if ids, n := h.pinned(); ids != "k2" || n != 2 {
		t.Fatalf("after statement 2: %s %d", ids, n)
	}

	// An old statement (serial 1, signed by v1, which is no longer pinned) is ignored before its
	// signature is checked: it changes nothing and does not block the release. A statement by v2
	// with an already used serial changes nothing either.
	g.release("fleet-v0.4.0", "k2", k2, s1, []byte(SignKeysLine("k1", k1, "o/r", 1, s1)))
	if err := h.stageLatest(t); err != nil {
		t.Fatalf("an old statement by a dropped key blocked the release: %v", err)
	}
	if ids, n := h.pinned(); ids != "k2" || n != 2 {
		t.Fatalf("an old statement changed the pins: %s %d", ids, n)
	}
	old := statement(t, "o/r", 2, edKey("k1", p1), edKey("k2", p2))
	g.release("fleet-v0.4.0", "k2", k2, old, []byte(SignKeysLine("k2", k2, "o/r", 2, old)))
	if err := h.stageLatest(t); err != nil {
		t.Fatalf("an already used serial should be ignored, not refuse the release: %v", err)
	}
	if ids, n := h.pinned(); ids != "k2" || n != 2 {
		t.Fatalf("a replayed serial changed the pins: %s %d", ids, n)
	}
	g.release("fleet-v0.4.0", "k1", k1, nil, nil)
	if err := h.stageLatest(t); err == nil {
		t.Fatal("a release by the dropped key v1 was accepted")
	}
}

// The owner's re-pin replaces the set (dropping the legacy key), keeps the rotation serial and
// turns updates back on; an invalid set changes nothing.
func TestSetKeys(t *testing.T) {
	g := newFakeGitHub(t)
	_, p1 := newKey(t)
	_, p2 := newKey(t)
	h := rotHarness(t, g)
	h.mu.Lock()
	h.settings.FleetKeys, h.settings.FleetPublicKey, h.settings.KeysSerial = nil, p1, 7
	h.mu.Unlock()

	for _, bad := range [][]config.FleetKey{nil, {edKey("k2", "x")}, {edKey("k2", p2), edKey("k2", p1)}} {
		if _, err := h.u.SetKeys(bad, nil); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := h.u.SetKeys([]config.FleetKey{edKey("k2", p2)}, []string{config.KeyWebAuthn}); err == nil {
		t.Error("require webauthn without a webauthn key accepted")
	}
	if got := h.u.Keys(); len(got.FleetKeys) != 1 || got.FleetKeys[0].ID != config.LegacyFleetKeyID {
		t.Fatalf("a refused re-pin changed the pins: %+v", got)
	}
	got, err := h.u.SetKeys([]config.FleetKey{edKey("k2", p2)}, nil)
	if err != nil || len(got.FleetKeys) != 1 || got.FleetKeys[0].ID != "k2" || got.KeysSerial != 7 {
		t.Fatalf("re-pin: %+v %v", got, err)
	}
	h.mu.Lock()
	legacy := h.settings.FleetPublicKey
	h.mu.Unlock()
	if legacy != "" {
		t.Fatal("the legacy key stayed pinned")
	}
}

// The owner's re-pin that lands while KEYS downloads wins: the statement is checked against the
// re-pinned set, so a statement by the key the owner just dropped is refused and saves nothing
// (security-review #119).
func TestRotationDoesNotUndoARepin(t *testing.T) {
	g := newFakeGitHub(t)
	k1, p1 := newKey(t)
	_, p2 := newKey(t)
	_, p3 := newKey(t)
	h := rotHarness(t, g, edKey("k1", p1))
	evil := statement(t, "o/r", 1, edKey("k1", p1), edKey("k3", p3))
	g.release("fleet-v0.2.0", "k1", k1, evil, []byte(SignKeysLine("k1", k1, "o/r", 1, evil)))
	repinned := false
	h.u.keysFetched = func() {
		if _, err := h.u.SetKeys([]config.FleetKey{edKey("k2", p2)}, nil); err != nil {
			t.Errorf("re-pin: %v", err)
		}
		repinned = true
	}
	if err := h.stageLatest(t); err == nil {
		t.Fatal("a statement by the key the owner just dropped was applied")
	}
	if ids, n := h.pinned(); !repinned || ids != "k2" || n != 0 {
		t.Fatalf("the re-pin was undone: %s %d (re-pinned %v)", ids, n, repinned)
	}
}

// A statement may not move the serial more than maxSerialStep at once (no burned serial space).
func TestRotationSerialStep(t *testing.T) {
	g := newFakeGitHub(t)
	k1, p1 := newKey(t)
	_, p2 := newKey(t)
	h := rotHarness(t, g, edKey("k1", p1))
	for _, serial := range []uint64{maxSerialStep + 1, 1<<64 - 1} {
		s := statement(t, "o/r", serial, edKey("k1", p1), edKey("k2", p2))
		g.release("fleet-v0.2.0", "k1", k1, s, []byte(SignKeysLine("k1", k1, "o/r", serial, s)))
		if err := h.stageLatest(t); err == nil {
			t.Fatalf("serial %d accepted", serial)
		}
		if ids, n := h.pinned(); ids != "k1" || n != 0 {
			t.Fatalf("serial %d changed the pins: %s %d", serial, ids, n)
		}
	}
	s := statement(t, "o/r", maxSerialStep, edKey("k1", p1), edKey("k2", p2))
	g.release("fleet-v0.2.0", "k1", k1, s, []byte(SignKeysLine("k1", k1, "o/r", maxSerialStep, s)))
	if err := h.stageLatest(t); err != nil {
		t.Fatalf("a step of exactly %d: %v", maxSerialStep, err)
	}
}

// Configure saves the whole update config; a re-pin that lands while it saves must not be
// overwritten with the pins Configure read before (review of #119). The re-pin is started from
// inside Configure's save: with keysMu it waits for that save and lands after it.
func TestConfigureKeepsAConcurrentRepin(t *testing.T) {
	g := newFakeGitHub(t)
	_, p1 := newKey(t)
	_, p2 := newKey(t)
	h := rotHarness(t, g, edKey("k1", p1))
	save := h.u.o.Save
	repin := make(chan error, 1)
	var first atomic.Bool
	h.u.o.Save = func(c config.UpdateConfig) error {
		if first.CompareAndSwap(false, true) { // the first save is Configure's
			go func() { _, err := h.u.SetKeys([]config.FleetKey{edKey("k2", p2)}, nil); repin <- err }()
			select {
			case err := <-repin: // no lock: the re-pin finished inside Configure's window
				repin <- err
			case <-time.After(200 * time.Millisecond):
			}
		}
		return save(c)
	}
	on := true
	if _, err := h.u.Configure(&on, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-repin; err != nil {
		t.Fatal(err)
	}
	if ids, _ := h.pinned(); ids != "k2" {
		t.Fatalf("Configure wrote the old pins %s back over the re-pin", ids)
	}
}
