package release

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// A rotation statement (`tmx release keys`, DESIGN.md "Updates", rotation): KEYS, the whole new
// pin set for repo with a serial, and KEYS.sig, signed by a key the daemons pin now. Both files
// go into a directory; they are attached to the next release (`gh release upload`) before it is
// signed. keys.log in the release home holds the serials used: a serial is never signed twice.

// KeysOptions is one rotation statement.
type KeysOptions struct {
	Home, Repo, KeyID string
	Serial            uint64
	// Set is the new pin set: JSON {fleet_keys, require}.
	Set []byte
	// Dir receives KEYS and KEYS.sig; neither may exist yet.
	Dir string
	Out io.Writer
}

// KeysLogFile is the record of signed rotation serials.
func KeysLogFile(home string) string { return filepath.Join(home, "keys.log") }

// SignKeys writes KEYS and KEYS.sig. It prints only paths, ids and fingerprints, never a secret.
func SignKeys(o KeysOptions) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if owner, name, _ := strings.Cut(o.Repo, "/"); !repoRx.MatchString(o.Repo) || owner == "." || owner == ".." || name == "." || name == ".." {
		return fmt.Errorf("repository %q: want OWNER/NAME", o.Repo)
	}
	if o.Serial == 0 {
		return errors.New("--serial must be 1 or more, above every serial signed before")
	}
	var set struct {
		FleetKeys []config.FleetKey `json:"fleet_keys"`
		Require   []string          `json:"require"`
	}
	dec := json.NewDecoder(bytes.NewReader(o.Set))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&set); err != nil {
		return fmt.Errorf("the new key set is not {fleet_keys, require}: %w", err)
	}
	if _, err := (config.UpdateConfig{}).WithPins(set.FleetKeys, set.Require); err != nil {
		return fmt.Errorf("the new key set: %w", err)
	}
	last, err := lastKeysSerial(o.Home)
	if err != nil {
		return err
	}
	if o.Serial <= last {
		return fmt.Errorf("serial %d is not above %d, the last one signed here (%s)", o.Serial, last, KeysLogFile(o.Home))
	}
	key, err := LoadKey(o.Home, o.KeyID)
	if err != nil {
		return err
	}
	pub := key.Public().(ed25519.PublicKey)
	st := update.KeysStatement{Repo: o.Repo, Serial: o.Serial, FleetKeys: set.FleetKeys, Require: set.Require}
	if st.Require == nil {
		st.Require = []string{config.KeyEd25519}
	}
	body, err := update.MarshalKeysStatement(st)
	if err != nil {
		return err
	}
	sig := []byte(update.SignKeysLine(o.KeyID, key, o.Repo, o.Serial, body) + "\n")
	// Check it as a daemon pinning only this key would.
	signer := config.FleetKey{ID: o.KeyID, Type: config.KeyEd25519, Key: base64.StdEncoding.EncodeToString(pub)}
	if _, err := update.VerifyKeys([]config.FleetKey{signer}, nil, o.Repo, body, sig); err != nil {
		return fmt.Errorf("the statement does not verify: %w", err)
	}
	keysPath, sigPath := filepath.Join(o.Dir, "KEYS"), filepath.Join(o.Dir, "KEYS.sig")
	for _, p := range []string{keysPath, sigPath} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("refusing: %s exists", p)
		}
	}
	if err := os.WriteFile(keysPath, body, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(sigPath, sig, 0o644); err != nil {
		os.Remove(keysPath)
		return err
	}
	if err := appendKeysLog(o.Home, o.Serial); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Rotation statement %d for %s, signed by %s (%s):\n", o.Serial, o.Repo, o.KeyID, Fingerprint(pub))
	for _, k := range set.FleetKeys {
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(k.Key))
		fmt.Fprintf(o.Out, "  pins %s %s (%s)\n", k.ID, k.Type, Fingerprint(raw))
	}
	fmt.Fprintf(o.Out, "%s\n%s\nAttach both to the next release before signing it: gh release upload TAG KEYS KEYS.sig\n", keysPath, sigPath)
	return nil
}

func lastKeysSerial(home string) (uint64, error) {
	b, err := os.ReadFile(KeysLogFile(home))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var last uint64
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		n, err := strconv.ParseUint(line, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s is corrupt", KeysLogFile(home))
		}
		last = max(last, n)
	}
	return last, sc.Err()
}

func appendKeysLog(home string, serial uint64) error {
	f, err := os.OpenFile(KeysLogFile(home), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%d\n", serial)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
