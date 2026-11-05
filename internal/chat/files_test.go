package chat

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func fileOf(t *testing.T, s *side, chatID, msgID string) model.ChatFile {
	t.Helper()
	for _, m := range messages(t, s, chatID) {
		if m.ID == msgID && len(m.Files) > 0 {
			return m.Files[0]
		}
	}
	return model.ChatFile{}
}

func TestFileInDM(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo := newSide(t, n, "alex"), newSide(t, n, "bo")
	id := pairDM(t, alex, bo)
	data := bytes.Repeat([]byte("menu,price\n"), 5000)
	up, err := alex.svc.Upload(id, "../menu.csv", "", data)
	if err != nil || up.Name != "menu.csv" || up.Type != "text/csv" || up.State != model.ChatFileReady || up.Path == "" {
		t.Fatalf("upload: %+v %v", up, err)
	}
	// Agents cannot attach; an unknown upload is refused; an unsent upload is not served.
	if _, err := alex.svc.SendWith(fleet.AgentCaller("h1"), id, SendRequest{Body: "x", Files: []string{up.ID}}); err == nil {
		t.Fatal("an agent attached a file")
	}
	if _, err := alex.svc.SendWith(owner, id, SendRequest{Files: []string{"nope"}}); err == nil {
		t.Fatal("unknown upload accepted")
	}
	if _, _, err := alex.svc.serveFile(p2p.FileRequest{PeerID: bo.node.ID(), ChatID: id, FileID: up.ID}); err == nil {
		t.Fatal("served an upload that was never sent")
	}
	m, err := alex.svc.SendWith(owner, id, SendRequest{Files: []string{up.ID}}) // files only, no text
	if err != nil || len(m.Files) != 1 || m.Files[0].MessageID != m.ID {
		t.Fatalf("send: %+v %v", m, err)
	}
	if _, err := alex.svc.SendWith(owner, id, SendRequest{Body: "again", Files: []string{up.ID}}); err == nil {
		t.Fatal("a sent file was attached twice")
	}
	eventually(t, "bo has the file", func() bool { return fileOf(t, bo, id, m.ID).State == model.ChatFileReady })
	f := fileOf(t, bo, id, m.ID)
	got, err := os.ReadFile(f.Path)
	if err != nil || !bytes.Equal(got, data) || f.Sender != alex.node.ID() || f.Size != int64(len(data)) {
		t.Fatalf("bo's copy: %+v %v", f, err)
	}
	if _, b, err := bo.svc.ReadFile(owner, id, f.ID); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("ReadFile: %v", err)
	}
	// bo's head got the message with the path to read.
	eventually(t, "mail to bo's head", func() bool {
		for _, mi := range bo.ag.Mail() {
			if strings.Contains(mi.text, "Attached files") && strings.Contains(mi.text, f.Path) {
				return true
			}
		}
		return false
	})
	// Only the DM's peer is served.
	if _, _, err := alex.svc.serveFile(p2p.FileRequest{PeerID: "peer-mallory", ChatID: id, FileID: up.ID}); err == nil {
		t.Fatal("served a stranger")
	}
	if r, size, err := alex.svc.serveFile(p2p.FileRequest{PeerID: bo.node.ID(), ChatID: id, FileID: up.ID}); err != nil || size != int64(len(data)) {
		t.Fatalf("serve to bo: %v", err)
	} else {
		r.Close()
	}
}

func TestFileRetriedWhenAuthorComesBack(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo := newSide(t, n, "alex"), newSide(t, n, "bo")
	id := pairDM(t, alex, bo)
	alex.node.ServeFiles(nil) // alex cannot serve for now (as if the file were unreachable)
	up, _ := alex.svc.Upload(id, "photo.png", "image/png", []byte("\x89PNG\r\n\x1a\nxxxx"))
	m, err := alex.svc.SendWith(owner, id, SendRequest{Body: "look", Files: []string{up.ID}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bo tried", func() bool {
		f, err := bo.st.GetChatFile(id, up.ID)
		return err == nil && f.Tries >= 1 && f.State == model.ChatFilePending
	})
	if f := fileOf(t, bo, id, m.ID); !f.Image || f.Path != "" {
		t.Fatalf("pending file: %+v", f)
	}
	alex.node.ServeFiles(alex.svc.serveFile)
	bo.svc.peerOnlineForFiles(alex.node.ID())
	eventually(t, "bo has it", func() bool { return fileOf(t, bo, id, m.ID).State == model.ChatFileReady })
}

func TestGroupFileFromAnotherMember(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	cleo.node.SetOnline(false)
	up, _ := alex.svc.Upload(gid, "plan.txt", "", []byte("day 1: hike"))
	m, err := alex.svc.SendWith(owner, gid, SendRequest{Body: "the plan", Files: []string{up.ID}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bo has it", func() bool { return fileOf(t, bo, gid, m.ID).State == model.ChatFileReady })
	alex.node.ServeFiles(nil) // alex is gone as a source
	cleo.node.SetOnline(true)
	eventually(t, "cleo got it from bo", func() bool { return fileOf(t, cleo, gid, m.ID).State == model.ChatFileReady })
	if b, _ := os.ReadFile(fileOf(t, cleo, gid, m.ID).Path); string(b) != "day 1: hike" {
		t.Fatalf("cleo's copy %q", b)
	}
}

// A source that sends other bytes than announced: the file is not stored.
func TestDamagedFileIsRejected(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo := newSide(t, n, "alex"), newSide(t, n, "bo")
	id := pairDM(t, alex, bo)
	alex.node.ServeFiles(func(req p2p.FileRequest) (io.ReadCloser, int64, error) {
		return io.NopCloser(strings.NewReader("EVIL")), 4, nil
	})
	up, _ := alex.svc.Upload(id, "a.txt", "", []byte("good"))
	m, _ := alex.svc.SendWith(owner, id, SendRequest{Files: []string{up.ID}})
	eventually(t, "a failed attempt", func() bool {
		f, err := bo.st.GetChatFile(id, up.ID)
		return err == nil && f.Tries >= 1
	})
	if f := fileOf(t, bo, id, m.ID); f.State == model.ChatFileReady {
		t.Fatalf("damaged file accepted: %+v", f)
	}
	if _, _, err := bo.svc.ReadFile(owner, id, up.ID); err == nil {
		t.Fatal("read a file that is not there")
	}
}

func TestMentions(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	helper := "w1"
	if _, err := cleo.svc.Update(gid, ChatPatch{AgentID: &helper}); err != nil {
		t.Fatal(err)
	}
	// cleo writes once, so the others learn that "helper" is cleo's agent in this chat.
	if _, err := cleo.svc.Send(owner, gid, "hi all"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alex can mention cleo/helper", func() bool {
		ms, _ := alex.svc.Mentionable(owner, gid)
		for _, m := range ms {
			if m.Label == "cleo/helper" && m.Agent == "helper" && m.PeerID == cleo.node.ID() {
				return true
			}
		}
		return false
	})
	m, err := alex.svc.Send(owner, gid, "@Bo can you book it? @cleo/helper check the trains. mail me at x@bo.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Mentions) != 2 || m.Mentions[0].PeerID != bo.node.ID() || m.Mentions[0].Label != "Bo" ||
		m.Mentions[1].Agent != "helper" {
		t.Fatalf("mentions: %+v", m.Mentions)
	}
	eventually(t, "bo is mentioned", func() bool {
		c, err := bo.svc.Chat(gid)
		return err == nil && c.Mentions == 1
	})
	for _, x := range messages(t, bo, gid) {
		if x.ID == m.ID && !x.MentionsMe {
			t.Fatal("bo's copy does not mention bo")
		}
	}
	for _, x := range messages(t, cleo, gid) {
		if x.ID == m.ID && x.MentionsMe {
			t.Fatal("cleo (the person) was not mentioned")
		}
	}
	eventually(t, "cleo's helper is told", func() bool {
		for _, mi := range cleo.ag.Mail() {
			if mi.agentID == "w1" && strings.Contains(mi.text, "[this message @mentions you]") && strings.Contains(mi.text, "trains") {
				return true
			}
		}
		return false
	})
	if u, _ := bo.svc.Unread(); u.Mentions != 1 {
		t.Fatalf("unread mentions %d", u.Mentions)
	}
	if _, err := bo.svc.MarkRead(gid); err != nil {
		t.Fatal(err)
	}
	if c, _ := bo.svc.Chat(gid); c.Mentions != 0 {
		t.Fatal("mentions not cleared by reading")
	}
}

func TestParseMentions(t *testing.T) {
	c := []Mentionable{{Label: "Alex", PeerID: "a"}, {Label: "Alex Weber", PeerID: "aw"}, {Label: "Alex/head", PeerID: "a", Agent: "head"}}
	for body, want := range map[string]string{
		"@alex hi":                 "a/",
		"hi @Alex Weber!":          "aw/",
		"@Alex/head and @alex":     "a/head,a/",
		"mail@alex.com":            "",
		"@Alexander":               "",
		"(@Alex)":                  "a/",
		"@Alex @Alex @Alex":        "a/",
		"@nobody":                  "",
		"@Alex Weber, @Alex/heads": "aw/,a/", // "/heads" is not the agent: Alex alone
	} {
		var got []string
		for _, m := range parseMentions(body, c) {
			got = append(got, m.PeerID+"/"+m.Agent)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%q: got %v, want %s", body, got, want)
		}
	}
	if _, me, ag := acceptMentions([]wireMention{{PeerID: "me", Label: "x"}, {PeerID: "me", Agent: "HEAD", Label: "y"}}, "me", "head"); !me || !ag {
		t.Fatal("acceptMentions")
	}
	_ = errors.New
}
