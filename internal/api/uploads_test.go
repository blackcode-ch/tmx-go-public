package api_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

type upload struct {
	ID, Name, Type, Path, Data string
	AgentID                    string `json:"agent_id"`
	Size                       int64
	Image                      bool
}

func TestUploads(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	if s := owner.do("POST", "/api/projects", map[string]string{"name": "Alpha", "brief": "b"}, nil); s != 201 {
		t.Fatalf("project: %d", s)
	}
	lead, _ := d.Fleet.Agent("alpha-lead")
	head, _ := d.Fleet.Agent("head")

	// Claude agents may read their uploads folder without asking.
	updir := filepath.Join(d.Home, "uploads", lead.ID)
	if p := d.Proc(t, "alpha-lead"); !slices.Contains(p.Spec.AddDirs, updir) {
		t.Fatalf("AddDirs %v, want %s", p.Spec.AddDirs, updir)
	}

	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 100))
	var up upload
	body := map[string]string{"name": "../../shot 1.png", "data": base64.StdEncoding.EncodeToString(png)}
	if s := owner.do("POST", "/api/agents/alpha-lead/uploads", body, &up); s != 201 {
		t.Fatalf("upload: %d", s)
	}
	if up.Name != "shot 1.png" || up.Type != "image/png" || !up.Image || up.Size != int64(len(png)) || up.AgentID != lead.ID {
		t.Fatalf("upload: %+v", up)
	}
	if !strings.HasPrefix(up.Path, updir+string(filepath.Separator)) {
		t.Fatalf("path %s outside %s", up.Path, updir)
	}
	if b, err := os.ReadFile(up.Path); err != nil || string(b) != string(png) {
		t.Fatalf("stored file: %v", err)
	}
	var got upload
	if s := owner.do("GET", "/api/uploads/"+up.ID+"?data=1", nil, &got); s != 200 || got.Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("get with data: %d", s)
	}
	got = upload{}
	if s := owner.do("GET", "/api/uploads/"+up.ID, nil, &got); s != 200 || got.Data != "" || got.Name != "shot 1.png" {
		t.Fatalf("get: %d %+v", s, got)
	}

	// Refusals: not the owner, empty, too big, bad base64, unknown agent/upload.
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "alpha-lead")}
	if s := agent.do("POST", "/api/agents/alpha-lead/uploads", body, nil); s != 403 {
		t.Fatalf("agent upload: %d", s)
	}
	if s := agent.do("GET", "/api/uploads/"+up.ID, nil, nil); s != 403 {
		t.Fatalf("agent read: %d", s)
	}
	if s := owner.do("POST", "/api/agents/alpha-lead/uploads", map[string]string{"name": "e", "data": ""}, nil); s != 400 {
		t.Fatalf("empty: %d", s)
	}
	if s := owner.do("POST", "/api/agents/alpha-lead/uploads", map[string]string{"name": "e", "data": "@@"}, nil); s != 400 {
		t.Fatalf("bad base64: %d", s)
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, 20<<20+1))
	if s := owner.do("POST", "/api/agents/alpha-lead/uploads", map[string]string{"name": "big", "data": big}, nil); s != 413 {
		t.Fatalf("too big: %d", s)
	}
	if s := owner.do("POST", "/api/agents/ghost/uploads", body, nil); s != 404 {
		t.Fatalf("no agent: %d", s)
	}
	if s := owner.do("GET", "/api/uploads/nope", nil, nil); s != 404 {
		t.Fatalf("no upload: %d", s)
	}

	// Sending: the message lists the files with their paths; another agent's upload is refused.
	var note upload
	owner.do("POST", "/api/agents/alpha-lead/uploads", map[string]string{"name": "notes.txt", "type": "text/plain",
		"data": base64.StdEncoding.EncodeToString([]byte("hello"))}, &note)
	var m model.Message
	if s := owner.do("POST", "/api/agents/alpha-lead/send", map[string]any{"text": "look", "uploads": []string{up.ID, note.ID}}, &m); s != 200 {
		t.Fatalf("send: %d", s)
	}
	want := "look\n\nAttached files (open them from these paths):\n- shot 1.png (image/png, 108 B): " + up.Path +
		"\n- notes.txt (text/plain, 5 B): " + note.Path
	if m.Body != want {
		t.Fatalf("body:\n%s\nwant:\n%s", m.Body, want)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(d.Proc(t, "alpha-lead").Sent(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("not delivered: %q", d.Proc(t, "alpha-lead").Sent())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s := owner.do("POST", "/api/agents/alpha-lead/send", map[string]any{"uploads": []string{note.ID}}, &m); s != 200 ||
		!strings.HasPrefix(m.Body, "Attached files") {
		t.Fatalf("files only: %d %q", s, m.Body)
	}
	if s := owner.do("POST", "/api/agents/"+head.ID+"/send", map[string]any{"text": "x", "uploads": []string{up.ID}}, nil); s != 400 {
		t.Fatalf("other agent's upload: %d", s)
	}

	// Removing the agent removes its uploads.
	if s := owner.do("DELETE", "/api/agents/alpha-lead", nil, nil); s >= 300 {
		t.Fatalf("remove: %d", s)
	}
	if _, err := os.Stat(updir); !os.IsNotExist(err) {
		t.Fatalf("uploads left after removal: %v", err)
	}
}
