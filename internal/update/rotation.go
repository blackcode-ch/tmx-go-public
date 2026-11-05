package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// Key rotation (DESIGN.md "Updates", rotation): a release may carry KEYS, a signed rotation
// statement, and KEYS.sig. KEYS is JSON {repo, serial, fleet_keys, require}: the whole new pin set.
// KEYS.sig has SHA256SUMS.sig's format and rules (sign.go), over the context FleetKeysContext
// (webauthn: FleetKeysWebAuthnContext), the repository and "keys-<serial>", then the exact KEYS
// bytes. A daemon checks it with the keys it pins NOW and their require policy; a valid statement
// with a serial above update.keys_serial replaces the pinned set before the release itself is
// checked, so a release may already be signed by a key the statement adds. A statement with a
// serial not above the last applied one is ignored before its signature is checked (a replay, or
// an old statement by a key dropped since); otherwise one that does not parse or verify, or moves
// the serial more than maxSerialStep, refuses the whole release. The check and the save run under
// keysMu, which the owner's re-pin holds too.

// FleetKeysContext and FleetKeysWebAuthnContext separate rotation statements from releases.
const (
	FleetKeysContext         = "tmx-fleet-keys-v1"
	FleetKeysWebAuthnContext = "tmx-fleet-keys-v2"
)

const (
	maxKeysFile   = 16 << 10
	maxSerialStep = 1000 // the most a statement's serial may move past the last applied one
)

// KeysStatement is the content of KEYS.
type KeysStatement struct {
	Repo      string            `json:"repo"`
	Serial    uint64            `json:"serial"`
	FleetKeys []config.FleetKey `json:"fleet_keys"`
	Require   []string          `json:"require"`
}

var errBadKeys = errors.New("KEYS is not a rotation statement")

func keysTag(serial uint64) string { return "keys-" + strconv.FormatUint(serial, 10) }

// ParseKeysStatement reads KEYS strictly: one JSON object, no unknown fields, serial ≥ 1.
func ParseKeysStatement(b []byte) (KeysStatement, error) {
	var st KeysStatement
	if len(b) == 0 || len(b) > maxKeysFile {
		return st, errBadKeys
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil || dec.More() || st.Serial == 0 || st.Repo == "" {
		return KeysStatement{}, errBadKeys
	}
	return st, nil
}

// MarshalKeysStatement is the canonical KEYS the signer writes.
func MarshalKeysStatement(st KeysStatement) ([]byte, error) {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// verifyKeys checks KEYS and KEYS.sig of repo against the current pins and returns the statement.
func verifyKeys(repo string, keys, sig []byte, ks keySet) (KeysStatement, error) {
	st, err := ParseKeysStatement(keys)
	if err != nil {
		return st, err
	}
	if st.Repo != repo {
		return KeysStatement{}, fmt.Errorf("KEYS is for %s, not %s", st.Repo, repo)
	}
	if _, err := (config.UpdateConfig{}).WithPins(st.FleetKeys, st.Require); err != nil {
		return KeysStatement{}, fmt.Errorf("KEYS: %w", err)
	}
	if err := verifySums(FleetKeysContext, repo, keysTag(st.Serial), keys, sig, ks); err != nil {
		return KeysStatement{}, fmt.Errorf("KEYS: %w", err)
	}
	return st, nil
}

// SignKeysLine returns the KEYS.sig line "<id>:ed25519:<base64>" for these KEYS bytes.
func SignKeysLine(id string, key ed25519.PrivateKey, repo string, serial uint64, keys []byte) string {
	sig := ed25519.Sign(key, signatureMessage(FleetKeysContext, repo, keysTag(serial), keys))
	return id + ":" + config.KeyEd25519 + ":" + base64.StdEncoding.EncodeToString(sig)
}

// VerifyKeys checks KEYS and KEYS.sig exactly as a daemon pinning keys/require does.
func VerifyKeys(keys []config.FleetKey, require []string, repo string, statement, sig []byte) (KeysStatement, error) {
	ks, err := newKeySet(keys, require)
	if err != nil {
		return KeysStatement{}, err
	}
	return verifyKeys(repo, statement, sig, ks)
}

// applyKeys applies the release's rotation statement, if it has one (see above). It runs at
// download, before the release's own signature is checked.
func (u *Updater) applyKeys(ctx context.Context, r release) error {
	keysA, ok := r.find("KEYS")
	sigA, okSig := r.find("KEYS.sig")
	if !ok && !okSig {
		return nil
	}
	if !ok || !okSig {
		return fmt.Errorf("release %s has KEYS or KEYS.sig without the other", r.TagName)
	}
	if u.o.Keys != nil {
		return errors.New("test keys are not rotated")
	}
	var keys, sig bytes.Buffer
	if err := u.fetch(ctx, keysA, &keys, maxKeysFile); err != nil {
		return err
	}
	if err := u.fetch(ctx, sigA, &sig, maxSig); err != nil {
		return err
	}
	if u.keysFetched != nil {
		u.keysFetched()
	}
	st, err := ParseKeysStatement(keys.Bytes())
	if err != nil {
		return fmt.Errorf("release %s: %w", r.TagName, err)
	}
	// From here to the save, the pins checked are the pins replaced: an owner's re-pin that lands
	// while KEYS downloads is what this statement must verify against (security-review #119).
	u.keysMu.Lock()
	defer u.keysMu.Unlock()
	cur := u.settings()
	if st.Serial <= cur.KeysSerial {
		// Checked before the signature: ignoring changes nothing, and an old statement signed by a
		// key dropped since must not block every later release that still carries it.
		u.o.Logger.Info("update: rotation statement already applied or older; ignored", "release", r.TagName, "serial", st.Serial, "applied", cur.KeysSerial)
		return nil
	}
	if st.Serial > cur.KeysSerial+maxSerialStep {
		// A key could otherwise burn the serial space (2^64-1) and no later rotation would apply.
		return fmt.Errorf("release %s: KEYS serial %d jumps more than %d past %d", r.TagName, st.Serial, maxSerialStep, cur.KeysSerial)
	}
	ks, err := u.trustedKeys()
	if err != nil {
		return err
	}
	if st, err = verifyKeys(u.Repo(), keys.Bytes(), sig.Bytes(), ks); err != nil {
		return fmt.Errorf("release %s: %w", r.TagName, err)
	}
	next, err := cur.WithPins(st.FleetKeys, st.Require)
	if err != nil {
		return err
	}
	next.KeysSerial = st.Serial
	if err := u.save(next); err != nil {
		return fmt.Errorf("rotation statement %d not saved: %w", st.Serial, err)
	}
	u.o.Logger.Warn("update: pinned keys rotated", "release", r.TagName, "serial", st.Serial, "keys", keyIDs(st.FleetKeys))
	return nil
}

func keyIDs(keys []config.FleetKey) string {
	var ids []string
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	return strings.Join(ids, ",")
}

func (u *Updater) save(c config.UpdateConfig) error {
	if u.o.Save == nil {
		return errors.New("this tmx cannot save its settings")
	}
	return u.o.Save(c)
}

// PinnedKeys is GET /api/update/keys: the pinned set (public keys only) and the last applied
// rotation serial; Error says why the set is invalid (updates are then off).
type PinnedKeys struct {
	FleetKeys  []config.FleetKey `json:"fleet_keys"`
	Require    []string          `json:"require"`
	KeysSerial uint64            `json:"keys_serial"`
	Error      string            `json:"error,omitempty"`
}

// Keys returns the pinned set.
func (u *Updater) Keys() PinnedKeys {
	s := u.settings()
	keys, require, err := s.PinnedKeys()
	p := PinnedKeys{FleetKeys: keys, Require: require, KeysSerial: s.KeysSerial}
	if err != nil {
		p.Error = err.Error()
	}
	if p.FleetKeys == nil {
		p.FleetKeys = []config.FleetKey{}
	}
	return p
}

// SetKeys replaces the pinned set (the owner's re-pin; the API gates it). The rotation serial is
// kept: a re-pin does not make older statements valid again.
func (u *Updater) SetKeys(keys []config.FleetKey, require []string) (PinnedKeys, error) {
	if u.o.Keys != nil {
		return PinnedKeys{}, errors.New("test keys are not re-pinned")
	}
	u.keysMu.Lock()
	next, err := u.settings().WithPins(keys, require)
	if err == nil {
		err = u.save(next)
	}
	u.keysMu.Unlock()
	if err != nil {
		return PinnedKeys{}, err
	}
	u.mu.Lock()
	switch reason := u.offReason(); {
	case reason != "":
		u.st.State, u.st.Reason = StateOff, reason
	case u.st.State == StateOff:
		u.st.State, u.st.Reason = StateIdle, ""
	}
	u.refreshSettingsLocked()
	u.publishLocked()
	u.mu.Unlock()
	return u.Keys(), nil
}
