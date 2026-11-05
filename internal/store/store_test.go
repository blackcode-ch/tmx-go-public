package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	t.Setenv("TMXG_HOME", t.TempDir())
	s, err := Open(filepath.Join(t.TempDir(), "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmx.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.SchemaVersion()
	if err != nil || v != len(migrations) {
		t.Fatalf("version %d %v", v, err)
	}
	var mode string
	s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal_mode %q", mode)
	}
}

func TestAgentsCRUD(t *testing.T) {
	s := openTest(t)
	a := model.Agent{ID: "a1", Name: "head", Level: model.LevelHead, Harness: model.HarnessClaude,
		Mode: model.ModeChat, Model: "m", State: model.StateIdle, Desired: "running", CreatedAt: 1, UpdatedAt: 1}
	if err := s.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	a.CostUSD, a.ProjectID = 1.5, "p1"
	if err := s.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgent("a1")
	if err != nil || got != a {
		t.Fatalf("got %+v %v", got, err)
	}
	dup := a
	dup.ID = "a2"
	if err := s.PutAgent(dup); err == nil {
		t.Fatal("duplicate name accepted")
	}
	list, _ := s.ListAgents()
	if len(list) != 1 {
		t.Fatalf("list %d", len(list))
	}
	s.AppendTimeline(model.TimelineEntry{AgentID: "a1", Seq: 1, Kind: model.EntryUser, Text: "x"})
	if err := s.DeleteAgent("a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgent("a1"); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	if n, _ := s.LastSeq("a1"); n != 0 {
		t.Fatal("timeline not deleted")
	}
}

func TestProjects(t *testing.T) {
	s := openTest(t)
	p := model.Project{ID: "p1", Name: "Alpha", Slug: "alpha", Folder: "/x", Status: model.ProjectActive}
	if err := s.PutProject(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject("p1")
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("%+v %v", got, err)
	}
	s.DeleteProject("p1")
	if l, _ := s.ListProjects(); len(l) != 0 {
		t.Fatal("not deleted")
	}
}

func TestMessages(t *testing.T) {
	s := openTest(t)
	for i, id := range []string{"m1", "m2", "m3"} {
		s.PutMessage(model.Message{ID: id, From: "w1", To: "l1", Body: id, State: model.MsgQueued, CreatedAt: int64(100 + i)})
	}
	s.PutMessage(model.Message{ID: "m4", From: "owner", To: "w1", Body: "hi", State: model.MsgDelivered, CreatedAt: 200, ReplyTo: "m1"})
	if n, _ := s.CountQueued("l1"); n != 3 {
		t.Fatalf("queued %d", n)
	}
	if n, _ := s.CountSent("w1", "l1", 101); n != 2 {
		t.Fatalf("sent since %d", n)
	}
	q, _ := s.ListMessages(MessageFilter{To: "l1", State: model.MsgQueued})
	if len(q) != 3 || q[0].ID != "m1" || q[2].ID != "m3" {
		t.Fatalf("order %+v", q)
	}
	last2, _ := s.ListMessages(MessageFilter{Party: "w1", Limit: 2})
	if len(last2) != 2 || last2[0].ID != "m3" || last2[1].ID != "m4" || last2[1].ReplyTo != "m1" {
		t.Fatalf("party %+v", last2)
	}
	s.SetMessageState("m1", model.MsgDelivered, 300)
	m, _ := s.GetMessage("m1")
	if m.State != model.MsgDelivered || m.DeliveredAt != 300 {
		t.Fatalf("%+v", m)
	}
}

func TestApprovals(t *testing.T) {
	s := openTest(t)
	a := model.Approval{ID: "x1", AgentID: "a1", Kind: model.ApprovalPermission, Tool: "Bash",
		Input: json.RawMessage(`{"command":"ls"}`), State: model.ApprovalPending, CreatedAt: 1}
	if err := s.PutApproval(a); err != nil {
		t.Fatal(err)
	}
	q := model.Approval{ID: "x2", AgentID: "a1", Kind: model.ApprovalQuestion, Question: "?",
		Options: []string{"a", "b"}, State: model.ApprovalAnswered, CreatedAt: 2}
	s.PutApproval(q)
	pend, _ := s.ListApprovals(model.ApprovalPending)
	if len(pend) != 1 || string(pend[0].Input) != `{"command":"ls"}` || pend[0].Options == nil {
		t.Fatalf("%+v", pend)
	}
	got, _ := s.GetApproval("x2")
	if len(got.Options) != 2 {
		t.Fatalf("%+v", got)
	}
	all, _ := s.ListApprovals("")
	if len(all) != 2 {
		t.Fatal(len(all))
	}
}

func TestTimelineTrimAndPaging(t *testing.T) {
	s := openTest(t)
	const n = TimelineKeep + 250
	for i := int64(1); i <= n; i++ {
		e := model.TimelineEntry{AgentID: "a1", Seq: i, At: i, Kind: model.EntryAssistant, Text: "t"}
		if i == 7 {
			e.Meta = map[string]any{"from": "w1"}
		}
		if err := s.AppendTimeline(e); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM timeline WHERE agent_id='a1'`).Scan(&count)
	if count > TimelineKeep+trimEvery {
		t.Fatalf("kept %d", count)
	}
	last, _ := s.LastSeq("a1")
	if last != n {
		t.Fatal(last)
	}
	tail, _ := s.Timeline("a1", 0, 3)
	if len(tail) != 3 || tail[2].Seq != n || tail[0].Seq != n-2 {
		t.Fatalf("tail %+v", tail)
	}
	page, _ := s.Timeline("a1", n-10, 5)
	if len(page) != 5 || page[0].Seq != n-9 {
		t.Fatalf("page %+v", page)
	}
	if empty, _ := s.Timeline("nobody", 0, 0); empty == nil || len(empty) != 0 {
		t.Fatal("want empty non-nil slice")
	}
}

// '%', '?' and '#' in the path are part of the file name, not URI syntax.
func TestOpenPathWithURICharacters(t *testing.T) {
	name := "a#b?c%41d"
	if runtime.GOOS == "windows" {
		name = "a#bc%41d" // '?' cannot occur in a Windows filename.
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tmx.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the database is not at %s: %v", path, err)
	}
	if v, err := s.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("schema %d %v", v, err)
	}
}

// Exercise '?' escaping on every platform without creating an invalid Windows filename.
func TestEscapeURIPath(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/tmp/a#b?c%41d/tmx.db", "/tmp/a%23b%3Fc%2541d/tmx.db"},
		{"C:/data/a#b?c%41d/tmx.db", "C:/data/a%23b%3Fc%2541d/tmx.db"},
		{"/tmp/plain/tmx.db", "/tmp/plain/tmx.db"},
	} {
		if got := escapeURIPath(tc.path); got != tc.want {
			t.Errorf("escapeURIPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
