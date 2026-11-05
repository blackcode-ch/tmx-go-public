package fleet

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/fake"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// TestRealDatabase upgrades a copy of a real tmx.db (TMX_TEST_DB) and builds its board: the
// migration runs, old head history is filed, every card lands on a known thread. It logs
// counts only (the database holds someone's conversations) and starts no agent.
func TestRealDatabase(t *testing.T) {
	src := os.Getenv("TMX_TEST_DB")
	if src == "" {
		t.Skip("set TMX_TEST_DB to a copy of a tmx.db")
	}
	dir := t.TempDir()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	in.Close()
	out.Close()
	st, err := store.Open(filepath.Join(dir, "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v, _ := st.SchemaVersion(); v != 15 {
		t.Fatalf("schema %d after migrating", v)
	}
	f, err := New(Options{Elevated: func() bool { return false }, Store: st, Paths: config.Paths{Home: dir}, Executable: "/opt/tmx/tmx",
		Runners: map[model.Mode]runner.Runner{model.ModeChat: fake.New(), model.ModeTerminal: fake.New()}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Board()
	if err != nil {
		t.Fatal(err)
	}
	if b.HeadID == "" {
		t.Skip("this database has no head")
	}
	if has, _ := st.HasUnthreaded(b.HeadID); has {
		t.Fatal("head entries left unfiled")
	}
	known := map[string]bool{}
	for _, th := range b.Threads {
		known[th.ID] = true
	}
	per := map[string]int{}
	for _, c := range b.Cards {
		if !known[c.ThreadID] {
			t.Fatalf("card %d on unknown thread %q", c.ID, c.ThreadID)
		}
		per[c.ThreadID]++
	}
	for _, th := range b.Threads {
		t.Logf("%-8s %-30q archived=%-5v cards=%d", th.Kind, th.Title, th.Archived, per[th.ID])
	}
	t.Logf("%d cards in all", len(b.Cards))
}
