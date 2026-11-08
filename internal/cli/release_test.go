package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/release"
)

func TestReleaseKeygenPrintsOnlyThePublicKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv(release.EnvHome, home)
	var out, errw bytes.Buffer
	if code := run([]string{"release", "keygen"}, &out, &errw); code != 0 {
		t.Fatalf("keygen: %d %s", code, errw.String())
	}
	key, err := os.ReadFile(release.KeyFile(home, "fleet-release-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+errw.String(), strings.TrimSpace(string(key))) {
		t.Fatal("keygen printed the private key")
	}
	for _, want := range []string{"public key:", "fingerprint:", `"fleet_keys"`, `"fleet-release-v1"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %s:\n%s", want, out.String())
		}
	}
	keygenOut := out.String()
	out.Reset()
	if code := run([]string{"release", "keygen"}, &out, &errw); code == 0 || !strings.Contains(errw.String(), "exists") {
		t.Fatalf("second keygen: %d %s", code, errw.String())
	}
	if b, _ := os.ReadFile(release.KeyFile(home, "fleet-release-v1")); !bytes.Equal(b, key) {
		t.Fatal("key replaced")
	}
	// pubkey prints the same public key and fingerprint from the file, and still no seed.
	out.Reset()
	if code := run([]string{"release", "pubkey"}, &out, &errw); code != 0 {
		t.Fatalf("pubkey: %d %s", code, errw.String())
	}
	if strings.Contains(out.String(), strings.TrimSpace(string(key))) {
		t.Fatal("pubkey printed the private key")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if (strings.HasPrefix(line, "public key:") || strings.HasPrefix(line, "fingerprint:")) && !strings.Contains(keygenOut, line) {
			t.Errorf("pubkey says %q, keygen said:\n%s", line, keygenOut)
		}
	}
	if !strings.Contains(out.String(), "public key:") || !strings.Contains(out.String(), "fingerprint:") {
		t.Errorf("pubkey output: %s", out.String())
	}
	if code := run([]string{"release", "pubkey", "--id", "fleet-release-v2"}, &out, &errw); code == 0 {
		t.Fatal("pubkey of a missing key")
	}
	if code := run([]string{"release", "sign"}, &out, &errw); code == 0 {
		t.Fatal("sign without a tag")
	}
}

func TestReleaseSignFile(t *testing.T) {
	home := t.TempDir()
	pub, err := release.Keygen(home, "fleet-release-v1")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := release.KeyFile(home, pub.ID)
	sums := filepath.Join(home, "SHA256SUMS")
	sig := filepath.Join(home, "SHA256SUMS.sig")
	if err := os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  binary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	args := []string{"release", "sign-file", sums, "--key", keyPath, "--out", sig, "--tag", "fleet-v1.0.0"}
	if code := run(args, &out, &errw); code == 0 {
		t.Fatal("sign-file accepted no rebuild source")
	}
	args = append(args, "--source", home, "--commit", strings.Repeat("a", 40))
	if code := run(args, &out, &errw); code == 0 {
		t.Fatal("signed despite invalid source repository")
	}
	if _, err := os.Stat(sig); !os.IsNotExist(err) {
		t.Fatal("signature created before rebuild")
	}
	key, _ := os.ReadFile(keyPath)
	if strings.Contains(out.String()+errw.String(), strings.TrimSpace(string(key))) {
		t.Fatal("printed private key")
	}
}
