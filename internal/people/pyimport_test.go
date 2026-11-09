package people_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/people"
)

func digest(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = sha256.Sum256(b)
		}
		return nil
	})
	return out
}

// The fixture is fictional (testdata/py): the Python layout, with every record shape the reader folds.
func TestReadPythonFixture(t *testing.T) {
	before := digest(t, "testdata/py")
	plan, err := people.ReadPython("testdata/py")
	if err != nil {
		t.Fatal(err)
	}
	c := plan.Counts()
	want := people.Counts{Team: 2, External: 1, Clients: 1, PromisesOpen: 2, PromisesDone: 1, PromisesDropped: 1, Reminded: 1, Skipped: 1}
	if c != want {
		t.Fatalf("counts\n got %+v\nwant %+v", c, want)
	}
	byID := map[string]string{}
	for _, p := range plan.Promises {
		byID[p.ID] = p.Status + "|" + p.Title + "|" + p.Owner + "|" + p.Due
	}
	for id, w := range map[string]string{
		"py-ana-a1": "open|Mockups|lead|2026-10-15",
		"py-ana-a2": "done|Logo files?|lead|",
		"py-ana-a3": "dropped|Old ask|worker1|2099-01-01",
		"py-ana-a4": "open|Future|worker1|2099-06-01",
	} {
		if byID[id] != w {
			t.Errorf("%s: %q, want %q", id, byID[id], w)
		}
	}
	if plan.Reminded["py-ana-a1"] != 1760600000*1000 {
		t.Errorf("reminded: %v", plan.Reminded)
	}
	for _, p := range plan.People {
		if p.Key == "ana" && (p.Channels["email"] != "ana@example.test" || len(p.Aliases) != 1) {
			t.Errorf("ana: %+v", p)
		}
	}
	after := digest(t, "testdata/py")
	if len(after) != len(before) {
		t.Fatal("files changed")
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s was modified", k)
		}
	}
}

func TestReadPythonRefusesBadData(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "team", "threads"), 0o700)
	if p, err := people.ReadPython(dir); err != nil || len(p.People) != 0 {
		t.Fatal("missing team.json must be an empty plan", err)
	}
	os.WriteFile(filepath.Join(dir, "team", "team.json"), []byte(`{"team":[{"key":"x"},{"key":"x"}]}`), 0o600)
	if _, err := people.ReadPython(dir); err == nil {
		t.Fatal("duplicate key accepted")
	}
	os.WriteFile(filepath.Join(dir, "team", "team.json"), []byte(`{"team":[{"key":"x"}]}`), 0o600)
	os.WriteFile(filepath.Join(dir, "team", "threads", "x.jsonl"), []byte("{bad\n"), 0o600)
	if _, err := people.ReadPython(dir); err == nil {
		t.Fatal("corrupt thread accepted")
	}
}
