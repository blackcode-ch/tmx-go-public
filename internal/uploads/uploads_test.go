package uploads

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"a.png":              "a.png",
		"../../etc/passwd":   "passwd",
		`C:\Users\x\doc.pdf`: "doc.pdf",
		".env":               "env",
		"":                   "file",
		"..":                 "file",
		"a\x00b:c?.txt":      "a_b_c_.txt",
		"report. ":           "report",
		"a\nb\u2028c\u2029d": "a_b_c_d",
	} {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
	long := CleanName(strings.Repeat("ä", 300) + ".tar.gz")
	if r := []rune(long); len(r) != maxNameRunes || !strings.HasSuffix(long, ".gz") {
		t.Errorf("long name: %d runes, %q", len(r), long[len(long)-8:])
	}
}

func TestStore(t *testing.T) {
	s := New(config.Paths{Home: t.TempDir()})
	f, err := s.Save("agent1", "notes", "", []byte("%PDF-1.7 hello"))
	if err != nil || f.Type != "application/pdf" {
		t.Fatalf("save: %v %+v", err, f)
	}
	if _, err := s.Save("../x", "a", "", []byte("a")); err == nil {
		t.Fatal("bad agent id accepted")
	}
	g, b, err := s.Read(f.ID)
	if err != nil || g.Path != f.Path || string(b) != "%PDF-1.7 hello" {
		t.Fatalf("read: %v %+v", err, g)
	}
	if _, err := s.Get("../" + f.ID); err != ErrNotFound {
		t.Fatalf("traversal: %v", err)
	}
	other, _ := s.Save("agent2", "a.txt", "text/plain", []byte("a"))
	s.Prune(func(id string) bool { return id == "agent2" })
	if _, err := s.Get(f.ID); err != ErrNotFound {
		t.Fatal("pruned agent's upload still there")
	}
	if _, err := s.Get(other.ID); err != nil {
		t.Fatal("kept agent's upload pruned")
	}
	if WithNote("", nil) != "" || WithNote("hi\n", nil) != "hi\n" {
		t.Fatal("note without files")
	}
}
