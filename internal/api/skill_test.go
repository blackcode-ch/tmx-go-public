package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/skill"
)

// GET /skill.md is public like the UI (no token) and serves the embedded SKILL.md.
func TestSkillMarkdownIsPublic(t *testing.T) {
	d := apitest.New(t)
	resp, err := http.Get(d.URL + "/skill.md")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/markdown") || string(body) != string(skill.Content()) {
		t.Fatalf("%d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body[:min(len(body), 80)])
	}
	if strings.Contains(string(body), d.OwnerToken) {
		t.Fatal("skill.md carries the owner token")
	}
	if resp, err := http.Post(d.URL+"/skill.md", "text/plain", nil); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 && resp.Header.Get("Content-Type") == "text/markdown; charset=utf-8" {
			t.Fatal("POST served the skill")
		}
	}
}
