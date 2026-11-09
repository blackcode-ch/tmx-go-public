package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func TestP2PFileTools(t *testing.T) {
	n := p2ptest.NewNetwork()
	a, b := apitest.NewWithNode(t, n.NewNode("alex"), func(o *fleet.Options) { o.Workspace = t.TempDir() }), apitest.NewWithNode(t, n.NewNode("bo"), func(o *fleet.Options) { o.Workspace = t.TempDir() })
	inv, err := a.Chats.CreateInvite(chat.InviteRequest{Kind: p2p.InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := b.Chats.Join(context.Background(), inv.Link)
	if err != nil {
		t.Fatal(err)
	}
	id := joined.Chat.ID
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := a.Chats.Chat(id); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pair timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	aa, _ := a.Fleet.Agent("head")
	ba, _ := b.Fleet.Agent("head")
	if err := os.WriteFile(filepath.Join(aa.Cwd, "notes.txt"), []byte("untrusted content"), 0600); err != nil {
		t.Fatal(err)
	}
	as, bs := session(t, a, "head"), session(t, b, "head")
	out := mustOK(t, as, "p2p_send_file", map[string]any{"chat": id, "path": "notes.txt", "caption": "notes"})
	if !strings.HasPrefix(out, External+"\n") {
		t.Fatal("missing external boundary", out)
	}
	var m model.ChatMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(out, External+"\n")), &m); err != nil || len(m.Files) != 1 {
		t.Fatal(out, err)
	}
	var files []model.ChatFile
	deadline = time.Now().Add(5 * time.Second)
	for {
		out = mustOK(t, bs, "p2p_files", map[string]any{"chat": id})
		if err := json.Unmarshal([]byte(strings.TrimPrefix(out, External+"\n")), &files); err != nil {
			t.Fatal(err, out)
		}
		if len(files) == 1 && files[0].State == model.ChatFileReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receive timeout", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	out = mustOK(t, bs, "p2p_fetch_file", map[string]any{"id": files[0].ID, "dest": "received.txt"})
	if !strings.HasPrefix(out, External+"\n") {
		t.Fatal(out)
	}
	if data, err := os.ReadFile(filepath.Join(ba.Cwd, "received.txt")); err != nil || string(data) != "untrusted content" {
		t.Fatal("fetch", err)
	}
	if _, isErr := call(t, bs, "p2p_fetch_file", map[string]any{"id": files[0].ID, "dest": "../escape.txt"}); !isErr {
		t.Fatal("fetch escaped root")
	}
}
