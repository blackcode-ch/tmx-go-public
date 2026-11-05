package skill

import (
	"strings"
	"testing"
)

func TestFrontMatter(t *testing.T) {
	s := string(Content())
	head, _, ok := strings.Cut(strings.TrimPrefix(s, "---\n"), "\n---\n")
	if !strings.HasPrefix(s, "---\n") || !ok || !strings.Contains(head, "name: tmxg\n") || !strings.Contains(head, "description: ") {
		t.Fatalf("front matter: %q", s[:min(len(s), 200)])
	}
	c := Content()
	c[0] = 'x'
	if Content()[0] != '-' {
		t.Fatal("Content shares the embedded bytes")
	}
}
