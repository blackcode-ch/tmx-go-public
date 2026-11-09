// Package antechamber is the quarantine and inspection area for everything that crosses machines:
// a port of tmx v2's Python antechamber.py (see DESIGN.md, "Antechamber"). Bytes from another
// machine land in <root>/<id>/blob (no extension, read-only), are inspected without ever being
// opened or run, and get a verdict: clean, needs_human or blocked, each with plain reasons. Only
// clean items, or needs_human items a human approved, can be released into a folder; release never
// overwrites and marks what it places as coming from another machine.
package antechamber

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Verdicts and their order: clean < needs_human < blocked.
const (
	Clean      = "clean"
	NeedsHuman = "needs_human"
	Blocked    = "blocked"
)

// Trust levels: one of the owner's own machines, or another person.
const (
	Own    = "own"
	Person = "person"
)

var rank = map[string]int{Clean: 0, NeedsHuman: 1, Blocked: 2}

// Rank orders verdicts: clean 0, needs_human 1, blocked 2.
func Rank(level string) int { return rank[level] }

// Limits are the inspection and store limits; the JSON names are Python's DEFAULT_LIMITS keys.
type Limits struct {
	MaxDepth       int     `json:"max_depth"`        // archive inside archive
	MaxMembers     int     `json:"max_members"`      // files in all archives together
	MaxTotalOwn    int64   `json:"max_total_own"`    // uncompressed bytes walked, own machines
	MaxTotalPerson int64   `json:"max_total_person"` // ... another person
	MaxRatio       int64   `json:"max_ratio"`        // one member's compression ratio (above RatioFloor)
	RatioFloor     int64   `json:"ratio_floor"`
	NestedCap      int64   `json:"nested_cap"`     // bytes of one nested archive held in memory
	TextScan       int64   `json:"text_scan"`      // bytes of a text item scanned for patterns
	BinaryScan     int64   `json:"binary_scan"`    // bytes of a PDF / OLE file scanned for active content
	TimeoutS       float64 `json:"timeout_s"`      // inspection child; plus 1 s per 10 MB
	QuarantineMax  int64   `json:"quarantine_max"` // undecided items, not retained completed ones
	MinFree        *int64  `json:"min_free"`       // bytes kept free; nil = ReserveAt (2 GB or a tenth)
	KeepDays       float64 `json:"keep_days"`      // refused / blocked / expired items, then deleted
	ReleasedKeepH  float64 `json:"released_keep_h"`
	UploadIdleS    float64 `json:"upload_idle_s"`
	RescanAfterS   float64 `json:"rescan_after_s"` // antivirus again before releasing an old approval
	MaxFileOwn     int64   `json:"max_file_own"`
	MaxFilePerson  int64   `json:"max_file_person"`
}

// DefaultLimits are Python's DEFAULT_LIMITS, same numbers.
func DefaultLimits() Limits {
	return Limits{
		MaxDepth: 4, MaxMembers: 20_000, MaxTotalOwn: 2 << 30, MaxTotalPerson: 512 << 20,
		MaxRatio: 200, RatioFloor: 1 << 20, NestedCap: 64 << 20, TextScan: 8 << 20,
		BinaryScan: 64 << 20, TimeoutS: 120, QuarantineMax: 5 << 30, KeepDays: 7,
		ReleasedKeepH: 24, UploadIdleS: 3600, RescanAfterS: 3600,
		MaxFileOwn: 4 << 30, MaxFilePerson: 25 << 20,
	}
}

// Error is a refusal with an HTTP-like status (Python's AntechamberError).
type Error struct {
	Status int
	Text   string
}

func (e *Error) Error() string { return e.Text }

func errf(status int, format string, a ...any) error {
	return &Error{Status: status, Text: fmt.Sprintf(format, a...)}
}

// Finding is one reason, with its level for an own machine and for another person.
type Finding struct {
	Code   string
	Text   string
	Levels [2]string // (own machine, another person)
	Where  string
}

// Level is the finding's level for the given trust.
func (f Finding) Level(trust string) string {
	if trust == Own {
		return f.Levels[0]
	}
	return f.Levels[1]
}

func mk(code, text, own, person, where string) Finding {
	if person == "" {
		person = own
	}
	return Finding{Code: code, Text: text, Levels: [2]string{own, person}, Where: where}
}

// Reason is a finding as stored and shown: its level for the item's trust.
type Reason struct {
	Level string `json:"level"`
	Code  string `json:"code"`
	Text  string `json:"text"`
	Where string `json:"where"`
}

// Report is the result of inspecting one item.
type Report struct {
	Trust    string
	Kind     string
	Findings []Finding
	Members  int
	Walked   int64 // uncompressed bytes looked at
}

// ReportJSON is a report as written by the inspection child and kept in item.json.
type ReportJSON struct {
	Verdict string   `json:"verdict"`
	Kind    string   `json:"kind"`
	Trust   string   `json:"trust"`
	Members int      `json:"members"`
	Walked  int64    `json:"walked"`
	Reasons []Reason `json:"reasons"`
}

// Verdict is the highest level among the findings (clean when there are none).
func (r *Report) Verdict() string {
	v := Clean
	for _, f := range r.Findings {
		if l := f.Level(r.Trust); rank[l] > rank[v] {
			v = l
		}
	}
	return v
}

// Add keeps one line per (code, text): a folder of 400 scripts is one reason with a count, not 400.
func (r *Report) Add(f Finding) {
	for i := range r.Findings {
		g := &r.Findings[i]
		if g.Code == f.Code && g.Text == f.Text {
			if !strings.Contains(g.Where, "more") {
				g.Where += " (and more)"
			}
			return
		}
	}
	r.Findings = append(r.Findings, f)
}

// Reasons lists the findings, highest level first (stable).
func (r *Report) Reasons() []Reason {
	order := append([]Finding(nil), r.Findings...)
	sort.SliceStable(order, func(i, j int) bool { return rank[order[i].Level(r.Trust)] > rank[order[j].Level(r.Trust)] })
	out := make([]Reason, 0, len(order))
	for _, f := range order {
		out = append(out, Reason{Level: f.Level(r.Trust), Code: f.Code, Text: f.Text, Where: f.Where})
	}
	return out
}

// JSON is the report as Python's to_dict.
func (r *Report) JSON() ReportJSON {
	return ReportJSON{Verdict: r.Verdict(), Kind: r.Kind, Trust: r.Trust, Members: r.Members,
		Walked: r.Walked, Reasons: r.Reasons()}
}

// pyRepr quotes s the way Python's repr() does, so finding texts match the Python ones.
func pyRepr(s string) string {
	q := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		q = '"'
	}
	var b strings.Builder
	b.WriteRune(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == q:
			b.WriteRune('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case !unicode.IsPrint(r) && r != ' ':
			switch {
			case r <= 0xff:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xffff:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteRune(q)
	return b.String()
}
