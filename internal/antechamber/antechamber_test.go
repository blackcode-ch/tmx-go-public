package antechamber

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Ported from tmx v2's tests/test_antechamber.py and test_antechamber_purge.py. Samples are built
// here from harmless pieces; no real malware. Attack-technique command lines live, encoded, in
// patterns_test.go (Linux, or Windows CI only).

// ------------------------------------------------------------------------------- helpers ---

type kv [2]string // archive entry: name, content

func verdict(t *testing.T, data []byte, name, trust string, folder bool, tweak ...func(*Limits)) ReportJSON {
	t.Helper()
	l := DefaultLimits()
	for _, f := range tweak {
		f(&l)
	}
	return NewInspector(trust, l, nil, "").InspectBytes(data, name, folder).JSON()
}

func codes(r ReportJSON, level string) map[string]bool {
	out := map[string]bool{}
	for _, x := range r.Reasons {
		if level == "" || x.Level == level {
			out[x.Code] = true
		}
	}
	return out
}

func mkzipMethod(t *testing.T, method uint16, files ...kv) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range files {
		fw, err := w.CreateHeader(&zip.FileHeader{Name: f[0], Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func mkzip(t *testing.T, files ...kv) []byte { return mkzipMethod(t, zip.Deflate, files...) }

type tarEntry struct {
	hdr  tar.Header
	data string
}

func tfile(name, data string) tarEntry {
	return tarEntry{tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}, data}
}

func mktar(t *testing.T, gz bool, entries ...tarEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	w := tar.NewWriter(&raw)
	for _, e := range entries {
		h := e.hdr
		if err := w.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !gz {
		return raw.Bytes()
	}
	return gzipBytes(t, raw.Bytes())
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zlibBytes(data []byte) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write(data)
	w.Close()
	return b.Bytes()
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func utf16le(s string) []byte { return u16(s) }

var (
	pe  = []byte("MZ" + strings.Repeat("\x90", 58) + "\x80\x00\x00\x00" + strings.Repeat("\x00", 64) + "PE\x00\x00" + strings.Repeat("\x00", 200))
	pdf = []byte("%PDF-1.7\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")
)

func zeros(n int) string { return strings.Repeat("\x00", n) }

func lim(f func(*Limits)) func(*Limits) { return f }

// ------------------------------------------------------------------------------ type, names ---

func TestSniffByContent(t *testing.T) {
	var all bytes.Buffer
	for i := 0; i < 4; i++ {
		for c := 0; c < 256; c++ {
			all.WriteByte(byte(c))
		}
	}
	utf16text := append([]byte{0xFF, 0xFE}, utf16le("\u00e9 \u00fcn\u00efcode\n")...)
	cases := []struct {
		data []byte
		kind string
	}{
		{pe, "windows-program"}, {[]byte("\x7fELF\x02\x01\x01" + zeros(50)), "linux-program"},
		{[]byte("\xcf\xfa\xed\xfe" + zeros(50)), "mac-program"}, {[]byte("\xca\xfe\xba\xbe\x00\x00\x00\x34"), "java-class"},
		{mkzip(t, kv{"a.txt", "x"}), "zip"}, {pdf, "pdf"}, {[]byte("\x89PNG\r\n\x1a\n" + zeros(20)), "png"},
		{[]byte("hello\nworld\n"), "text"}, {[]byte("<!DOCTYPE html><html></html>"), "html"}, {nil, "empty"},
		{all.Bytes(), "binary"},
		{[]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), "heic"}, {[]byte("\x00\x00\x00\x18ftypmif1\x00\x00\x00\x00"), "heic"},
		{[]byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00"), "avif"}, {[]byte("\x00\x00\x00\x14ftypqt  \x00\x00\x00\x00"), "mov"},
		{[]byte("\x00\x00\x00\x18ftypisom\x00\x00\x00\x00"), "mp4"}, {gzipBytes(t, []byte("abc")), "gzip"}, {utf16text, "text"},
		// MP3 without an ID3 tag starts at an MPEG frame (tmx-tech-h, 4 Oct)
		{append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 60)...), "mp3"},
		{append([]byte{0xFF, 0xF3, 0x48, 0xC4}, make([]byte, 60)...), "mp3"},
		{append([]byte{0xFF, 0xE3, 0x18, 0xC4}, make([]byte, 60)...), "mp3"},
		{append([]byte{0xFF, 0xFF, 0xFF, 0xFF}, all.Bytes()[:512]...), "binary"},
	}
	for i, c := range cases {
		if got := Sniff(c.data, nil); got != c.kind {
			t.Errorf("case %d: Sniff = %q, want %q", i, got, c.kind)
		}
	}
}

func TestNames(t *testing.T) {
	name, f := CleanName("invoice\u202efdp.exe")
	rtlo := false
	for _, x := range f {
		rtlo = rtlo || (x.Code == "rtlo" && x.Levels[1] == Blocked)
	}
	if strings.ContainsRune(name, '\u202e') || !rtlo {
		t.Errorf("rtlo: %q %v", name, f)
	}
	for in, want := range map[string]string{"../../etc/passwd": "passwd", "C:\\Windows\\evil.txt:ads": "evil.txtads",
		"CON.txt": "_CON.txt", "trail. . ": "trail"} {
		if got, _ := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
	hasDouble := func(n string) bool {
		_, fs := CleanName(n)
		for _, x := range fs {
			if x.Code == "double-ext" {
				return true
			}
		}
		return false
	}
	if !hasDouble("report.pdf.exe") || !hasDouble("photo.jpg      .scr") {
		t.Error("double extension not found")
	}
	if _, fs := CleanName("archive.tar.gz"); len(fs) != 0 {
		t.Errorf("archive.tar.gz: %v", fs)
	}
	if got := pyRepr("it's"); got != `"it's"` {
		t.Errorf("pyRepr = %s", got)
	}
}

func TestRTLONameIsBlocked(t *testing.T) {
	if r := verdict(t, []byte("hi"), "cv\u202etxt.exe", Person, false); r.Verdict != Blocked {
		t.Fatal(r)
	}
}

func TestExtensionMismatchAndPrograms(t *testing.T) {
	r := verdict(t, pe, "notes.txt", Own, false)
	if c := codes(r, ""); r.Verdict != NeedsHuman || !c["program"] || !c["ext-mismatch"] {
		t.Errorf("%+v", r)
	}
	if r := verdict(t, []byte("[InternetShortcut]\nURL=file://x\n"), "click.url", Own, false); r.Verdict != NeedsHuman {
		t.Errorf("url: %+v", r)
	}
	if r := verdict(t, mkzip(t, kv{"a", "b"}), "doc.pdf", Own, false); r.Verdict != NeedsHuman {
		t.Errorf("zip as pdf: %+v", r)
	}
}

func TestPlainContentIsCleanForAnyone(t *testing.T) {
	for _, c := range []struct {
		data []byte
		name string
	}{
		{[]byte("meeting notes\n"), "notes.txt"}, {pdf, "a.pdf"}, {[]byte("\x89PNG\r\n\x1a\n" + zeros(9)), "p.png"},
		{mkzip(t, kv{"[Content_Types].xml", "<x/>"}, kv{"word/document.xml", "<w/>"}), "letter.docx"},
	} {
		if r := verdict(t, c.data, c.name, Person, false); r.Verdict != Clean {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}

func TestScriptsTrustLevels(t *testing.T) {
	script := []byte("import os\nprint(os.getcwd())\n")
	odd := []byte(strings.Repeat("just some unknown bytes \x00\x01\x02", 10))
	for _, c := range []struct {
		data        []byte
		name, trust string
		want        string
	}{
		{script, "tool.py", Own, Clean}, {script, "tool.py", Person, NeedsHuman},
		{[]byte("#!/bin/sh\necho hi\n"), "run", Person, NeedsHuman},
		{[]byte("<html><body onload=x()>"), "page.html", Own, Clean},
		{[]byte("<html><script>x()</script>"), "page.html", Person, NeedsHuman},
		{odd, "data.bin.x", Person, NeedsHuman},
	} {
		if r := verdict(t, c.data, c.name, c.trust, false); r.Verdict != c.want {
			t.Errorf("%s/%s: %s, want %s (%v)", c.name, c.trust, r.Verdict, c.want, r.Reasons)
		}
	}
}

// ---------------------------------------------------------------------------- active content ---

func TestOfficeMacrosAndEmbeddings(t *testing.T) {
	macro := mkzip(t, kv{"[Content_Types].xml", "<x/>"}, kv{"word/document.xml", "<w/>"}, kv{"word/vbaProject.bin", zeros(10)})
	if !codes(verdict(t, macro, "a.docm", Own, false), NeedsHuman)["active-content"] {
		t.Error("macro docm")
	}
	ole := append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"+zeros(100)), append(utf16le("WordDocument"), make([]byte, 20)...)...)
	if r := verdict(t, ole, "old.doc", Person, false); r.Verdict != Clean {
		t.Errorf("plain ole: %+v", r)
	}
	if !codes(verdict(t, append(append([]byte{}, ole...), utf16le("_VBA_PROJECT")...), "old.doc", Person, false), "")["active-content"] {
		t.Error("ole macro")
	}
	if !codes(verdict(t, []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"+zeros(100)), "setup.msi", Person, false), "")["program"] {
		t.Error("msi")
	}
	rtf := []byte(`{\rtf1 {\object\objemb {\*\objdata 0105000002000000}}}`)
	if r := verdict(t, rtf, "a.rtf", Person, false); r.Verdict != NeedsHuman {
		t.Errorf("rtf: %+v", r)
	}
}

func TestPDFActiveContentEvenCompressedOrEscaped(t *testing.T) {
	js := bytes.Replace(pdf, []byte("/Type /Catalog"), []byte("/Type /Catalog /OpenAction << /S /JavaScript /JS (app.alert(1)) >>"), 1)
	if r := verdict(t, js, "a.pdf", Person, false); r.Verdict != NeedsHuman {
		t.Errorf("js: %+v", r)
	}
	esc := bytes.Replace(pdf, []byte("/Catalog"), []byte("/Catalog /Names << /J#61vaScript 3 0 R >>"), 1)
	if r := verdict(t, esc, "a.pdf", Person, false); r.Verdict != NeedsHuman {
		t.Errorf("escaped: %+v", r)
	}
	hidden := zlibBytes([]byte("<< /S /Launch /F (cmd.exe) >>"))
	p := append(append(append(append([]byte{}, pdf...), "4 0 obj << /Type /ObjStm /Filter /FlateDecode >> stream\n"...), hidden...), "\nendstream endobj\n"...)
	if !codes(verdict(t, p, "a.pdf", Person, false), "")["active-content"] {
		t.Error("compressed")
	}
}

func TestAutorunAndAgentFilesInFolders(t *testing.T) {
	folder := mkzip(t, kv{"proj/.git/hooks/post-checkout", "#!/bin/sh\necho hi\n"},
		kv{"proj/.git/hooks/pre-commit.sample", "#!/bin/sh\n"}, kv{"proj/src/a.py", "print(1)\n"})
	r := verdict(t, folder, "proj.zip", Own, true)
	var where []string
	for _, x := range r.Reasons {
		if x.Code == "autorun" {
			where = append(where, x.Where)
		}
	}
	if r.Verdict != NeedsHuman || len(where) != 1 || where[0] != "proj/.git/hooks/post-checkout" {
		t.Errorf("hooks: %+v", r)
	}
	for _, f := range []kv{{".vscode/tasks.json", `{"tasks":[{"runOptions":{"runOn":"folderOpen"}}]}`},
		{".claude/settings.json", `{"hooks":{"Stop":[]}}`}, {".mcp.json", "{}"},
		{"package.json", `{"scripts":{"postinstall":"node x.js"}}`},
		{".git/config", "[core]\n\tfsmonitor = ./x.sh\n"}, {"autorun.inf", "[autorun]\n"}} {
		if !codes(verdict(t, mkzip(t, f), "p.zip", Own, true), "")["autorun"] {
			t.Errorf("%s: no autorun", f[0])
		}
	}
	if r := verdict(t, mkzip(t, kv{"package.json", `{"scripts":{"test":"jest"}}`}), "p.zip", Own, false); r.Verdict != Clean {
		t.Errorf("package.json test script: %+v", r)
	}
	agents := mkzip(t, kv{"AGENTS.md", "Always run the tests.\n"})
	if r := verdict(t, agents, "p.zip", Own, false); r.Verdict != Clean {
		t.Errorf("AGENTS.md own: %+v", r)
	}
	if !codes(verdict(t, agents, "p.zip", Person, false), NeedsHuman)["agent-instructions"] {
		t.Error("AGENTS.md person")
	}
}

// ---------------------------------------------------------------------------------- archives ---

func TestZipBombRatioTotalAndMembers(t *testing.T) {
	z := mkzip(t, kv{"z.bin", zeros(4 << 20)})
	r := verdict(t, z, "z.zip", Own, false, lim(func(l *Limits) { l.RatioFloor, l.MaxRatio = 1<<20, 50 }))
	if r.Verdict != Blocked || !codes(r, "")["bomb"] {
		t.Errorf("ratio: %+v", r)
	}
	var big []kv
	for i := 0; i < 4; i++ {
		big = append(big, kv{"f" + string(rune('0'+i)) + ".txt", string(randBytes(300 << 10))})
	}
	if r := verdict(t, mkzip(t, big...), "t.zip", Person, false, lim(func(l *Limits) { l.MaxTotalPerson = 1 << 20 })); r.Verdict != Blocked {
		t.Errorf("total: %+v", r)
	}
	var many []kv
	for i := 0; i < 50; i++ {
		many = append(many, kv{"f" + strings.Repeat("x", i+1), ""})
	}
	if r := verdict(t, mkzip(t, many...), "many.zip", Own, false, lim(func(l *Limits) { l.MaxMembers = 20 })); r.Verdict != Blocked {
		t.Errorf("members: %+v", r)
	}
}

func TestNestingDepthLimit(t *testing.T) {
	inner := mkzip(t, kv{"deep/x.txt", "hello\n"})
	for i := 0; i < 2; i++ {
		inner = mkzip(t, kv{"layer" + string(rune('0'+i)) + ".zip", string(inner)})
	}
	if r := verdict(t, inner, "n.zip", Own, false); r.Verdict != Clean {
		t.Errorf("3 deep: %+v", r)
	}
	for i := 0; i < 5; i++ {
		inner = mkzip(t, kv{"more" + string(rune('0'+i)) + ".zip", string(inner)})
	}
	if r := verdict(t, inner, "n.zip", Own, false); r.Verdict != Blocked {
		t.Errorf("8 deep: %+v", r)
	}
}

func TestTraversalAndLinks(t *testing.T) {
	if r := verdict(t, mkzip(t, kv{"../evil.txt", "x"}), "t.zip", Own, false); r.Verdict != Blocked {
		t.Errorf("..: %+v", r)
	}
	if r := verdict(t, mkzip(t, kv{"/etc/cron.d/x", "x"}), "t.zip", Own, false); r.Verdict != Blocked {
		t.Errorf("absolute: %+v", r)
	}
	out := tarEntry{hdr: tar.Header{Name: "proj/link", Typeflag: tar.TypeSymlink, Linkname: "../../.ssh/authorized_keys", Mode: 0o777}}
	if r := verdict(t, mktar(t, true, tfile("proj/a.txt", "hi"), out), "p.tar.gz", Own, false); r.Verdict != Blocked {
		t.Errorf("escaping link: %+v", r)
	}
	inside := tarEntry{hdr: tar.Header{Name: "proj/link", Typeflag: tar.TypeSymlink, Linkname: "a.txt", Mode: 0o777}}
	if r := verdict(t, mktar(t, true, tfile("proj/a.txt", "hi"), inside), "p.tar.gz", Own, false); r.Verdict != Clean {
		t.Errorf("inner link own: %+v", r)
	}
	if r := verdict(t, mktar(t, true, tfile("proj/a.txt", "hi"), inside), "p.tar.gz", Person, false); r.Verdict != NeedsHuman {
		t.Errorf("inner link person: %+v", r)
	}
	dev := tarEntry{hdr: tar.Header{Name: "dev/sda", Typeflag: tar.TypeBlock, Mode: 0o600}}
	if r := verdict(t, mktar(t, true, dev), "d.tar", Own, false); r.Verdict != Blocked {
		t.Errorf("device: %+v", r)
	}
	// a ZIP symlink (Unix creator, S_IFLNK) pointing outside
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "proj/l", Method: zip.Store}
	h.SetMode(os.ModeSymlink | 0o777)
	fw, _ := w.CreateHeader(h)
	fw.Write([]byte("../../x"))
	w.Close()
	if r := verdict(t, b.Bytes(), "l.zip", Own, false); r.Verdict != Blocked || !codes(r, "")["traversal"] {
		t.Errorf("zip link: %+v", r)
	}
}

func TestCompressedSingleFilesAndTarGz(t *testing.T) {
	if r := verdict(t, gzipBytes(t, []byte("plain log\n")), "log.txt.gz", Person, false); r.Verdict != Clean {
		t.Errorf("log: %+v", r)
	}
	if r := verdict(t, gzipBytes(t, pe), "x.gz", Own, false); r.Verdict != NeedsHuman {
		t.Errorf("pe.gz: %+v", r)
	}
	// Python uses .tar.xz here; Go has no xz reader in the standard library, so the tar bomb is gzip.
	r := verdict(t, mktar(t, true, tfile("a.txt", zeros(3<<20))), "b.tar.gz", Own, false,
		lim(func(l *Limits) { l.RatioFloor, l.MaxRatio = 1<<20, 50 }))
	if r.Verdict != Blocked {
		t.Errorf("tar bomb: %+v", r)
	}
	if r := verdict(t, []byte("\xfd7zXZ\x00"+zeros(30)), "b.tar.xz", Own, false); r.Verdict != NeedsHuman || !codes(r, "")["uninspectable"] {
		t.Errorf("xz: %+v", r)
	}
}

func TestEncryptedAndUninspectable(t *testing.T) {
	raw := mkzipMethod(t, zip.Store, kv{"secret.txt", "x"})
	raw[6] |= 1 // local header: encrypted flag
	i := bytes.Index(raw, []byte("PK\x01\x02"))
	raw[i+8] |= 1 // central directory: encrypted flag
	if !codes(verdict(t, raw, "s.zip", Own, false), "")["encrypted"] {
		t.Error("encrypted")
	}
	if r := verdict(t, []byte("Rar!\x1a\x07\x01\x00"+zeros(30)), "x.rar", Own, false); r.Verdict != NeedsHuman {
		t.Errorf("rar: %+v", r)
	}
	if r := verdict(t, []byte("7z\xbc\xaf\x27\x1c"+zeros(30)), "x.7z", Own, false); r.Verdict != NeedsHuman {
		t.Errorf("7z: %+v", r)
	}
}

func TestDamagedZipNeedsHumanNotCrash(t *testing.T) {
	if r := verdict(t, []byte("PK\x03\x04"+strings.Repeat("garbage", 20)), "x.zip", Own, false); r.Verdict != NeedsHuman {
		t.Errorf("%+v", r)
	}
}

func TestKnownBad(t *testing.T) {
	data := []byte("anything\n")
	d := sha256Of(bytes.NewReader(data), int64(len(data)))
	r := NewInspector(Own, DefaultLimits(), map[string]bool{d: true}, "").InspectBytes(data, "a.txt", false).JSON()
	if r.Verdict != Blocked || !codes(r, "")["known-bad"] {
		t.Errorf("%+v", r)
	}
}

// ----------------------------------------------------------------------------- the quarantine ---

var (
	ownOrigin    = Origin{Kind: "transfer", Trust: Own, From: "hostc-claude@hostc"}
	personOrigin = Origin{Kind: "person", Trust: Person, From: "Clara"}
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newChamber(t *testing.T) (*Antechamber, *clock) {
	t.Helper()
	a, err := New(filepath.Join(t.TempDir(), "ac"), "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Isolate = false
	a.AV = NoAV{}
	zero := int64(0)
	a.Limits.MinFree = &zero
	c := &clock{time.Unix(1_800_000_000, 0)}
	a.Now = c.now
	return a, c
}

func admit(t *testing.T, a *Antechamber, data []byte, name string, o Origin, folder bool) Item {
	t.Helper()
	it, err := a.AdmitBytes(data, name, o, folder)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAdmitReleaseNeverOverwrites(t *testing.T) {
	a, _ := newChamber(t)
	dest := filepath.Join(t.TempDir(), "dest")
	os.Mkdir(dest, 0o755)
	it := admit(t, a, []byte("hello\n"), "notes.txt", ownOrigin, false)
	if it.Verdict != Clean || it.State != StatePending || it.Kind != "text" {
		t.Fatalf("%+v", it)
	}
	blob := filepath.Join(a.Root, it.ID, "blob")
	if !lexists(blob) || lexists(blob+".txt") {
		t.Fatal("blob")
	}
	p1, err := a.Release(it.ID, dest, ReleaseOptions{By: "test"})
	if err != nil || p1 != filepath.Join(dest, "notes.txt") || readFile(t, p1) != "hello\n" {
		t.Fatalf("release: %q %v", p1, err)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(readFile(t, p1+":Zone.Identifier"), "ZoneId=3") {
			t.Error("no Mark of the Web")
		}
	} else if st, _ := os.Stat(p1); st.Mode()&0o111 != 0 {
		t.Error("execute bits")
	}
	if _, err := a.Release(it.ID, dest, ReleaseOptions{}); err == nil {
		t.Error("released twice")
	}
	it2 := admit(t, a, []byte("other\n"), "notes.txt", ownOrigin, false)
	p2, err := a.Release(it2.ID, dest, ReleaseOptions{})
	if err != nil || filepath.Base(p2) != "notes (2).txt" || readFile(t, p1) != "hello\n" {
		t.Fatalf("second: %q %v", p2, err)
	}
	log := readFile(t, a.LogPath)
	if !strings.Contains(log, `"released"`) || strings.Contains(log, "hello") {
		t.Errorf("audit log: %s", log)
	}
}

func TestNeedsHumanWaitsForApproval(t *testing.T) {
	a, _ := newChamber(t)
	dest := t.TempDir()
	it := admit(t, a, []byte("print(1)\n"), "tool.py", personOrigin, false)
	if it.Verdict != NeedsHuman {
		t.Fatal(it)
	}
	if _, err := a.Release(it.ID, dest, ReleaseOptions{}); status(err) != 409 {
		t.Fatalf("release before approval: %v", err)
	}
	if _, err := a.Approve(it.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	p, err := a.Release(it.ID, dest, ReleaseOptions{})
	if err != nil || readFile(t, p) != "print(1)\n" {
		t.Fatal(p, err)
	}
}

func TestBlockedNeverLeavesAndRefuseBlocksHash(t *testing.T) {
	a, _ := newChamber(t)
	it := admit(t, a, mkzip(t, kv{"../x", "x"}), "a.zip", personOrigin, false)
	if it.Verdict != Blocked {
		t.Fatal(it)
	}
	if _, err := a.Approve(it.ID, "operator"); err == nil {
		t.Error("a blocked item was approved")
	}
	if _, err := a.Release(it.ID, t.TempDir(), ReleaseOptions{}); status(err) != 403 {
		t.Errorf("release blocked: %v", err)
	}
	odd := []byte(strings.Repeat("\x00\x01odd binary\x02", 20))
	o := admit(t, a, odd, "thing.dat", personOrigin, false)
	if _, err := a.Refuse(o.ID, "operator", true); err != nil {
		t.Fatal(err)
	}
	again := admit(t, a, odd, "renamed.dat", personOrigin, false)
	if again.Verdict != Blocked || !codes(ReportJSON{Reasons: again.Reasons}, "")["known-bad"] {
		t.Errorf("%+v", again)
	}
}

func TestAdmitLimits(t *testing.T) {
	a, _ := newChamber(t)
	if _, err := a.AdmitBytes(make([]byte, 26<<20), "big", personOrigin, false); status(err) != 413 {
		t.Errorf("over the person limit: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(a.Root, "uploads")); len(ents) != 0 {
		t.Errorf("uploads left: %v", ents)
	}
}

func TestQuarantineFullRefusesNewItems(t *testing.T) {
	a, _ := newChamber(t)
	a.Limits.QuarantineMax = 1000
	admit(t, a, bytes.Repeat([]byte("x"), 600), "a.txt", ownOrigin, false)
	if _, err := a.AdmitBytes(bytes.Repeat([]byte("y"), 600), "b.txt", ownOrigin, false); status(err) != 507 {
		t.Fatalf("want 507, got %v", err)
	}
	if n := len(a.List("")); n != 1 {
		t.Errorf("%d items", n)
	}
	// a decided item no longer counts against the quota
	it := a.List("")[0]
	if _, err := a.Refuse(it.ID, "operator", false); err != nil {
		t.Fatal(err)
	}
	if q := a.QuotaUsage(); q != 0 {
		t.Errorf("quota after refuse = %d", q)
	}
	if a.Usage() < 600 {
		t.Errorf("usage = %d", a.Usage())
	}
	admit(t, a, bytes.Repeat([]byte("y"), 600), "b.txt", ownOrigin, false)
}

func TestFolderReleaseExtractsSafely(t *testing.T) {
	a, _ := newChamber(t)
	dest := t.TempDir()
	z := mkzip(t, kv{"proj/", ""}, kv{"proj/src/a.py", "print(1)\n"}, kv{"proj/README.md", "# hi\n"}, kv{"proj/CON.txt", "x"})
	it := admit(t, a, z, "proj.zip", ownOrigin, true)
	if it.Verdict != Clean {
		t.Fatalf("%+v", it)
	}
	out, err := a.Release(it.ID, dest, ReleaseOptions{})
	if err != nil || out != filepath.Join(dest, "proj") || readFile(t, filepath.Join(out, "proj", "src", "a.py")) != "print(1)\n" {
		t.Fatal(out, err)
	}
	if !lexists(filepath.Join(out, "proj", "_CON.txt")) {
		t.Error("CON.txt not renamed")
	}
	it = admit(t, a, z, "proj.zip", ownOrigin, true)
	if out, err := a.Release(it.ID, dest, ReleaseOptions{}); err != nil || out != filepath.Join(dest, "proj (2)") {
		t.Error(out, err)
	}
}

// The sender picks the name: "..zip" or "...zip" must never name the destination or its parent
// once ".zip" is stripped, and with ReplaceInto never move that folder away (o5, #256).
func TestFolderReleaseNameCannotEscape(t *testing.T) {
	z := mkzip(t, kv{"a.txt", "x"})
	for _, name := range []string{"..zip", "...zip", ".zip"} {
		for _, replace := range []bool{false, true} {
			a, _ := newChamber(t)
			parent := t.TempDir()
			dest := filepath.Join(parent, "Documents")
			if err := os.MkdirAll(dest, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dest, "keep.txt")
			_ = os.WriteFile(marker, []byte("mine"), 0o600)
			it := admit(t, a, z, name, ownOrigin, true)
			if it.Verdict == Blocked {
				continue // refused before any placing: fine
			}
			if it.Verdict != Clean {
				if _, err := a.Approve(it.ID, "test"); err != nil {
					t.Fatalf("%s: approve: %v", name, err)
				}
			}
			o := ReleaseOptions{}
			if replace {
				o.ReplaceInto = t.TempDir()
			}
			out, err := a.Release(it.ID, dest, o)
			if err == nil && filepath.Dir(out) != filepath.Clean(dest) {
				t.Errorf("%s (replace %v): placed at %s, not inside %s", name, replace, out, dest)
			}
			if readFile(t, marker) != "mine" {
				t.Errorf("%s (replace %v): the destination folder was moved or replaced", name, replace)
			}
			if !lexists(dest) || !lexists(parent) {
				t.Errorf("%s (replace %v): the destination or its parent is gone", name, replace)
			}
		}
	}
}

func TestReservedNames(t *testing.T) {
	for in, want := range map[string]string{"CON .txt": "_CON .txt", "COM\u00b9.txt": "_COM\u00b9.txt", "CONIN$": "_CONIN$", "notes.txt": "notes.txt"} {
		if got, _ := CleanName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestPurgeTimings(t *testing.T) {
	a, c := newChamber(t)
	keep := admit(t, a, []byte("a\n"), "a.txt", ownOrigin, false)
	old := admit(t, a, []byte("b\n"), "b.txt", ownOrigin, false)
	if _, err := a.Refuse(old.ID, "operator", false); err != nil {
		t.Fatal(err)
	}
	gone := admit(t, a, []byte("c\n"), "c.txt", ownOrigin, false)
	if _, err := a.Release(gone.ID, t.TempDir(), ReleaseOptions{}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(23 * time.Hour)
	if n := a.Purge(); n != 0 {
		t.Errorf("purged %d within a day", n)
	}
	c.t = c.t.Add(2 * time.Hour) // released 25 h ago: its blob goes
	a.Purge()
	if lexists(filepath.Join(a.Root, gone.ID)) {
		t.Error("released item kept after 24 h")
	}
	if _, err := a.Get(keep.ID); err != nil || !lexists(filepath.Join(a.Root, old.ID)) {
		t.Error("removed too early")
	}
	c.t = c.t.Add(7 * 24 * time.Hour) // 8 days after
	a.Purge()
	if lexists(filepath.Join(a.Root, old.ID)) {
		t.Error("refused item kept after 7 days")
	}
	it, err := a.Get(keep.ID)
	if err != nil || it.State != StateExpired || !lexists(filepath.Join(a.Root, keep.ID, "blob")) {
		t.Errorf("waiting item: %+v %v", it, err)
	}
	c.t = c.t.Add(8 * 24 * time.Hour) // expired 8 days ago: deleted
	a.Purge()
	if lexists(filepath.Join(a.Root, keep.ID)) {
		t.Error("expired item kept")
	}
}

func TestPurgeDropsIdleUploads(t *testing.T) {
	a, c := newChamber(t)
	up := filepath.Join(a.Root, "uploads", strings.Repeat("a", 32))
	os.WriteFile(up+".part", []byte("x"), 0o600)
	meta, _ := json.Marshal(map[string]any{"size": 10, "touched": a.now()})
	os.WriteFile(up+".json", meta, 0o600)
	if q := a.QuotaUsage(); q != 10 {
		t.Errorf("an upload reserves its announced size: %d", q)
	}
	c.t = c.t.Add(2 * time.Hour)
	if n := a.Purge(); n != 1 || lexists(up+".part") {
		t.Errorf("idle upload: %d", n)
	}
}

func TestDiscardARefusedCopyNow(t *testing.T) {
	a, _ := newChamber(t)
	it := admit(t, a, pe, "tool.exe", personOrigin, false)
	if _, err := a.Discard(it.ID, "owner"); status(err) != 409 { // pending: never
		t.Fatalf("discard pending: %v", err)
	}
	if _, err := a.Refuse(it.ID, "owner", false); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Discard(it.ID, "owner"); err != nil || got.ID != it.ID {
		t.Fatal(got, err)
	}
	if _, err := a.Discard(it.ID, "owner"); status(err) != 404 { // gone
		t.Errorf("discard twice: %v", err)
	}
	if lexists(filepath.Join(a.Root, it.ID)) {
		t.Error("the copy is still there")
	}
}

func TestSafeExtractStandalone(t *testing.T) {
	dir := t.TempDir()
	l := DefaultLimits()
	zero := int64(0)
	l.MinFree = &zero
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o600)
		return p
	}
	good := write("good.zip", mkzip(t, kv{"a/b.txt", "hi"}, kv{"a/c/", ""}))
	out, err := SafeExtract(good, filepath.Join(dir, "out"), l, "", Own)
	if err != nil || readFile(t, filepath.Join(out, "a", "b.txt")) != "hi" {
		t.Fatal(out, err)
	}
	if out, _ := SafeExtract(good, filepath.Join(dir, "out"), l, "", Own); out != filepath.Join(dir, "out (2)") {
		t.Errorf("never into an existing folder: %s", out)
	}
	trav := write("trav.zip", mkzip(t, kv{"ok.txt", "x"}, kv{"../escape.txt", "x"}))
	if _, err := SafeExtract(trav, filepath.Join(dir, "t"), l, "", Own); status(err) != 403 || lexists(filepath.Join(dir, "t")) || lexists(filepath.Join(dir, "escape.txt")) {
		t.Errorf("traversal: %v", err)
	}
	bombZip := write("bomb.zip", mkzip(t, kv{"z.bin", zeros(4 << 20)}))
	lb := l
	lb.RatioFloor, lb.MaxRatio = 1<<20, 50
	if _, err := SafeExtract(bombZip, filepath.Join(dir, "b"), lb, "", Own); status(err) != 403 || lexists(filepath.Join(dir, "b")) {
		t.Errorf("bomb: %v", err)
	}
	bad := write("bad.zip", []byte("PK\x03\x04 not really"))
	if _, err := SafeExtract(bad, filepath.Join(dir, "d"), l, "", Own); status(err) != 422 || lexists(filepath.Join(dir, "d")) {
		t.Errorf("damaged: %v", err)
	}
	enc := mkzipMethod(t, zip.Store, kv{"s.txt", "x"})
	i := bytes.Index(enc, []byte("PK\x01\x02"))
	enc[6] |= 1
	enc[i+8] |= 1
	if _, err := SafeExtract(write("enc.zip", enc), filepath.Join(dir, "e"), l, "", Own); status(err) != 422 {
		t.Errorf("encrypted: %v", err)
	}
}

func TestSafeExtractRestoresOldMtimes(t *testing.T) {
	dir := t.TempDir()
	l := DefaultLimits()
	zero := int64(0)
	l.MinFree = &zero
	z := mkzip(t, kv{"old.txt", "x"}, kv{"new.txt", "y"}, kv{MtimesName, `{"old.txt": 86400, "new.txt": 400000000, "missing.txt": 5}`})
	p := filepath.Join(dir, "m.zip")
	os.WriteFile(p, z, 0o600)
	out, err := SafeExtract(p, filepath.Join(dir, "out"), l, "", Own)
	if err != nil {
		t.Fatal(err)
	}
	if lexists(filepath.Join(out, MtimesName)) {
		t.Error("the sidecar was extracted")
	}
	st, _ := os.Stat(filepath.Join(out, "old.txt"))
	if st.ModTime().Unix() != 86400 {
		t.Errorf("old.txt mtime %v", st.ModTime())
	}
	st, _ = os.Stat(filepath.Join(out, "new.txt"))
	if st.ModTime().Unix() == 400000000 {
		t.Error("a time ZIP can hold was overridden")
	}
}

func TestReleaseWithNameAndReplaceIntoBackup(t *testing.T) {
	a, _ := newChamber(t)
	root := t.TempDir()
	dest, backup := filepath.Join(root, "dest"), filepath.Join(root, "bk")
	os.Mkdir(dest, 0o755)
	os.Mkdir(backup, 0o755)
	os.WriteFile(filepath.Join(dest, "report.txt"), []byte("old"), 0o644)
	it := admit(t, a, []byte("new"), "whatever.txt", ownOrigin, false)
	placed, err := a.Release(it.ID, dest, ReleaseOptions{Name: "report.txt", ReplaceInto: backup})
	if err != nil || placed != filepath.Join(dest, "report.txt") || readFile(t, placed) != "new" {
		t.Fatal(placed, err)
	}
	if readFile(t, filepath.Join(backup, "report.txt")) != "old" { // kept, not deleted
		t.Error("old copy")
	}
	if got, _ := a.Get(it.ID); got.ReplacedTo != filepath.Join(backup, "report.txt") {
		t.Errorf("replaced_to = %q", got.ReplacedTo)
	}
	it = admit(t, a, []byte("newer"), "x.txt", ownOrigin, false)
	a.Release(it.ID, dest, ReleaseOptions{Name: "report.txt", ReplaceInto: backup})
	if readFile(t, filepath.Join(backup, "report (2).txt")) != "new" || readFile(t, filepath.Join(dest, "report.txt")) != "newer" {
		t.Error("second replace")
	}
	it = admit(t, a, []byte("third"), "x.txt", ownOrigin, false)
	if p, _ := a.Release(it.ID, dest, ReleaseOptions{Name: "../report.txt"}); p != filepath.Join(dest, "report (2).txt") {
		t.Errorf("no replace: never overwrite: %s", p)
	}
	os.Mkdir(filepath.Join(dest, "proj"), 0o755)
	os.WriteFile(filepath.Join(dest, "proj", "keep.txt"), []byte("mine"), 0o644)
	it = admit(t, a, mkzip(t, kv{"a.txt", "A"}), "proj.zip", ownOrigin, true)
	out, err := a.Release(it.ID, dest, ReleaseOptions{ReplaceInto: backup})
	if err != nil || out != filepath.Join(dest, "proj") || readFile(t, filepath.Join(out, "a.txt")) != "A" {
		t.Fatal(out, err)
	}
	if readFile(t, filepath.Join(backup, "proj", "keep.txt")) != "mine" {
		t.Error("old folder")
	}
}

func TestReleaseFailurePutsTheOldCopyBack(t *testing.T) {
	a, _ := newChamber(t)
	root := t.TempDir()
	dest, backup := filepath.Join(root, "dest"), filepath.Join(root, "bk")
	os.MkdirAll(filepath.Join(dest, "proj"), 0o755)
	os.Mkdir(backup, 0o755)
	os.WriteFile(filepath.Join(dest, "proj", "keep.txt"), []byte("mine"), 0o644)
	a.Limits.MaxRatio = 100_000 // passes inspection...
	it := admit(t, a, mkzip(t, kv{"z.bin", zeros(4 << 20)}), "proj.zip", ownOrigin, true)
	if it.Verdict != Clean {
		t.Fatalf("%+v", it)
	}
	a.Limits.RatioFloor, a.Limits.MaxRatio = 1<<20, 50 // ...the bomb is found only on unpack
	if _, err := a.Release(it.ID, dest, ReleaseOptions{ReplaceInto: backup}); err == nil {
		t.Fatal("released a bomb")
	}
	if readFile(t, filepath.Join(dest, "proj", "keep.txt")) != "mine" {
		t.Error("old copy not put back")
	}
	if ents, _ := os.ReadDir(backup); len(ents) != 0 {
		t.Errorf("backup not empty: %v", ents)
	}
}

func TestAdmitPathMove(t *testing.T) {
	a, _ := newChamber(t)
	src := filepath.Join(t.TempDir(), "t.part")
	os.WriteFile(src, []byte("payload\n"), 0o600)
	it, err := a.AdmitPath(src, "notes.txt", ownOrigin, false, true, "")
	if err != nil || lexists(src) || it.Verdict != Clean || it.Name != "notes.txt" {
		t.Fatal(it, err)
	}
	os.WriteFile(src, []byte("payload\n"), 0o600)
	if _, err := a.AdmitPath(src, "x.txt", ownOrigin, false, false, strings.Repeat("0", 64)); status(err) != 409 {
		t.Errorf("a wrong checksum is refused: %v", err)
	}
}

func TestDestinationDecidesWhatAFileBecomes(t *testing.T) {
	a, _ := newChamber(t)
	hook := []byte("#!/bin/sh\necho hi\n")
	if r := verdict(t, hook, "post-checkout", Own, false); r.Verdict != Clean { // just a script...
		t.Errorf("%+v", r)
	}
	r := NewInspector(Own, DefaultLimits(), nil, `C:\code\shop\.git\hooks`).InspectBytes(hook, "post-checkout", false).JSON()
	if r.Verdict != NeedsHuman || !codes(r, "")["autorun"] { // ...unless it lands in hooks
		t.Errorf("%+v", r)
	}
	o := ownOrigin
	o.Dest = "/srv/repo/.git/hooks"
	if it := admit(t, a, hook, "post-checkout", o, false); it.Verdict != NeedsHuman {
		t.Errorf("%+v", it)
	}
	o.Dest = "/srv/repo/.git"
	if it := admit(t, a, mkzip(t, kv{"post-checkout", string(hook)}), "hooks.zip", o, true); it.Verdict != NeedsHuman {
		t.Errorf("folder 'hooks' into .git: %+v", it)
	}
	startup := `C:\Users\a\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup`
	for _, c := range [][2]string{{startup, "notes.txt"}, {"/home/a/.ssh", "authorized_keys"}, {"/home/a", ".bashrc"},
		{"/Users/a/Library/LaunchAgents", "x.plist"}, {"/home/a/.codex", "config.toml"}} {
		r := NewInspector(Own, DefaultLimits(), nil, c[0]).InspectBytes([]byte("x\n"), c[1], false).JSON()
		if !codes(r, "")["autorun"] {
			t.Errorf("%v: %+v", c, r)
		}
	}
	if r := NewInspector(Own, DefaultLimits(), nil, `C:\Users\a\Documents`).InspectBytes([]byte("x\n"), "notes.txt", false).JSON(); r.Verdict != Clean {
		t.Errorf("%+v", r)
	}
}

// tmx-sec, 7 Oct: a rename that can't cross drives falls back to copy + delete.
func TestReplaceIntoWorksAcrossDrives(t *testing.T) {
	a, _ := newChamber(t)
	sep := string(filepath.Separator)
	real := rename
	rename = func(src, dst string) error {
		if strings.Contains(dst, sep+"bk") || strings.Contains(src, sep+"bk") {
			return &os.LinkError{Op: "rename", Old: src, New: dst, Err: errors.New("cannot move the file to a different disk drive")}
		}
		return real(src, dst)
	}
	t.Cleanup(func() { rename = real })
	root := t.TempDir()
	dest, backup := filepath.Join(root, "dest"), filepath.Join(root, "bk")
	os.Mkdir(dest, 0o755)
	os.Mkdir(backup, 0o755)
	os.WriteFile(filepath.Join(dest, "report.txt"), []byte("old"), 0o644)
	it := admit(t, a, []byte("new"), "report.txt", ownOrigin, false)
	p, err := a.Release(it.ID, dest, ReleaseOptions{Name: "report.txt", ReplaceInto: backup})
	if err != nil || readFile(t, p) != "new" || readFile(t, filepath.Join(backup, "report.txt")) != "old" {
		t.Fatal(p, err)
	}
}

// ---------------------------------------------------------------------------- the fleet rule ---

func TestFleetAutoRelease(t *testing.T) {
	a, _ := newChamber(t)
	it := admit(t, a, pe, "setup.exe", ownOrigin, false)
	if it.Verdict != NeedsHuman {
		t.Fatalf("%+v", it)
	}
	if ShouldAutoRelease(it, FleetTrusts(true)) {
		t.Error("hold_needs_human: true must hold")
	}
	cloud := it
	cloud.Origin.Cloud = true
	if ShouldAutoRelease(cloud, true) {
		t.Error("a cloud box's copy always waits for a human")
	}
	got, label, ok, err := a.AutoRelease(it.ID, "hostc", FleetTrusts(false))
	if err != nil || !ok || got.State != StateApproved || got.ApprovedBy != "fleet copy from hostc (owner's ruling)" {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if !strings.HasPrefix(label, "auto-released (would have needed a human: a program or something Windows runs") {
		t.Errorf("label %q", label)
	}
	if _, err := a.Release(it.ID, t.TempDir(), ReleaseOptions{}); err != nil {
		t.Error(err)
	}
	// blocked: never auto-released, never approvable
	b := admit(t, a, mkzip(t, kv{"../x", "x"}), "a.zip", ownOrigin, false)
	if _, _, ok, _ := a.AutoRelease(b.ID, "hostc", true); ok {
		t.Error("blocked item auto-released")
	}
	if _, err := a.Approve(b.ID, "hostc"); status(err) != 409 {
		t.Errorf("approve blocked: %v", err)
	}
}

func TestPasskeyReasons(t *testing.T) {
	item := func(rs ...Reason) Item { return Item{Verdict: NeedsHuman, Reasons: rs} }
	cases := []struct {
		it   Item
		auto bool
	}{
		{item(Reason{Level: NeedsHuman, Code: "script", Text: "a script (.py)"}), true},
		{item(Reason{Level: NeedsHuman, Code: "pattern", Text: "persistence: 'x'"}), true},
		{item(Reason{Level: NeedsHuman, Code: "pattern", Text: "destructive: 'x'"}), false},
		{item(Reason{Level: NeedsHuman, Code: "pattern", Text: "disabling defences: 'x'"}), false},
		{item(Reason{Level: Clean, Code: "pattern", Text: "destructive: 'x'"}, Reason{Level: NeedsHuman, Code: "program", Text: "p"}), true},
		{item(Reason{Level: Clean, Code: "agent-manipulation", Text: "steer"}, Reason{Level: NeedsHuman, Code: "program", Text: "p"}), false},
		{Item{Verdict: Clean}, false},
	}
	for i, c := range cases {
		if got := ShouldAutoRelease(c.it, true); got != c.auto {
			t.Errorf("case %d: auto = %v (passkey %v)", i, got, PasskeyReasons(c.it))
		}
	}
	if l := AutoLabel([]string{"a", "b", "c", "d"}); l != "auto-released (would have needed a human: a; b; c \u2026)" {
		t.Errorf("%q", l)
	}
	if l := AutoLabel(nil); l != "auto-released (would have needed a human: see the antechamber)" {
		t.Errorf("%q", l)
	}
}

// ------------------------------------------------------------------------- child, reserve ---

func TestMainInspect(t *testing.T) {
	dir := t.TempDir()
	p, out := filepath.Join(dir, "blob"), filepath.Join(dir, "report.json")
	os.WriteFile(p, []byte("hello from hostc\n"), 0o600)
	var so, se bytes.Buffer
	if code := Main([]string{"inspect", "--no-av", "--name", "hello.txt", "--trust", "own", "--out", out, "--context", "", "--", p}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	var r ReportJSON
	if err := json.Unmarshal([]byte(readFile(t, out)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Clean || len(r.Reasons) != 0 {
		t.Errorf("%+v", r)
	}
	if code := Main([]string{"inspect", "--bogus", p}, &so, &se); code != 2 {
		t.Errorf("unknown flag: exit %d", code)
	}
	if code := Main([]string{"inspect", "--no-av", "--limits", `{"max_depth": 1}`, p}, &so, &se); code != 0 || !strings.Contains(so.String(), `"verdict"`) {
		t.Errorf("stdout report: %d %s", code, so.String())
	}
}

func TestTimeoutFor(t *testing.T) {
	if got := timeoutFor(DefaultLimits(), 100<<20); got != 130*time.Second {
		t.Errorf("timeout = %v", got)
	}
}

func TestReserveAt(t *testing.T) {
	real := diskSpace
	t.Cleanup(func() { diskSpace = real })
	total, fail := uint64(10<<30), false
	diskSpace = func(string) (uint64, uint64, error) {
		if fail {
			return 0, 0, errors.New("no")
		}
		return total, total, nil
	}
	for _, c := range []struct {
		env   string
		total uint64
		fail  bool
		want  int64
	}{
		{"0", 0, false, 0}, {"123", 0, false, 123}, {"-5", 0, false, 0},
		{"", 10 << 30, false, 1 << 30}, {"", 100 << 30, false, 2 << 30}, {"", 0, true, 2 << 30},
		{"junk", 10 << 30, false, 1 << 30},
	} {
		total, fail = c.total, c.fail
		if got := reserveAt(".", c.env); got != c.want {
			t.Errorf("reserveAt(env %q, total %d) = %d, want %d", c.env, c.total, got, c.want)
		}
	}
}
