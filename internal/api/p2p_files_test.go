package api_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func TestP2PAgentFilesAPI(t *testing.T) {
	n := p2ptest.NewNetwork()
	a, b := apitest.NewWithNode(t, n.NewNode("a"), func(o *fleet.Options) { o.Workspace = p2pWorkspace(t) }), apitest.NewWithNode(t, n.NewNode("b"), func(o *fleet.Options) { o.Workspace = p2pWorkspace(t) })
	inv, err := a.Chats.CreateInvite(chat.InviteRequest{Kind: p2p.InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := b.Chats.Join(context.Background(), inv.Link)
	if err != nil {
		t.Fatal(err)
	}
	id := joined.Chat.ID
	waitFor(t, "pair", func() bool { _, err := a.Chats.Chat(id); return err == nil })
	aa, _ := a.Fleet.Agent("head")
	ba, _ := b.Fleet.Agent("head")
	ac := client{t: t, base: a.URL, token: a.AgentToken(t, "head")}
	bc := client{t: t, base: b.URL, token: b.AgentToken(t, "head")}
	put := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(aa.Cwd, "report.txt"), "a report")
	private := filepath.Join(p2pWorkspace(t), "private.json")
	put(private, "private fixture")
	if err := os.Link(private, filepath.Join(aa.Cwd, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if code := ac.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: id, Path: "linked.json"}, nil); code != 403 {
		t.Fatal("hardlinked state exported", code)
	}
	var sent model.ChatMessage
	if code := ac.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: id, Path: "report.txt", Caption: "review"}, &sent); code != 201 || len(sent.Files) != 1 {
		t.Fatalf("send: %d %+v", code, sent)
	}
	var files []model.ChatFile
	waitFor(t, "received file", func() bool {
		return bc.do("POST", "/api/p2p/files/list", api.P2PFilesRequest{Chat: id}, &files) == 200 && len(files) == 1 && files[0].State == model.ChatFileReady
	})
	fetch := api.P2PFetchFileRequest{ID: files[0].ID, Dest: "received.txt"}
	if code := bc.do("POST", "/api/p2p/files/fetch", fetch, nil); code != 201 {
		t.Fatal("fetch", code)
	}
	if data, err := os.ReadFile(filepath.Join(ba.Cwd, "received.txt")); err != nil || string(data) != "a report" {
		t.Fatal("wrong received bytes", err)
	}
	if code := bc.do("POST", "/api/p2p/files/fetch", fetch, nil); code != 403 {
		t.Fatal("overwrote existing file", code)
	}
	fetch.Dest = "received.pdf"
	if code := bc.do("POST", "/api/p2p/files/fetch", fetch, nil); code != 400 {
		t.Fatal("changed extension", code)
	}
	if err := os.Mkdir(filepath.Join(aa.Cwd, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	put(filepath.Join(aa.Cwd, ".ssh", "id_ed25519"), "private-key fixture")
	for _, path := range []string{"../outside.txt", "..\\outside.txt", filepath.Join(t.TempDir(), "outside.txt"), "report.txt:secret", ".env", ".ssh/id_ed25519", "AppData/Local/gh/hosts.yml", "client-credentials.json", "my-secret.txt", "hosts.yml", "signature.pdf", "run.ps1", "secrets/key.txt", ".git/config", filepath.Join(a.Home, "config.json")} {
		if code := ac.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: id, Path: path}, nil); code != 403 {
			t.Errorf("source %q: %d", path, code)
		}
		fetch.Dest = path
		if code := bc.do("POST", "/api/p2p/files/fetch", fetch, nil); code != 403 {
			t.Errorf("dest %q: %d", path, code)
		}
	}
	put(filepath.Join(aa.Cwd, "run.ps1"), "Write-Output should-not-run")
	put(filepath.Join(aa.Cwd, "fake.pdf"), "MZ executable")
	for _, path := range []string{"fake.pdf"} {
		if code := ac.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: id, Path: path}, nil); code != 400 {
			t.Errorf("blocked %q: %d", path, code)
		}
	}
	large, err := os.Create(filepath.Join(aa.Cwd, "large.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = large.Truncate(chat.MaxFileBytes + 1)
	large.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code := ac.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: id, Path: "large.txt"}, nil); code != 413 {
		t.Fatal("size cap", code)
	}

	// A junction/symlink to a folder outside the agent root cannot send or fetch.
	outside := t.TempDir()
	put(filepath.Join(outside, "private.txt"), "private")
	link := filepath.Join(aa.Cwd, "escape")
	if runtime.GOOS == "windows" {
		if out, err := hiddenexec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("junction: %v %s", err, out)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if code := ac.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: id, Path: "escape/private.txt"}, nil); code != 403 {
		t.Fatal("link source", code)
	}
	// Give a's agent a received file too, then test the same link on writing.
	if _, err := b.Chats.SendFile(fleet.AgentCaller(ba.ID), id, "back.txt", "", []byte("reply")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply file", func() bool {
		return ac.do("POST", "/api/p2p/files/list", api.P2PFilesRequest{Chat: id}, &files) == 200 && len(files) == 1 && files[0].State == model.ChatFileReady
	})
	if code := ac.do("POST", "/api/p2p/files/fetch", api.P2PFetchFileRequest{ID: files[0].ID, Dest: "escape/new.txt"}, nil); code != 403 {
		t.Fatal("link dest", code)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("wrote outside root")
	}
	worker := fileAgent(t, a)
	wc := client{t: t, base: a.URL, token: a.AgentToken(t, worker.ID)}
	if code := wc.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: id, Path: "report.txt"}, nil); code != 403 {
		t.Fatal("nonmember send", code)
	}
	if code := wc.do("POST", "/api/p2p/files/list", api.P2PFilesRequest{Chat: id}, nil); code != 403 {
		t.Fatal("nonmember list", code)
	}
	if code := wc.do("POST", "/api/p2p/files/fetch", api.P2PFetchFileRequest{ID: files[0].ID, Dest: "stolen.txt"}, nil); code != 404 {
		t.Fatal("nonmember fetch", code)
	}
	owner := client{t: t, base: a.URL, token: a.OwnerToken}
	if code := owner.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: id, Path: "report.txt"}, nil); code != 403 {
		t.Fatal("owner used agent filesystem route", code)
	}
}

func TestP2PFileToolsRefuseWorkingFolderInsideState(t *testing.T) {
	n := p2ptest.NewNetwork()
	d := apitest.NewWithNode(t, n.NewNode("a")) // default test workspace is beneath state
	a, _ := d.Fleet.Agent("head")
	ch, err := d.Chats.Create(chat.CreateRequest{Kind: model.ChatGroup, Name: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Cwd, "private.txt"), []byte("state"), 0600); err != nil {
		t.Fatal(err)
	}
	c := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	if code := c.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: ch.ID, Path: "private.txt"}, nil); code != 403 {
		t.Fatal("state workspace exported", code)
	}
}

// Keep workspaces outside AppData/temp application state on Windows.
func p2pWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "p2p-workspace-")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(abs); err != nil {
			t.Error(err)
		}
	})
	return abs
}

func TestP2PSendScansContentAndAllowsDocuments(t *testing.T) {
	n := p2ptest.NewNetwork()
	workspace := p2pWorkspace(t)
	d := apitest.NewWithNode(t, n.NewNode("a"), func(o *fleet.Options) { o.Workspace = workspace })
	ch, err := d.Chats.Create(chat.CreateRequest{Kind: model.ChatGroup, Name: "local"})
	if err != nil {
		t.Fatal(err)
	}
	c := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	for _, tc := range []struct {
		name, data string
		code       int
	}{
		{"report.txt", "gh" + "p_" + strings.Repeat("a", 36), 403},
		{"report.pdf", "%PDF-1.4\n" + "gh" + "p_" + strings.Repeat("a", 36), 403},
		{"late.txt", strings.Repeat("a", (1<<20)+8) + " sk-" + "test-value", 403},
		{"normal.pdf", "%PDF-1.4\nA normal report\n%%EOF", 201},
		{"normal.png", "\x89PNG\r\n\x1a\nordinary image fixture", 201},
	} {
		if err := os.WriteFile(filepath.Join(workspace, tc.name), []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		code := c.do("POST", "/api/p2p/files/send", api.P2PSendFileRequest{Chat: ch.ID, Path: tc.name}, &response)
		if code != tc.code {
			t.Fatalf("%s: %d %v", tc.name, code, response)
		}

	}
}
