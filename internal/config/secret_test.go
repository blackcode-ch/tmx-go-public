//go:build !windows || aclmutate

package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // ACLs, not modes: secret_windows_test.go
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("%s: mode %v, want %v", path, fi.Mode().Perm(), want)
	}
}

func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if len(got) != len(names) {
		t.Fatalf("%s holds %v, want %v (a temp file left behind?)", dir, got, names)
	}
	for i := range got {
		if got[i] != names[i] {
			t.Fatalf("%s holds %v, want %v", dir, got, names)
		}
	}
}

func TestWriteSecretFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home", "secrets") // missing: made private
	path := filepath.Join(dir, "github-token")
	if err := WriteSecretFile(path, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
	if s, err := ReadSecret(path); err != nil || s != "one" {
		t.Fatalf("read back %q, %v", s, err)
	}
	// Replacing a wider file from an older version leaves a private one.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSecretFile(path, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)
	if s, _ := ReadSecret(path); s != "two" {
		t.Fatalf("after replace: %q", s)
	}
	assertOnly(t, dir, "github-token")
}

func TestWriteSecretFileFailureKeepsOld(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user cannot write to")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "owner-token")
	if err := WriteSecretFile(path, []byte("old\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := WriteSecretFile(path, []byte("new\n")); err == nil {
		t.Fatal("the write must fail when no private temp file can be made")
	}
	if s, _ := ReadSecret(path); s != "old" {
		t.Fatalf("a failed write changed the secret: %q", s)
	}
	assertOnly(t, dir, "owner-token")
}

func TestWriteSecretFileRenameFailureCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gate.json")
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil { // the rename cannot replace it
		t.Fatal(err)
	}
	if err := WriteSecretFile(path, []byte("{}\n")); err == nil {
		t.Fatal("the rename over a non-empty folder must fail")
	}
	assertOnly(t, dir, "gate.json")
}

func TestCreateSecretFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	path := filepath.Join(dir, "p2p-key")
	f, err := CreateSecretFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("key")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
	if _, err := CreateSecretFile(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second create: %v, want os.ErrExist", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "key" {
		t.Fatalf("second create changed the file: %q", b)
	}
}

func TestMkdirSecret(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "secrets")
	if err := MkdirSecret(dir); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, 0o700)
	if err := MkdirSecret(dir); err != nil { // existing: fine
		t.Fatal(err)
	}
	p := Paths{Home: filepath.Join(t.TempDir(), "home")}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, p.SecretsDir(), 0o700)
}

// The call sites in this package still read back what they wrote.
func TestSecretCallSites(t *testing.T) {
	p := testPaths(t)
	tok, err := CompanionToken(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := CompanionToken(p, false); err != nil || again != tok {
		t.Fatalf("companion token read back %q, %v", again, err)
	}
	assertMode(t, p.CompanionTokenFile(), 0o600)
	if err := WriteSecret(p.CompanionAgentTokenFile(), " agent-key \n"); err != nil {
		t.Fatal(err)
	}
	if s, err := ReadSecret(p.CompanionAgentTokenFile()); err != nil || s != "agent-key" {
		t.Fatalf("agent key read back %q, %v", s, err)
	}
	assertMode(t, p.CompanionAgentTokenFile(), 0o600)
}
