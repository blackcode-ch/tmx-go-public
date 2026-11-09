package chat

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func TestAgentFileTransferIsInert(t *testing.T) {
	n := p2ptest.NewNetwork()
	a, b := newSide(t, n, "alex"), newSide(t, n, "bo")
	id := pairDM(t, a, b)
	marker := filepath.Join(t.TempDir(), "executed")
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("run.sh")
	fmt.Fprintf(w, "#!/bin/sh\necho executed > %q\n", marker)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := a.svc.SendFile(fleet.AgentCaller("h1"), id, "bundle.zip", "archive for review", buf.Bytes())
	if err != nil || m.SenderAgent != "head" || len(m.Files) != 1 {
		t.Fatalf("send: %+v %v", m, err)
	}
	eventually(t, "received archive", func() bool { return fileOf(t, b, id, m.ID).State == model.ChatFileReady })
	list, err := b.svc.ReceivedFiles(fleet.AgentCaller("h1"), id)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	f, data, err := b.svc.ReceivedFile(fleet.AgentCaller("h1"), list[0].ID)
	if err != nil || !bytes.Equal(data, buf.Bytes()) || f.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatalf("fetch: %+v %v", f, err)
	}
	if rel, err := filepath.Rel(b.svc.o.FilesDir, f.Path); err != nil || !filepath.IsLocal(rel) {
		t.Fatal("file escaped inbox", f.Path)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("received archive was executed")
	}
	entries, err := os.ReadDir(filepath.Dir(f.Path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("archive extracted: %v %v", entries, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(f.Path)
		if st.Mode().Perm()&0111 != 0 {
			t.Fatal("received file executable")
		}
	}
	if _, err := a.svc.SendFile(fleet.AgentCaller("w1"), id, "a.txt", "", []byte("private")); err == nil {
		t.Fatal("non-member sent")
	}
	if _, err := b.svc.ReceivedFiles(fleet.AgentCaller("w1"), id); err == nil {
		t.Fatal("non-member listed")
	}
	if _, _, err := b.svc.ReceivedFile(fleet.AgentCaller("w1"), f.ID); err == nil {
		t.Fatal("non-member fetched")
	}
	if _, _, err := a.svc.serveFile(p2p.FileRequest{PeerID: "stranger", ChatID: id, FileID: f.ID}); err == nil {
		t.Fatal("non-peer downloaded")
	}
	for i := 1; i < MaxAgentRun; i++ {
		if _, err := a.svc.Send(fleet.AgentCaller("h1"), id, "message"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.svc.SendFile(fleet.AgentCaller("h1"), id, "a.txt", "", []byte("rate limited")); err == nil {
		t.Fatal("file bypassed agent-run limit")
	}
	if pending, _ := a.st.UnsentChatFiles(model.Now() + 1); len(pending) != 0 {
		t.Fatal("failed send left an upload", pending)
	}
}

func TestFileSizeAndTypePolicy(t *testing.T) {
	n := p2ptest.NewNetwork()
	a := newSide(t, n, "alex", func(o *Options) { o.FileMaxBytes = 8 })
	b := newSide(t, n, "bo")
	id := pairDM(t, a, b)
	for _, tc := range []struct{ name, data string }{
		{"large.txt", "123456789"}, {"x.EXE", "x"}, {"x.ps1", "x"}, {"x.sh", "x"}, {"x.js", "x"}, {"x.svg", "x"},
		{"x.txt", "#!run"}, {"x.pdf", "MZfake"}, {"x.png", "\x7fELF"}, {"x.docm", "x"},
	} {
		if _, err := a.svc.SendFile(fleet.AgentCaller("h1"), id, tc.name, "", []byte(tc.data)); err == nil {
			t.Errorf("accepted %q", tc.name)
		}
	}
	if _, err := a.svc.SendFile(fleet.AgentCaller("h1"), id, "ok.txt", "", []byte("12345678")); err != nil {
		t.Fatal("cap boundary", err)
	}
	for _, name := range []string{"a.pdf", "a.png", "a.mp3", "a.zip", "a.docx", "a.tar.gz"} {
		if err := validateFile(name, []byte("fixture")); err != nil {
			t.Errorf("refused allowed %s: %v", name, err)
		}
	}
	a.svc.mu.Lock()
	ch, _ := a.st.GetChat(id)
	for _, f := range []wireFile{
		{ID: "big", Name: "big.txt", Size: 9, SHA256: strings.Repeat("a", 64)},
		{ID: "script", Name: "bad.sh", Size: 2, SHA256: strings.Repeat("a", 64)},
		{ID: "svg", Name: "bad.txt", Type: "image/svg+xml", Size: 2, SHA256: strings.Repeat("a", 64)},
	} {
		if a.svc.acceptFilesLocked(ch, b.node.ID(), "message", []wireFile{f}) != 0 {
			t.Error("received metadata bypassed policy")
		}
	}
	a.svc.mu.Unlock()
}

func TestReceivedDisguisedExecutableRejected(t *testing.T) {
	for _, governed := range []bool{false, true} {
		t.Run(fmt.Sprint(governed), func(t *testing.T) {
			n := p2ptest.NewNetwork()
			a := newSide(t, n, "alex")
			b := newSide(t, n, "bo", func(o *Options) {
				if governed {
					o.Jobs = chatJobs(t)
				}
			})
			id := pairDM(t, a, b)
			data := []byte("#!/bin/sh\necho do-not-run\n")
			a.node.ServeFiles(func(p2p.FileRequest) (io.ReadCloser, int64, error) {
				return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
			})
			b.svc.mu.Lock()
			ch, _ := b.st.GetChat(id)
			f := wireFile{ID: "disguised", Name: "report.txt", Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
			b.svc.acceptFilesLocked(ch, a.node.ID(), "message", []wireFile{f})
			b.svc.mu.Unlock()
			eventually(t, "blocked content", func() bool { got, _ := b.st.GetChatFile(id, f.ID); return got.State == model.ChatFileFailed })
			if _, err := os.Stat(b.svc.filePath(model.ChatFile{ChatID: id, ID: f.ID, Name: f.Name})); !os.IsNotExist(err) {
				t.Fatal("executable placed in inbox")
			}
		})
	}
}
