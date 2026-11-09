package antechamber

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ------------------------------------------------------------------------------------- names ---

var bidiRx = regexp.MustCompile("[‎‏؜‪-‮⁦-⁩]")

var reserved = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}
	for i := 0; i < 10; i++ {
		m[fmt.Sprintf("COM%d", i)] = true
		m[fmt.Sprintf("LPT%d", i)] = true
	}
	for _, d := range []string{"\u00b9", "\u00b2", "\u00b3"} { // COM¹ … LPT³: Windows reserves these too
		m["COM"+d] = true
		m["LPT"+d] = true
	}
	return m
}()

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// ProgramExts: opening, or merely showing in Explorer, makes Windows act on these.
var ProgramExts = set(
	"exe", "scr", "com", "pif", "cpl", "msi", "msp", "mst", "dll", "sys", "drv", "ocx", "lnk", "url",
	"scf", "library-ms", "searchconnector-ms", "settingcontent-ms", "hta", "reg", "chm", "jar", "apk",
	"appx", "msix", "appxbundle", "msixbundle", "appinstaller", "application", "appref-ms", "gadget",
	"iso", "img", "vhd", "vhdx", "dmg", "pkg", "xll", "inf", "diagcab", "msc", "wsc", "sct", "deb", "rpm",
	"app", "elf", "run", "ws", "wasm")

// RunExts are files that run by themselves (download-and-execute counts only here, or after a shebang).
var RunExts = set("ps1", "psm1", "bat", "cmd", "sh", "bash", "zsh", "py", "pyw", "js", "mjs", "cjs", "vbs", "vbe", "wsf")

// ScriptExts are scripts and script-carrying documents.
var ScriptExts = set(
	"ps1", "psm1", "psd1", "ps1xml", "bat", "cmd", "vbs", "vbe", "vb", "js", "jse", "mjs", "cjs", "wsf",
	"wsh", "sh", "bash", "zsh", "ksh", "fish", "csh", "py", "pyw", "pyz", "rb", "pl", "pm", "php", "lua",
	"applescript", "scpt", "command", "tool", "ahk", "au3", "tcl", "r", "groovy", "kts", "ts", "html",
	"htm", "xhtml", "svg", "xsl", "xslt", "mht", "mhtml", "desktop", "service", "plist", "nsh", "nsi",
	"makefile", "mk", "gradle", "ipynb")

var docLike = set("pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "txt", "jpg", "jpeg", "png", "gif",
	"mp4", "mp3", "zip", "rtf", "csv", "odt", "html", "htm")

// ext is the lower-case extension of the last path segment ("" if none).
func ext(name string) string {
	base := strings.ToLower(name[strings.LastIndex(name, "/")+1:])
	i := strings.LastIndex(base, ".")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(base[i+1:])
}

var hiddenExtRx = regexp.MustCompile(`\s{3,}\.\w+$`)

// CleanName returns a name safe to create on Windows, macOS and Linux, plus findings about tricks in it.
func CleanName(name string) (string, []Finding) {
	var found []Finding
	raw := name
	if bidiRx.MatchString(raw) {
		found = append(found, mk("rtlo", "the name contains right-to-left control characters, which make it "+
			"display a different extension than it really has", Blocked, "", ""))
	}
	s := strings.ReplaceAll(bidiRx.ReplaceAllString(raw, ""), "\\", "/")
	s = s[strings.LastIndex(s, "/")+1:]
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune("<>:\"|?*\x7f", r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(strings.TrimSpace(s), ". ")
	stem, _, _ := strings.Cut(s, ".")
	if reserved[strings.ToUpper(strings.TrimRight(stem, " "))] { // "CON .txt" is CON too
		s = "_" + s
	}
	if s == "" || s == "." || s == ".." {
		s = "file"
	}
	if rs := []rune(s); len(rs) > 180 {
		e := ext(s)
		keep := 170 - len([]rune(e))
		s = string(rs[:keep])
		if e != "" {
			s += "." + e
		}
	}
	if s != raw {
		found = append(found, mk("name-cleaned", fmt.Sprintf("name %s was made safe: %s", pyRepr(raw), pyRepr(s)), Clean, "", ""))
	}
	parts := strings.Split(strings.ToLower(s), ".")
	n := len(parts)
	last := ""
	if n > 0 {
		last = parts[n-1]
	}
	runs := ProgramExts[last] || (ScriptExts[last] && last != "html" && last != "htm" && last != "svg")
	if n >= 3 && runs && docLike[strings.TrimSpace(parts[n-2])] {
		found = append(found, mk("double-ext", fmt.Sprintf("'%s' looks like a .%s but is a .%s", s, strings.TrimSpace(parts[n-2]), last), NeedsHuman, "", ""))
	} else if hiddenExtRx.MatchString(s) {
		found = append(found, mk("double-ext", fmt.Sprintf("'%s' hides its real extension behind spaces", s), NeedsHuman, "", ""))
	}
	return s, found
}

// --------------------------------------------------------------------------- type by content ---

// mpegFrame: a valid MPEG audio frame header (stricter than the sync bits alone).
func mpegFrame(h []byte) bool {
	if len(h) < 4 || h[0] != 0xFF || h[1]&0xE0 != 0xE0 || h[1] == 0xFE { // FF FE: a UTF-16 text mark
		return false
	}
	version, layer := (h[1]>>3)&3, (h[1]>>1)&3
	bitrate, rate := h[2]>>4, (h[2]>>2)&3
	return version != 1 && layer != 0 && bitrate != 15 && rate != 3
}

func at(h []byte, off int, magic string) bool {
	return len(h) >= off+len(magic) && string(h[off:off+len(magic)]) == magic
}

func slice(h []byte, from, to int) []byte {
	if from > len(h) {
		return nil
	}
	return h[from:min(to, len(h))]
}

// Sniff tells the real type of some bytes from their first ~40 KB (and last 512 bytes for disk images).
func Sniff(h, tail []byte) string {
	if len(h) == 0 {
		return "empty"
	}
	switch {
	case at(h, 0, "MZ"):
		return "windows-program"
	case at(h, 0, "\x7fELF"):
		return "linux-program"
	case at(h, 0, "\xfe\xed\xfa\xce"), at(h, 0, "\xce\xfa\xed\xfe"), at(h, 0, "\xfe\xed\xfa\xcf"), at(h, 0, "\xcf\xfa\xed\xfe"):
		return "mac-program"
	case at(h, 0, "\xca\xfe\xba\xbe"):
		var n uint64
		for _, c := range slice(h, 4, 8) {
			n = n<<8 | uint64(c)
		}
		if n < 40 {
			return "mac-program"
		}
		return "java-class"
	case at(h, 0, "L\x00\x00\x00\x01\x14\x02\x00"):
		return "windows-shortcut"
	case at(h, 0, "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"):
		return "ole"
	case at(h, 0, "PK\x03\x04"), at(h, 0, "PK\x05\x06"), at(h, 0, "PK\x07\x08"):
		return "zip"
	case at(h, 0, "Rar!\x1a\x07"):
		return "rar"
	case at(h, 0, "7z\xbc\xaf\x27\x1c"):
		return "7z"
	case at(h, 0, "\x1f\x8b"):
		return "gzip"
	case at(h, 0, "BZh"):
		return "bzip2"
	case at(h, 0, "\xfd7zXZ\x00"):
		return "xz"
	case at(h, 0, "MSCF"):
		return "cab"
	case at(h, 0, "ITSF"):
		return "windows-help"
	case at(h, 0, "conectix"), at(h, 0, "vhdxfile"), at(tail, 0, "conectix"), at(tail, 0, "koly"):
		return "disk-image"
	case at(h, 0x8001, "CD001"), at(h, 0x8801, "CD001"), at(h, 0x9001, "CD001"),
		at(h, 0, "\x00\x00\x00\x00") && bytes.Contains(slice(h, 0x8000, 0x9000), []byte("NSR0")):
		return "disk-image"
	case at(h, 257, "ustar"):
		return "tar"
	case bytes.Contains(slice(h, 0, 1024), []byte("%PDF-")):
		return "pdf"
	case at(h, 0, "{\\rtf"):
		return "rtf"
	case at(h, 0, "\x00asm"):
		return "wasm"
	case at(h, 0, "dex\n"):
		return "android-program"
	case at(h, 0, "regf"):
		return "registry-hive"
	}
	for _, m := range [][2]string{{"\x89PNG", "png"}, {"\xff\xd8\xff", "jpeg"}, {"GIF8", "gif"}, {"BM", "bmp"},
		{"\x00\x00\x01\x00", "ico"}, {"OggS", "ogg"}, {"fLaC", "flac"}, {"ID3", "mp3"},
		{"\x1aE\xdf\xa3", "matroska"}, {"SQLite format 3\x00", "sqlite"}} {
		if at(h, 0, m[0]) {
			return m[1]
		}
	}
	if mpegFrame(h) { // an MP3 without an ID3 tag starts right at a frame
		return "mp3"
	}
	if at(h, 0, "RIFF") {
		switch string(slice(h, 8, 12)) {
		case "WEBP":
			return "webp"
		case "WAVE":
			return "wav"
		case "AVI ":
			return "avi"
		}
		return "riff"
	}
	if at(h, 4, "ftyp") { // ISO base media: the major brand tells photos from videos
		switch string(slice(h, 8, 12)) {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return "heic"
		case "avif", "avis":
			return "avif"
		case "qt  ":
			return "mov"
		}
		return "mp4"
	}
	return textKind(h)
}

// cp1252 maps 0x80-0x9F; 0xFFFD where Windows-1252 has no character (Python's errors="replace").
var cp1252 = [32]rune{0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
	0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178}

func decodeCP1252(b []byte) string {
	var s strings.Builder
	s.Grow(len(b))
	for _, c := range b {
		if c >= 0x80 && c < 0xA0 {
			s.WriteRune(cp1252[c-0x80])
		} else {
			s.WriteRune(rune(c))
		}
	}
	return s.String()
}

func decodeUTF16(b []byte) string {
	var order binary.ByteOrder = binary.LittleEndian
	if b[0] == 0xFE {
		order = binary.BigEndian
	}
	b = b[2:]
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, order.Uint16(b[i:]))
	}
	s := string(utf16.Decode(units))
	if len(b)%2 == 1 {
		s += "�"
	}
	return s
}

// firstInvalid is the index of the first byte that is not valid UTF-8, or -1.
func firstInvalid(b []byte) int {
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && n <= 1 {
			return i
		}
		i += n
	}
	return -1
}

// decodeText decodes text bytes (UTF-16 with a BOM, UTF-8, else Windows-1252 without control
// characters); ok is false for binary.
func decodeText(b []byte) (string, bool) {
	if at(b, 0, "\xff\xfe") || at(b, 0, "\xfe\xff") {
		return decodeUTF16(b), true
	}
	if bytes.IndexByte(slice(b, 0, 8192), 0) >= 0 {
		return "", false
	}
	body, bom := b, 0
	if at(b, 0, "\xef\xbb\xbf") {
		body, bom = b[3:], 3
	}
	i := firstInvalid(body)
	if i < 0 {
		return string(body), true
	}
	if i+bom >= len(b)-3 { // cut in the middle of a character
		return string(body[:i]), true
	}
	ctrl := 0
	for _, c := range slice(b, 0, 8192) {
		if c < 32 && c != 9 && c != 10 && c != 12 && c != 13 && c != 27 {
			ctrl++
		}
	}
	if ctrl == 0 {
		return decodeCP1252(b), true
	}
	return "", false
}

func textKind(h []byte) string {
	t, ok := decodeText(h)
	if !ok {
		return "binary"
	}
	rs := []rune(t)
	low := strings.ToLower(strings.TrimLeftFunc(string(rs[:min(len(rs), 4096)]), isSpace))
	lr := []rune(low)
	if strings.HasPrefix(low, "<!doctype html") || strings.HasPrefix(low, "<html") ||
		strings.Contains(string(lr[:min(len(lr), 1024)]), "<html") {
		return "html"
	}
	if strings.HasPrefix(low, "<svg") || (strings.HasPrefix(low, "<?xml") && strings.Contains(string(lr[:min(len(lr), 2048)]), "<svg")) {
		return "svg"
	}
	return "text"
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' ||
		r == 0x1c || r == 0x1d || r == 0x1e || r == 0x1f || r == 0x85 || r == 0xa0 || (r > 0xff && strings.TrimSpace(string(r)) == "")
}
