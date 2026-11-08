package transfers

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFolderPackAndReceive(t *testing.T) {
	sender, p := newTestService(t, nil)
	src := t.TempDir()
	file := filepath.Join(src, "note.txt")
	if err := os.WriteFile(file, []byte("folder payload\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1, 0)
	_ = os.Chtimes(file, old, old)
	secret := filepath.Join(src, "private")
	_ = os.Mkdir(secret, 0700)
	_ = os.WriteFile(filepath.Join(secret, "excluded.txt"), []byte("excluded"), 0600)
	sender.SecretFolders = []string{secret}
	dest := t.TempDir()
	r, err := sender.Start(context.Background(), Record{"src": src, "to": "source", "dest": dest}, "peer")
	if err != nil {
		t.Fatal(err)
	}
	r = waitState(t, sender, r.str("id"))
	if r.str("state") != "offered" {
		t.Fatal(r)
	}
	e, err := sender.Exports.Get(r.str("ticket"), "source")
	if err != nil {
		t.Fatal(err)
	}
	packed, err := os.ReadFile(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(packed), int64(len(packed)))
	if err != nil {
		t.Fatal(err)
	}
	hasTimes := false
	for _, f := range z.File {
		if f.Name == "private/excluded.txt" {
			t.Fatal("secret was packed")
		}
		if f.Name == ".tmx-mtimes.json" {
			hasTimes = true
		}
		if f.Method != zip.Store {
			t.Fatal("folder protocol requires stored ZIP entries")
		}
	}
	if !hasTimes {
		t.Fatal("pre-1980 timestamp sidecar missing")
	}
	receiver, _ := newTestService(t, packed)
	p.mu.Lock()
	offer := clone(p.offers[0])
	p.mu.Unlock()
	offer["from"] = "source"
	rec, err := receiver.Incoming(offer, "source")
	if err != nil {
		t.Fatal(err)
	}
	rec = waitState(t, receiver, rec.str("id"))
	if rec.str("state") != "placed" {
		t.Fatal(rec)
	}
	got, err := os.ReadFile(filepath.Join(rec.str("placed"), "note.txt"))
	if err != nil || string(got) != "folder payload\n" {
		t.Fatalf("folder content %q: %v", got, err)
	}
	st, err := os.Stat(filepath.Join(rec.str("placed"), "note.txt"))
	if err != nil || st.ModTime().Unix() != 1 {
		t.Fatalf("original mtime not restored: %v %v", st, err)
	}
	if err = sender.Exports.Done(e.Ticket, "source"); err != nil {
		t.Fatal(err)
	}
	if exists(e.Path) {
		t.Fatal("completed pack retained")
	}
}

func TestQuarantineBlocksKnownBadAndKeepsHold(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "hold", true: "known_bad"}[blocked], func(t *testing.T) {
			data := []byte("harmless fixture\n")
			s, _ := newTestService(t, data)
			s.HoldNeedsHuman = true
			b := offerFor(data, t.TempDir())
			b["name"] = "fixture.exe"
			if blocked {
				if err := os.WriteFile(filepath.Join(s.Antechamber.Root, "known_bad.txt"), []byte(b.str("sha256")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r, err := s.Incoming(b, "source")
			if err != nil {
				t.Fatal(err)
			}
			r = waitState(t, s, r.str("id"))
			want := "waiting_approval"
			if blocked {
				want = "refused"
			}
			if r.str("state") != want || r.str("item") == "" {
				t.Fatal(r)
			}
			if exists(filepath.Join(b.str("dest"), b.str("name"))) {
				t.Fatal("quarantined content placed")
			}
		})
	}
}

func TestResumeInspectionUsesExistingItem(t *testing.T) {
	data := []byte("resume inspect\n")
	s, _ := newTestService(t, data)
	r := offerFor(data, t.TempDir())
	r["id"], r["state"], r["dir"], r["created"] = "aabbccddeeff0011", "inspecting", "in", now()
	_ = os.WriteFile(s.part(r), data, 0600)
	if err := s.save(r); err != nil {
		t.Fatal(err)
	}
	s.Resume()
	r = waitState(t, s, r.str("id"))
	if r.str("state") != "placed" {
		t.Fatal(r)
	}
	// Simulate a crash between quarantine release and transfer status persistence.
	r["state"] = "inspecting"
	_ = s.save(r)
	s.Resume()
	r = waitState(t, s, r.str("id"))
	if r.str("state") != "placed" {
		t.Fatal(r)
	}
	entries, _ := os.ReadDir(r.str("dest"))
	if len(entries) != 1 {
		t.Fatalf("resume created a duplicate: %v", entries)
	}
}
