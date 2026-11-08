package antechamber

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

type budget struct {
	l        Limits
	maxTotal int64
	members  int
	total    int64
}

func newBudget(l Limits, trust string) *budget {
	b := &budget{l: l, maxTotal: l.MaxTotalPerson}
	if trust == Own {
		b.maxTotal = l.MaxTotalOwn
	}
	return b
}

// bombError stops an inspection or an extraction: an archive bomb limit was crossed.
type bombError struct{ msg string }

func (e *bombError) Error() string { return e.msg }

func bomb(format string, a ...any) error { return &bombError{fmt.Sprintf(format, a...)} }

// readCapped reads a decompressing stream, counting what it really yields (headers are not
// trusted). It returns the first keep bytes, the total size and whether all of it was kept.
// ratioBase < 0 means no per-member ratio check. A limit crossed is a *bombError.
func readCapped(r io.Reader, b *budget, keep, ratioBase int64, what string) ([]byte, int64, bool, error) {
	var buf []byte
	var n int64
	chunk := make([]byte, 1<<20)
	for {
		k, err := r.Read(chunk)
		if k > 0 {
			n += int64(k)
			b.total += int64(k)
			if b.total > b.maxTotal {
				return nil, n, false, bomb("more than %d MB once unpacked (archive bomb limit)", b.maxTotal>>20)
			}
			if ratioBase >= 0 && n > b.l.RatioFloor && n > b.l.MaxRatio*max(ratioBase, 1) {
				return nil, n, false, bomb("%s unpacks more than %dx its packed size (archive bomb)", what, b.l.MaxRatio)
			}
			if int64(len(buf)) < keep {
				buf = append(buf, chunk[:min(int64(k), keep-int64(len(buf)))]...)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return buf, n, false, err
		}
	}
	return buf, n, n <= keep, nil
}

var driveRx = regexp.MustCompile(`^[a-zA-Z]:`)

// unsafeMember says why an archive entry name is unsafe, or "".
func unsafeMember(name string) string {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || driveRx.MatchString(n) {
		return "an absolute path"
	}
	for _, p := range strings.Split(n, "/") {
		if p == ".." {
			return "'..' (it would be written outside the folder)"
		}
	}
	if bidiRx.MatchString(n) {
		return "right-to-left control characters"
	}
	return ""
}

// linkEscapes: a link named member pointing at target would leave the archive's folder.
func linkEscapes(member, target string) bool {
	t := strings.ReplaceAll(target, "\\", "/")
	if strings.HasPrefix(t, "/") || driveRx.MatchString(t) {
		return true
	}
	depth := 0
	for _, part := range strings.Split(path.Dir(member)+"/"+t, "/") {
		switch part {
		case "", ".":
		case "..":
			depth--
		default:
			depth++
		}
		if depth < 0 {
			return true
		}
	}
	return false
}

// Inspector looks at items without opening or running them.
type Inspector struct {
	patterns []pattern
	Trust    string
	Limits   Limits
	KnownBad map[string]bool
	// Context is where the item will be placed (a folder path), so a file named 'post-checkout'
	// sent into '<repo>/.git/hooks' is seen as the git hook it would become.
	Context string
}

// NewInspector returns an inspector with the given trust and limits; context may be "".
func NewInspector(trust string, limits Limits, knownBad map[string]bool, context string, ownerNames ...string) *Inspector {
	return &Inspector{patterns: ownerPatterns(ownerNames), Trust: trust, Limits: limits, KnownBad: knownBad,
		Context: strings.TrimRight(strings.ReplaceAll(context, "\\", "/"), "/")}
}

// InspectPath inspects a file on disk.
func (in *Inspector) InspectPath(p, name string, folder bool) (*Report, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("antechamber: inspect: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("antechamber: inspect: %w", err)
	}
	return in.Inspect(f, st.Size(), name, folder), nil
}

// InspectBytes inspects bytes in memory.
func (in *Inspector) InspectBytes(data []byte, name string, folder bool) *Report {
	return in.Inspect(bytes.NewReader(data), int64(len(data)), name, folder)
}

// Inspect inspects size bytes read through r, named name (folder: sent as a folder, a ZIP).
func (in *Inspector) Inspect(r io.ReaderAt, size int64, name string, folder bool) *Report {
	rep := &Report{Trust: in.Trust, Kind: "empty"}
	b := newBudget(in.Limits, in.Trust)
	safe, fs := CleanName(name)
	for _, f := range fs {
		rep.Add(f)
	}
	kind, err := in.one(r, size, safe, "", 0, rep, b)
	if err != nil { // as in Python, a bomb leaves the kind unset ("empty")
		rep.Add(mk("bomb", err.Error(), Blocked, "", ""))
	} else {
		rep.Kind = kind
	}
	if folder && rep.Kind != "zip" {
		rep.Add(mk("not-folder", fmt.Sprintf("sent as a folder but the content is %s, not a ZIP", rep.Kind), NeedsHuman, "", ""))
	}
	rep.Members, rep.Walked = b.members, b.total
	return rep
}

func readAt(r io.ReaderAt, off, n int64) []byte {
	if n <= 0 {
		return nil
	}
	buf := make([]byte, n)
	k, _ := r.ReadAt(buf, off)
	return buf[:k]
}

func sha256Of(r io.ReaderAt, size int64) string {
	h := sha256.New()
	io.Copy(h, io.NewSectionReader(r, 0, size))
	return hex.EncodeToString(h.Sum(nil))
}

var archiveKinds = set("zip", "tar", "gzip", "bzip2", "xz")

// one inspects one file (top level or archive member) and returns its kind. The error is only
// ever a *bombError.
func (in *Inspector) one(r io.ReaderAt, size int64, name, where string, depth int, rep *Report, b *budget) (string, error) {
	loc := where
	if loc == "" {
		loc = name
	}
	head := readAt(r, 0, min(size, 40*1024))
	var tail []byte
	if size > 1024 {
		tail = readAt(r, max(0, size-512), 512)
	}
	kind := Sniff(head, tail)
	e := ext(name)
	if len(in.KnownBad) > 0 {
		if d := sha256Of(r, size); in.KnownBad[d] {
			rep.Add(mk("known-bad", fmt.Sprintf("on the known-bad list (SHA-256 %s…)", d[:16]), Blocked, "", loc))
		}
	}
	p := loc
	if in.Context != "" {
		p = in.Context + "/" + loc
	}
	var content []byte
	if kind == "text" || kind == "html" || kind == "svg" {
		content = head
	}
	for _, f := range PathFindings(p, content) {
		rep.Add(f)
	}
	in.mismatch(kind, e, loc, rep)

	switch kind {
	case "windows-program", "linux-program", "mac-program", "java-class", "windows-shortcut",
		"windows-help", "wasm", "android-program", "registry-hive":
		rep.Add(mk("program", "a program or something Windows runs: "+strings.ReplaceAll(kind, "-", " "), NeedsHuman, "", loc))
	default:
		if ProgramExts[e] && kind != "zip" && kind != "disk-image" {
			rep.Add(mk("program", fmt.Sprintf(".%s files are run or acted on by Windows or macOS", e), NeedsHuman, "", loc))
		}
	}
	var err error
	switch kind {
	case "zip":
		err = in.zip(r, size, loc, depth, rep, b)
	case "tar", "gzip", "bzip2", "xz":
		err = in.compressed(r, size, kind, name, loc, depth, rep, b)
	case "rar", "7z", "cab", "disk-image":
		rep.Add(mk("uninspectable", kind+" archive or disk image: tmx cannot look inside it", NeedsHuman, "", loc))
	case "ole":
		in.ole(readAt(r, 0, min(size, in.Limits.BinaryScan)), e, loc, rep)
	case "pdf":
		in.pdf(readAt(r, 0, min(size, in.Limits.BinaryScan)), loc, rep)
	case "rtf":
		body := bytes.ToLower(readAt(r, 0, min(size, in.Limits.BinaryScan)))
		if rtfObjRx.Match(body) {
			rep.Add(mk("active-content", "RTF with embedded objects (a classic exploit carrier)", NeedsHuman, "", loc))
		}
	case "text", "html", "svg":
		in.text(readAt(r, 0, min(size, in.Limits.TextScan)), kind, e, name, loc, rep)
	case "binary":
		if !ProgramExts[e] {
			rep.Add(mk("unknown", "unknown binary format", Clean, NeedsHuman, loc))
		}
	}
	return kind, err
}

var rtfObjRx = regexp.MustCompile(`\\object|\\objdata|\\objupdate|\\objemb|\\objlink`)

var expectKinds = map[string][]string{
	"pdf": {"pdf"}, "png": {"png"}, "jpg": {"jpeg"}, "jpeg": {"jpeg"}, "gif": {"gif"},
	"docx": {"zip"}, "xlsx": {"zip"}, "pptx": {"zip"}, "odt": {"zip"}, "zip": {"zip"},
	"doc": {"ole", "rtf"}, "xls": {"ole"}, "ppt": {"ole"}, "msg": {"ole"},
	"txt": {"text", "empty"}, "md": {"text", "empty"}, "csv": {"text", "empty"},
	"json": {"text", "empty"}, "mp4": {"mp4", "mov"}, "mov": {"mov", "mp4"}, "mp3": {"mp3"}, "wav": {"wav"},
	"exe": {"windows-program"}, "dll": {"windows-program"}, "webp": {"webp"},
	"tar": {"tar"}, "gz": {"gzip"}, "tgz": {"gzip"},
}

func (in *Inspector) mismatch(kind, e, loc string, rep *Report) {
	expect, ok := expectKinds[e]
	if !ok {
		return
	}
	for _, k := range expect {
		if k == kind {
			return
		}
	}
	rep.Add(mk("ext-mismatch", fmt.Sprintf("named .%s but the content is %s", e, strings.ReplaceAll(kind, "-", " ")), NeedsHuman, "", loc))
}

var htmlScriptRx = regexp.MustCompile(`(?i)<script\b|\bon[a-z]+\s*=|javascript:`)

func (in *Inspector) text(raw []byte, kind, e, name, loc string, rep *Report) {
	text, ok := decodeText(raw)
	if !ok || text == "" {
		text = strings.ToValidUTF8(string(raw), "�")
	}
	base := strings.ToLower(name[strings.LastIndex(name, "/")+1:])
	script := ScriptExts[e] || strings.HasPrefix(text, "#!") || kind == "html" || kind == "svg" ||
		base == "makefile" || base == "dockerfile" || base == "justfile" || base == "rakefile"
	if e == "hta" {
		return // already a program
	}
	if kind == "html" || kind == "svg" || set("html", "htm", "svg", "xhtml", "mht", "mhtml")[e] {
		if htmlScriptRx.MatchString(text) {
			label := strings.ToUpper(kind)
			if kind == "text" {
				label = strings.ToUpper(e)
			}
			rep.Add(mk("script", label+" with scripts", Clean, NeedsHuman, loc))
		}
	} else if script {
		x := e
		if x == "" {
			x = "shebang"
		}
		rep.Add(mk("script", fmt.Sprintf("a script (.%s)", x), Clean, NeedsHuman, loc))
	}
	runs := RunExts[e] || strings.HasPrefix(text, "#!")
	for _, f := range scanPatterns(in.patterns, text, !script, loc) {
		if !runs && strings.HasPrefix(f.Text, "download and execute") {
			// tmx-sec, 10 Oct: only in a file that RUNS (a script, a shebang file); in documentation
			// it is a library's install line, not an instruction.
			f.Levels[0] = Clean
		}
		rep.Add(f)
	}
}

func u16(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2*len(units))
	for _, c := range units {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

func (in *Inspector) ole(body []byte, e, loc string, rep *Report) {
	has := func(s string) bool { return bytes.Contains(body, u16(s)) }
	if has("_VBA_PROJECT") || (has("VBA") && has("dir")) || has("Macros") {
		rep.Add(mk("active-content", "Office document with macros", NeedsHuman, "", loc))
	}
	if has("Ole10Native") || has("\x01Ole10Native") {
		rep.Add(mk("active-content", "contains a packaged file or program (OLE package)", NeedsHuman, "", loc))
	}
	office := has("WordDocument") || has("Workbook") || has("Book") || has("PowerPoint Document") || has("__substg1.0_")
	if !office || e == "msi" || e == "msp" || e == "mst" {
		rep.Add(mk("program", "a Windows installer or unknown compound file", NeedsHuman, "", loc))
	}
}

var (
	pdfStreamRx = regexp.MustCompile(`stream\r?\n`)
	pdfHexRx    = regexp.MustCompile(`#([0-9a-fA-F]{2})`)
	pdfNameRx   = regexp.MustCompile(`/(JavaScript|JS|Launch|EmbeddedFile|RichMedia|XFA|AA|OpenAction|SubmitForm|ImportData)\b`)
)

func (in *Inspector) pdf(body []byte, loc string, rep *Report) {
	chunks := [][]byte{body}
	left := int64(64 << 20)
	for _, m := range pdfStreamRx.FindAllIndex(body, -1) { // object streams can hide the names
		if left <= 0 {
			break
		}
		zr, err := zlib.NewReader(bytes.NewReader(body[m[1]:min(len(body), m[1]+(8<<20))]))
		if err != nil {
			continue
		}
		d, err := io.ReadAll(io.LimitReader(zr, 8<<20))
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			continue
		}
		left -= int64(len(d))
		chunks = append(chunks, d)
	}
	names := map[string]bool{}
	for _, c := range chunks {
		c = pdfHexRx.ReplaceAllFunc(c, func(m []byte) []byte {
			v, _ := strconv.ParseUint(string(m[1:]), 16, 8)
			return []byte{byte(v)}
		})
		for _, m := range pdfNameRx.FindAllSubmatch(c, -1) {
			names[string(m[1])] = true
		}
	}
	var risky []string
	for n := range names {
		if n != "AA" && n != "OpenAction" {
			risky = append(risky, n)
		}
	}
	if len(risky) > 0 || (names["AA"] && names["OpenAction"]) {
		show := risky
		if len(show) == 0 {
			for n := range names {
				show = append(show, n)
			}
		}
		sort.Strings(show)
		rep.Add(mk("active-content", "PDF with active content: "+strings.Join(show, ", "), NeedsHuman, "", loc))
	}
}

func head40(d []byte) []byte { return d[:min(len(d), 40960)] }

func (in *Inspector) member(data []byte, full bool, mname, loc string, depth int, rep *Report, b *budget) error {
	if !full {
		if k := Sniff(head40(data), nil); archiveKinds[k] {
			rep.Add(mk("uninspectable", fmt.Sprintf("nested archive larger than %d MB: not inspected", in.Limits.NestedCap>>20), NeedsHuman, "", loc))
			return nil
		}
	}
	if depth+1 > in.Limits.MaxDepth && archiveKinds[Sniff(head40(data), nil)] {
		return bomb("archives nested more than %d deep (archive bomb limit)", in.Limits.MaxDepth)
	}
	_, err := in.one(bytes.NewReader(data), int64(len(data)), mname, loc, depth+1, rep, b)
	return err
}

func isBomb(err error) bool {
	var be *bombError
	return errors.As(err, &be)
}

const sIFMT, sIFLNK = 0o170000, 0o120000

func zipIsLink(f *zip.File) bool {
	return f.CreatorVersion>>8 == 3 && (f.ExternalAttrs>>16)&sIFMT == sIFLNK
}

func (in *Inspector) zip(r io.ReaderAt, size int64, loc string, depth int, rep *Report, b *budget) error {
	zr, err := zip.NewReader(r, size)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
		rep.Add(mk("damaged", "damaged ZIP: "+err.Error(), NeedsHuman, "", loc))
		return nil
	}
	names := map[string]bool{}
	var low []string
	for _, f := range zr.File {
		names[f.Name] = true
		low = append(low, strings.ToLower(f.Name))
	}
	anyLow := func(ok func(string) bool) bool {
		for _, n := range low {
			if ok(n) {
				return true
			}
		}
		return false
	}
	if names["[Content_Types].xml"] || names["mimetype"] {
		if anyLow(func(n string) bool { return strings.HasSuffix(n, "vbaproject.bin") || strings.HasPrefix(n, "basic/") }) {
			rep.Add(mk("active-content", "Office document with macros", NeedsHuman, "", loc))
		}
		if anyLow(func(n string) bool {
			return strings.Contains(n, "/embeddings/") && (strings.Contains(n, "oleobject") || strings.HasSuffix(n, ".bin"))
		}) || anyLow(func(n string) bool { return strings.HasSuffix(n, "activex.xml") || strings.Contains(n, "/activex/") }) {
			rep.Add(mk("active-content", "Office document with embedded objects or ActiveX", NeedsHuman, "", loc))
		}
	}
	if names["META-INF/MANIFEST.MF"] {
		for n := range names {
			if strings.HasSuffix(n, ".class") {
				rep.Add(mk("program", "a Java program (JAR)", NeedsHuman, "", loc))
				break
			}
		}
	}
	if names["AndroidManifest.xml"] || names["AppxManifest.xml"] || names["AppxMetadata/AppxBundleManifest.xml"] {
		rep.Add(mk("program", "an app package (Android / MSIX)", NeedsHuman, "", loc))
	}
	for _, f := range zr.File {
		b.members++
		if b.members > in.Limits.MaxMembers {
			return bomb("more than %d files (archive bomb limit)", in.Limits.MaxMembers)
		}
		mloc := f.Name
		if depth > 0 {
			mloc = loc + "/" + f.Name
		}
		if bad := unsafeMember(f.Name); bad != "" {
			rep.Add(mk("traversal", "archive entry with "+bad, Blocked, "", mloc))
			continue
		}
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if f.Flags&0x1 != 0 {
			rep.Add(mk("encrypted", "password-protected archive entries cannot be inspected", NeedsHuman, "", mloc))
			continue
		}
		rc, err := f.Open()
		if err != nil {
			if errors.Is(err, zip.ErrAlgorithm) {
				rep.Add(mk("uninspectable", "entry cannot be unpacked by tmx: "+err.Error(), NeedsHuman, "", mloc))
			} else {
				rep.Add(mk("damaged", "damaged entry: "+err.Error(), NeedsHuman, "", mloc))
			}
			continue
		}
		data, _, full, err := readCapped(rc, b, in.Limits.NestedCap, int64(f.CompressedSize64), mloc)
		rc.Close()
		if isBomb(err) {
			return err
		}
		if err != nil {
			rep.Add(mk("damaged", "damaged entry: "+err.Error(), NeedsHuman, "", mloc))
			continue
		}
		if zipIsLink(f) {
			tgt := strings.ToValidUTF8(string(data), "�")
			if linkEscapes(f.Name, tgt) {
				rep.Add(mk("traversal", fmt.Sprintf("link pointing outside the folder (%s)", pyRepr(tgt)), Blocked, "", mloc))
			} else {
				rep.Add(mk("link", "a link (it will not be created)", Clean, NeedsHuman, mloc))
			}
			continue
		}
		if err := in.member(data, full, f.Name, mloc, depth, rep, b); err != nil {
			return err
		}
	}
	return nil
}

var compressedSuffixRx = regexp.MustCompile(`(?i)\.(gz|bz2|xz|tgz)$`)

func decompressor(kind string, r io.Reader) (io.Reader, error) {
	switch kind {
	case "gzip":
		return gzip.NewReader(r)
	case "bzip2":
		return bzip2.NewReader(r), nil
	}
	return r, nil
}

func (in *Inspector) compressed(r io.ReaderAt, size int64, kind, name, loc string, depth int, rep *Report, b *budget) error {
	if kind == "xz" {
		// The Go standard library has no xz decoder and tmx-go takes no new dependency for it yet.
		rep.Add(mk("uninspectable", "xz data: tmx cannot look inside it yet", NeedsHuman, "", loc))
		return nil
	}
	open := func() (io.Reader, error) { return decompressor(kind, io.NewSectionReader(r, 0, size)) }
	dr, err := open()
	var peek []byte
	if err == nil {
		peek = make([]byte, 512)
		var k int
		k, err = io.ReadFull(dr, peek)
		peek = peek[:k]
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			err = nil
		}
	}
	if err != nil {
		rep.Add(mk("damaged", fmt.Sprintf("damaged %s data: %v", kind, err), NeedsHuman, "", loc))
		return nil
	}
	if kind == "tar" || at(peek, 257, "ustar") {
		dr, _ := open()
		return in.tar(dr, size, loc, depth, rep, b)
	}
	dr, _ = open()
	data, _, full, err := readCapped(dr, b, in.Limits.NestedCap, size, loc)
	if isBomb(err) {
		return err
	}
	if err != nil {
		rep.Add(mk("damaged", fmt.Sprintf("damaged %s data: %v", kind, err), NeedsHuman, "", loc))
		return nil
	}
	inner := compressedSuffixRx.ReplaceAllString(name, "")
	if inner == "" {
		inner = "file"
	}
	mloc := inner
	if depth > 0 {
		mloc = loc + "/" + inner
	}
	return in.member(data, full, inner, mloc, depth, rep, b)
}

func (in *Inspector) tar(r io.Reader, size int64, loc string, depth int, rep *Report, b *budget) error {
	tr := tar.NewReader(r)
	for {
		ti, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			rep.Add(mk("damaged", "damaged tar archive: "+err.Error(), NeedsHuman, "", loc))
			return nil
		}
		b.members++
		if b.members > in.Limits.MaxMembers {
			return bomb("more than %d files (archive bomb limit)", in.Limits.MaxMembers)
		}
		mloc := ti.Name
		if depth > 0 {
			mloc = loc + "/" + ti.Name
		}
		if bad := unsafeMember(ti.Name); bad != "" {
			rep.Add(mk("traversal", "archive entry with "+bad, Blocked, "", mloc))
			continue
		}
		switch ti.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			if linkEscapes(ti.Name, ti.Linkname) || (ti.Typeflag == tar.TypeLink && unsafeMember(ti.Linkname) != "") {
				rep.Add(mk("traversal", fmt.Sprintf("link pointing outside the folder (%s)", pyRepr(ti.Linkname)), Blocked, "", mloc))
			} else {
				rep.Add(mk("link", "a link (it will not be created)", Clean, NeedsHuman, mloc))
			}
			continue
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			rep.Add(mk("traversal", "a device or pipe entry", Blocked, "", mloc))
			continue
		case tar.TypeReg, tar.TypeCont, tar.TypeGNUSparse: // the reader reports old-style files (TypeRegA) as TypeReg
		default:
			continue
		}
		if ti.Mode&0o6000 != 0 {
			rep.Add(mk("program", "setuid/setgid file", NeedsHuman, "", mloc))
		}
		data, _, full, err := readCapped(tr, b, in.Limits.NestedCap, -1, mloc)
		if isBomb(err) {
			return err
		}
		if err != nil {
			rep.Add(mk("damaged", "damaged tar archive: "+err.Error(), NeedsHuman, "", loc))
			return nil
		}
		if b.total > in.Limits.RatioFloor && b.total > in.Limits.MaxRatio*max(size, 1) {
			what := loc
			if what == "" {
				what = "archive"
			}
			return bomb("%s unpacks more than %dx its packed size (archive bomb)", what, in.Limits.MaxRatio)
		}
		if err := in.member(data, full, ti.Name, mloc, depth, rep, b); err != nil {
			return err
		}
	}
}
