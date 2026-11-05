package release

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// The statement verifies with the signer's pinned key, never prints the private key, refuses a
// used serial, an invalid set and existing files.
func TestSignKeys(t *testing.T) {
	home := t.TempDir()
	v1, err := Keygen(home, "k1")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := Keygen(home, "k2")
	if err != nil {
		t.Fatal(err)
	}
	set, _ := json.Marshal(map[string]any{"fleet_keys": []config.FleetKey{
		{ID: "k1", Type: config.KeyEd25519, Key: v1.Key}, {ID: "k2", Type: config.KeyEd25519, Key: v2.Key}}})
	dir := t.TempDir()
	var out bytes.Buffer
	o := KeysOptions{Home: home, Repo: "o/r", KeyID: "k1", Serial: 1, Set: set, Dir: dir, Out: &out}
	if err := SignKeys(o); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "KEYS"))
	sig, _ := os.ReadFile(filepath.Join(dir, "KEYS.sig"))
	st, err := update.VerifyKeys([]config.FleetKey{{ID: "k1", Type: config.KeyEd25519, Key: v1.Key}}, nil, "o/r", body, sig)
	if err != nil || st.Serial != 1 || len(st.FleetKeys) != 2 {
		t.Fatalf("verify: %+v %v", st, err)
	}
	seed, _ := os.ReadFile(KeyFile(home, "k1"))
	if strings.Contains(out.String(), strings.TrimSpace(string(seed))) || !strings.Contains(out.String(), v2.Fingerprint) {
		t.Fatalf("output: %s", out.String())
	}

	again := o
	again.Dir = t.TempDir()
	for name, bad := range map[string]func(*KeysOptions){
		"used serial":   func(o *KeysOptions) {},
		"lower serial":  func(o *KeysOptions) { o.Serial = 0 },
		"empty set":     func(o *KeysOptions) { o.Serial, o.Set = 2, []byte(`{"fleet_keys":[]}`) },
		"unknown field": func(o *KeysOptions) { o.Serial, o.Set = 2, []byte(`{"keys":[]}`) },
		"bad repo":      func(o *KeysOptions) { o.Serial, o.Repo = 2, "../x" },
		"no such key":   func(o *KeysOptions) { o.Serial, o.KeyID = 2, "k9" },
		"files exist":   func(o *KeysOptions) { o.Serial, o.Dir = 2, dir },
	} {
		c := again
		bad(&c)
		if err := SignKeys(c); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
	if err := SignKeys(KeysOptions{Home: home, Repo: "o/r", KeyID: "k2", Serial: 2, Set: set, Dir: again.Dir}); err != nil {
		t.Fatalf("serial 2: %v", err)
	}
}
