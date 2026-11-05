package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func TestUploadPath(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo := newSide(t, n, "alex"), newSide(t, n, "bo")
	id := pairDM(t, alex, bo)
	dir := t.TempDir()
	data := bytes.Repeat([]byte("\x1a\x45\xdf\xa3webm frames "), 40000) // about 640 KB
	src := filepath.Join(dir, "seg-0001.webm")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	up, err := alex.svc.UploadPath(id, "Demo (part 1 of 2).webm", "video/webm;codecs=vp8", src)
	sum := sha256.Sum256(data)
	if err != nil || up.Name != "Demo (part 1 of 2).webm" || up.Type != "video/webm" || up.Size != int64(len(data)) ||
		up.SHA256 != hex.EncodeToString(sum[:]) || up.State != model.ChatFileReady {
		t.Fatalf("upload: %+v %v", up, err)
	}
	if got, err := os.ReadFile(up.Path); err != nil || !bytes.Equal(got, data) || up.Path == src {
		t.Fatalf("copy: %v", err)
	}
	// Windows does not expose POSIX permission bits; still verify the file type.
	if st, err := os.Stat(up.Path); err != nil || !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("file: %v %v", st, err)
	}
	m, err := alex.svc.SendWith(owner, id, SendRequest{Body: "the demo", Files: []string{up.ID}})
	if err != nil || len(m.Files) != 1 {
		t.Fatalf("send: %+v %v", m, err)
	}
	eventually(t, "bo has the segment", func() bool { return fileOf(t, bo, id, m.ID).State == model.ChatFileReady })

	// the limits are Upload's
	big := filepath.Join(dir, "big.webm")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := alex.svc.UploadPath(id, "empty.webm", "", big); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("empty: %v", err)
	}
	if err := os.Truncate(big, MaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := alex.svc.UploadPath(id, "big.webm", "", big); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("too big: %v", err)
	}
	if err := os.Truncate(big, MaxFileBytes); err != nil { // exactly the limit is fine
		t.Fatal(err)
	}
	if f, err := alex.svc.UploadPath(id, "max.webm", "", big); err != nil || f.Size != MaxFileBytes {
		t.Fatalf("at the limit: %+v %v", f, err)
	}
	if _, err := alex.svc.UploadPath(id, "x.webm", "", filepath.Join(dir, "missing")); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := alex.svc.UploadPath(id, "dir", "", dir); fleet.KindOf(err) != fleet.KindInvalid {
		t.Fatalf("a folder: %v", err)
	}
	if _, err := alex.svc.UploadPath("nope", "x.webm", "", src); err == nil {
		t.Fatal("unknown chat accepted")
	}
}
