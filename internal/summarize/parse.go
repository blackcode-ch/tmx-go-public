package summarize

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Claude is asked for one JSON object, but a model may wrap it in a code fence, put a sentence
// before it, leave a trailing comma, name a field a little differently or give a list of
// strings where objects were asked for. parseAnswer takes all of that; only the summary itself
// is required. Every text is trimmed and capped, and the lists are capped too.

const (
	maxSummaryRunes = 2000
	maxItemRunes    = 400
	maxListItems    = 12
	maxChapters     = 12
)

// errNoAnswer: Claude's answer has no summary in it.
var errNoAnswer = errors.New("Claude's answer has no summary")

// parseAnswer reads Claude's answer into a summary (Summary and the lists; chapters have TMS
// only).
func parseAnswer(text string) (model.RecordingSummary, error) {
	var out model.RecordingSummary
	text = strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
	obj, ok := findObject(text)
	if !ok {
		// No JSON at all: a model that answered in prose still wrote a summary.
		if !strings.ContainsAny(text, "{}") && len(strings.Fields(text)) >= 8 {
			out.Summary = clip(text, maxSummaryRunes)
			fill(&out)
			return out, nil
		}
		return out, fmt.Errorf("%w (it said: %s)", errNoAnswer, clip(oneLine(text), 200))
	}
	fields := map[string]any{}
	for k, v := range obj {
		fields[normKey(k)] = v
	}
	out.Summary = clip(textOf(fields["summary"]), maxSummaryRunes)
	out.KeyPoints = stringsOf(first(fields, "keypoints", "points", "highlights"))
	out.Decisions = stringsOf(fields["decisions"])
	out.ActionItems = itemsOf(first(fields, "actionitems", "actions", "todos", "tasks", "nextsteps"))
	out.Chapters = chaptersOf(first(fields, "chapters", "sections", "topics"))
	if out.Summary == "" {
		return out, fmt.Errorf("%w (it said: %s)", errNoAnswer, clip(oneLine(text), 200))
	}
	fill(&out)
	return out, nil
}

func fill(x *model.RecordingSummary) {
	if x.KeyPoints == nil {
		x.KeyPoints = []string{}
	}
	if x.Decisions == nil {
		x.Decisions = []string{}
	}
	if x.ActionItems == nil {
		x.ActionItems = []model.SummaryItem{}
	}
	if x.Chapters == nil {
		x.Chapters = []model.SummaryChapter{}
	}
}

// findObject finds the JSON object in text: the first '{' up to its matching '}' (strings
// respected), decoded; a trailing comma is forgiven.
func findObject(text string) (map[string]any, bool) {
	for start := strings.IndexByte(text, '{'); start >= 0; {
		end := matchBrace(text, start)
		if end < 0 {
			end = strings.LastIndexByte(text, '}') // cut off or unbalanced: try to the last one
		}
		if end > start {
			cand := text[start : end+1]
			for _, c := range []string{cand, dropTrailingCommas(cand)} {
				var m map[string]any
				if json.Unmarshal([]byte(c), &m) == nil {
					return m, true
				}
			}
		}
		next := strings.IndexByte(text[start+1:], '{')
		if next < 0 {
			break
		}
		start += 1 + next
	}
	return nil, false
}

// matchBrace returns the index of the '}' that closes the '{' at start, or -1.
func matchBrace(s string, start int) int {
	depth, in, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case in && esc:
			esc = false
		case in && c == '\\':
			esc = true
		case in && c == '"':
			in = false
		case in:
		case c == '"':
			in = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// dropTrailingCommas removes a ',' before '}' or ']' outside strings.
func dropTrailingCommas(s string) string {
	var b strings.Builder
	in, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in {
			b.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
			}
			continue
		}
		if c == '"' {
			in = true
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\r' || s[j] == '\t') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// normKey: "Key Points", "key_points", "keyPoints" → "keypoints".
func normKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func first(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

// textOf is a string, or a list of strings joined, or an object's text.
func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return clean(x)
	case []any:
		var parts []string
		for _, e := range x {
			if t := textOf(e); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		for _, k := range []string{"text", "what", "title", "point", "summary", "description", "content"} {
			for kk, vv := range x {
				if normKey(kk) == k {
					if t := textOf(vv); t != "" {
						return t
					}
				}
			}
		}
	case float64:
		return clean(fmt.Sprint(x))
	}
	return ""
}

func stringsOf(v any) []string {
	var list []any
	switch x := v.(type) {
	case []any:
		list = x
	case string:
		for _, l := range strings.Split(x, "\n") {
			list = append(list, l)
		}
	}
	out := []string{}
	for _, e := range list {
		if t := clip(textOf(e), maxItemRunes); t != "" && len(out) < maxListItems {
			out = append(out, t)
		}
	}
	return out
}

func itemsOf(v any) []model.SummaryItem {
	var list []any
	switch x := v.(type) {
	case []any:
		list = x
	case string:
		for _, l := range strings.Split(x, "\n") {
			list = append(list, l)
		}
	}
	out := []model.SummaryItem{}
	for _, e := range list {
		var it model.SummaryItem
		switch x := e.(type) {
		case string:
			it.What = clean(x)
		case map[string]any:
			f := map[string]any{}
			for k, v := range x {
				f[normKey(k)] = v
			}
			it.Who = clip(textOf(first(f, "who", "owner", "assignee", "person", "by")), 80)
			it.What = textOf(first(f, "what", "task", "item", "action", "text", "description", "todo"))
		}
		it.What = clip(it.What, maxItemRunes)
		if strings.EqualFold(it.Who, "unknown") || strings.EqualFold(it.Who, "unclear") || it.Who == "?" {
			it.Who = ""
		}
		if it.What != "" && len(out) < maxListItems {
			out = append(out, it)
		}
	}
	return out
}

func chaptersOf(v any) []model.SummaryChapter {
	list, _ := v.([]any)
	out := []model.SummaryChapter{}
	for _, e := range list {
		x, ok := e.(map[string]any)
		if !ok {
			continue
		}
		f := map[string]any{}
		for k, v := range x {
			f[normKey(k)] = v
		}
		title := clip(textOf(first(f, "title", "name", "topic", "heading", "text")), 120)
		ms, ok := msOf(f)
		if title == "" || !ok {
			continue
		}
		if len(out) < maxChapters {
			out = append(out, model.SummaryChapter{TMS: ms, Title: title})
		}
	}
	return out
}

// msOf reads a chapter's time: t_ms / ms / start_ms in milliseconds, or t / time / start / at
// as "m:ss" (a bare number there is seconds).
func msOf(f map[string]any) (int64, bool) {
	for _, k := range []string{"tms", "ms", "startms", "timems"} {
		if n, ok := f[k].(float64); ok && n >= 0 && n < math.MaxInt32*1000.0 {
			return int64(n), true
		}
	}
	for _, k := range []string{"t", "time", "start", "at", "timestamp", "from", "starttime"} {
		switch x := f[k].(type) {
		case string:
			if ms, ok := parseStamp(x); ok {
				return ms, true
			}
		case float64:
			if x >= 0 && x < math.MaxInt32 {
				return int64(x * 1000), true
			}
		}
	}
	return 0, false
}

// clean trims a text, joins its whitespace and drops a list marker at its start.
func clean(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for _, p := range []string{"- ", "* ", "• ", "– "} {
		s = strings.TrimPrefix(s, p)
	}
	return strings.TrimSpace(s)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
