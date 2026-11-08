package transfers

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExportLifecycle(t *testing.T) {
	home, install := t.TempDir(), t.TempDir()
	x := NewExports(filepath.Join(t.TempDir(), "exports"))
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	x.now = func() time.Time { return now }
	src := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := x.Prepare(src, "hosta", "/inbox", "id1", home, install)
	if err != nil {
		t.Fatal(err)
	}
	if e.Size != 5 || e.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" || len(e.Ticket) != 32 || e.Kind != "file" {
		t.Fatalf("export %+v", e)
	}
	if _, err := x.Get(e.Ticket, "hostc"); !errors.Is(err, ErrNotYours) {
		t.Fatalf("another machine: %v", err)
	}
	if got, err := x.Get(e.Ticket, "hosta"); err != nil || got.Path != e.Path {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := x.Get("../../etc/passwd", "hosta"); !errors.Is(err, ErrNoExport) {
		t.Fatalf("a path as ticket: %v", err)
	}
	if err := x.Done(e.Ticket, "hostc"); !errors.Is(err, ErrNotYours) {
		t.Fatalf("done by another: %v", err)
	}
	if err := x.Done(e.Ticket, "hosta"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Get(e.Ticket, "hosta"); !errors.Is(err, ErrNoExport) {
		t.Fatalf("after done: %v", err)
	}

	// Expired after 24 h; a changed file is not served.
	e2, _ := x.Prepare(src, "hosta", "/inbox", "id2", home, install)
	if err := os.WriteFile(src, []byte("changed!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Get(e2.Ticket, "hosta"); err == nil {
		t.Fatal("a changed file was served")
	}
	now = now.Add(ExportTTL + time.Minute)
	if _, err := x.Get(e2.Ticket, "hosta"); !errors.Is(err, ErrNoExport) {
		t.Fatalf("expired: %v", err)
	}
}

func TestForbiddenSources(t *testing.T) {
	home, install := t.TempDir(), t.TempDir()
	in := func(dir, name string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("x"), 0o600)
		return p
	}
	if ForbiddenSource(in(home, "tokens.json"), home, install) == "" {
		t.Error("tmx state accepted")
	}
	if ForbiddenSource(in(install, "tmx.exe"), home, install) == "" {
		t.Error("the installation accepted")
	}
	if u, err := os.UserHomeDir(); err == nil {
		if ForbiddenSource(filepath.Join(u, ".ssh", "id_ed25519"), home, install) == "" {
			t.Error("a credential store accepted")
		}
	}
	ok := in(t.TempDir(), "photo.png")
	if why := ForbiddenSource(ok, home, install); why != "" {
		t.Errorf("an ordinary file refused: %s", why)
	}
	x := NewExports(t.TempDir())
	if _, err := x.Prepare(in(home, "s.txt"), "hosta", "/x", "i", home, install); !errors.Is(err, ErrForbidden) {
		t.Fatalf("prepare from state: %v", err)
	}
	if _, err := x.Prepare(t.TempDir(), "hosta", "/x", "i", home, install); err == nil {
		t.Fatal("a directory was accepted as a direct export without packing")
	}
}

// A file swapped in under the offered path after the offer (a parent folder re-pointed with a
// link) is not served (o5, #259). Links need privileges on Windows: Unix only.
func TestRepointedPathIsNotServed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	base := t.TempDir()
	a := filepath.Join(base, "a")
	_ = os.MkdirAll(a, 0o700)
	src := filepath.Join(a, "f.txt")
	_ = os.WriteFile(src, []byte("hello"), 0o600)
	x := NewExports(t.TempDir())
	e, err := x.Prepare(src, "hosta", "/x", "i", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// a/ becomes a link to b/, which holds a file of the same size and time
	b := filepath.Join(base, "b")
	_ = os.MkdirAll(b, 0o700)
	_ = os.WriteFile(filepath.Join(b, "f.txt"), []byte("other"), 0o600)
	st, _ := os.Stat(src)
	_ = os.Chtimes(filepath.Join(b, "f.txt"), st.ModTime(), st.ModTime())
	_ = os.RemoveAll(a)
	if err := os.Symlink(b, a); err != nil {
		t.Skip(err)
	}
	if _, err := x.Get(e.Ticket, "hosta"); err == nil {
		t.Fatal("a re-pointed path was served")
	}
}

// The Python tmx's state (extra) is never a source either.
func TestExtraForbiddenRoot(t *testing.T) {
	py := t.TempDir()
	p := filepath.Join(py, "tokens.json")
	_ = os.WriteFile(p, []byte("x"), 0o600)
	if ForbiddenSource(p, t.TempDir(), t.TempDir(), py) == "" {
		t.Fatal("the Python tmx's state accepted")
	}
}
