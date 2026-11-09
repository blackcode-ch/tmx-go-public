package chat

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/jobs"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func chatJobs(t *testing.T) *jobs.Manager {
	t.Helper()
	m, err := jobs.New(jobs.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Fresh: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
func TestGovernedChatDownloadWaitsOutsideChatLock(t *testing.T) {
	m := chatJobs(t)
	n := p2ptest.NewNetwork()
	alex := newSide(t, n, "alex")
	bo := newSide(t, n, "bo", func(o *Options) { o.Jobs = m })
	id := pairDM(t, alex, bo)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})
	_, err := m.SubmitAudit(jobs.Identity{Caller: "owner", Owner: "owner", Key: "occupy"}, true, 0, func(context.Context) (io.ReadCloser, error) {
		close(entered)
		<-unblock
		return io.NopCloser(strings.NewReader("")), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	data := bytes.Repeat([]byte("verified content"), 10000)
	up, err := alex.svc.Upload(id, "fixture.txt", "", data)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := alex.svc.SendWith(owner, id, SendRequest{Files: []string{up.ID}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "queued governed download", func() bool { return len(m.List("owner", true)) == 2 })
	acquired := make(chan struct{})
	go func() { bo.svc.mu.Lock(); bo.svc.mu.Unlock(); close(acquired) }()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("hash wait held chat mutex")
	}
	close(unblock)
	eventually(t, "verified file placed", func() bool { return fileOf(t, bo, id, msg.ID).State == model.ChatFileReady })
	f := fileOf(t, bo, id, msg.ID)
	got, err := os.ReadFile(f.Path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("wrong bytes", err)
	}
	key := jobs.Identity{Caller: "chat-peer:" + alex.node.ID(), Owner: model.Owner, Key: "download/" + id + "/" + up.ID}
	r, err := m.Get(key.ID(), model.Owner, true)
	if err != nil || r.State != "completed" || r.Digest != up.SHA256 || r.Bytes != int64(len(data)) {
		t.Fatal(r, err)
	}
	if _, _, err := bo.svc.serveFile(p2p.FileRequest{PeerID: "stranger", ChatID: id, FileID: up.ID}); err == nil {
		t.Fatal("governed file served to stranger")
	}
}

func TestGovernedChatPoisonQuarantineStopsFetch(t *testing.T) {
	m := chatJobs(t)
	n := p2ptest.NewNetwork()
	alex := newSide(t, n, "alex")
	bo := newSide(t, n, "bo", func(o *Options) { o.Jobs = m })
	id := pairDM(t, alex, bo)
	var opens atomic.Int32
	alex.node.ServeFiles(func(p2p.FileRequest) (io.ReadCloser, int64, error) {
		opens.Add(1)
		return io.NopCloser(strings.NewReader("EVIL")), 4, nil
	})
	up, err := alex.svc.Upload(id, "a.txt", "", []byte("good"))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := alex.svc.SendWith(owner, id, SendRequest{Files: []string{up.ID}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "first damaged attempt", func() bool { f, _ := bo.st.GetChatFile(id, up.ID); return f.Tries > 0 })
	// ErrBusy during the previous attempt's ledger sync is a retryable admission
	// failure, not another poison read. Wait for quarantine instead of spending a
	// fixed retry budget while the disk is still busy.
	eventually(t, "poison job quarantined", func() bool {
		f, _ := bo.st.GetChatFile(id, up.ID)
		// Retry the governed admission directly: persistence contention must not
		// consume the chat layer's separate missing-file retry budget.
		ok, why := bo.svc.fetchGoverned(f, alex.node.ID())
		if ok || (why != "damaged" && why != "unreachable" && why != "quarantined") {
			t.Fatalf("unexpected poison result: ok=%v why=%q", ok, why)
		}
		return why == "quarantined"
	})
	f := fileOf(t, bo, id, msg.ID)
	// Admission may remain busy until the quarantine ledger sync finishes.
	eventually(t, "chat observes quarantine", func() bool {
		bo.svc.fetchOne(f)
		f = fileOf(t, bo, id, msg.ID)
		return f.State == model.ChatFileFailed
	})
	if f.State != model.ChatFileFailed || !strings.Contains(f.Error, "quarantined") {
		t.Fatal(f)
	}
	if opens.Load() != 3 {
		t.Fatal("unexpected attempts", opens.Load())
	}
	bo.svc.fetchOne(f)
	if opens.Load() != 3 {
		t.Fatal("quarantine reopened poison input")
	}
	if _, err := os.Stat(bo.svc.filePath(f)); !os.IsNotExist(err) {
		t.Fatal("unverified content placed", err)
	}
}

type waitReader struct {
	io.Reader
	gate <-chan struct{}
}

func (r *waitReader) Read(b []byte) (int, error) { <-r.gate; return r.Reader.Read(b) }
func TestGovernedChatRevocationBeforePlacement(t *testing.T) {
	m := chatJobs(t)
	n := p2ptest.NewNetwork()
	alex := newSide(t, n, "alex")
	bo := newSide(t, n, "bo", func(o *Options) { o.Jobs = m })
	id := pairDM(t, alex, bo)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})
	alex.node.ServeFiles(func(p2p.FileRequest) (io.ReadCloser, int64, error) {
		close(entered)
		return io.NopCloser(&waitReader{strings.NewReader("good"), unblock}), 4, nil
	})
	up, err := alex.svc.Upload(id, "a.txt", "", []byte("good"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = alex.svc.SendWith(owner, id, SendRequest{Files: []string{up.ID}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		f, _ := bo.st.GetChatFile(id, up.ID)
		t.Fatalf("download did not start within 5s (file state %q)", f.State)
	}
	if err = bo.svc.RemoveContact(alex.node.ID()); err != nil {
		t.Fatal(err)
	}
	close(unblock)
	eventually(t, "revoked attachment fails", func() bool { f, _ := bo.st.GetChatFile(id, up.ID); return f.State == model.ChatFileFailed })
	f, _ := bo.st.GetChatFile(id, up.ID)
	if _, err = os.Stat(bo.svc.filePath(f)); !os.IsNotExist(err) {
		t.Fatal("revoked content placed", err)
	}
	identity := jobs.Identity{Caller: "chat-peer:" + alex.node.ID(), Owner: model.Owner, Key: "download/" + id + "/" + up.ID}
	r, err := m.Get(identity.ID(), model.Owner, true)
	if err != nil || r.State != "revoked" {
		t.Fatal(r, err)
	}
}
