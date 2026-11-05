package harness

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Conversation is one saved conversation of a harness, as found on disk.
type Conversation struct {
	ID      string `json:"id"`
	Started int64  `json:"started"` // Unix ms of its first record; 0 = unknown
	Updated int64  `json:"updated"` // Unix ms of the file's last change
	Preview string `json:"preview"` // the first user message
	Path    string `json:"-"`
}

const (
	maxConversations = 15
	headRecords      = 60
	previewLen       = 160
)

var slugRx = regexp.MustCompile(`[^a-zA-Z0-9]`)

// resolve returns dir absolute with symlinks resolved (/tmp → /private/tmp on macOS), as
// the harnesses record it.
func resolve(dir string) string {
	p, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// ClaudeProjectDir is where Claude Code keeps the conversations of cwd:
// ~/.claude/projects/<cwd with every non-alphanumeric as "-", at most 200>.
func ClaudeProjectDir(home, cwd string) string {
	return ClaudeProjectDirIn(filepath.Join(home, ".claude"), cwd)
}

// ClaudeProjectDirIn is ClaudeProjectDir for a claude whose state folder is root (its
// CLAUDE_CONFIG_DIR, or ~/.claude).
func ClaudeProjectDirIn(root, cwd string) string {
	slug := slugRx.ReplaceAllString(resolve(cwd), "-")
	if len(slug) > 200 {
		slug = slug[:200]
	}
	return filepath.Join(root, "projects", slug)
}

// ClaudeTranscriptExists reports whether conversation id has a transcript for cwd (Claude
// writes it with the first message, so this is "the conversation exists").
func ClaudeTranscriptExists(home, cwd, id string) bool {
	return ClaudeTranscriptExistsIn(filepath.Join(home, ".claude"), cwd, id)
}

// ClaudeTranscriptExistsIn is ClaudeTranscriptExists for the state folder root.
func ClaudeTranscriptExistsIn(root, cwd, id string) bool {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return false
	}
	_, err := os.Stat(filepath.Join(ClaudeProjectDirIn(root, cwd), id+".jsonl"))
	return err == nil
}

// ClaudeConversations lists cwd's recent conversations, newest first.
func ClaudeConversations(home, cwd string) []Conversation {
	dir := ClaudeProjectDir(home, cwd)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Conversation
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		c := Conversation{ID: strings.TrimSuffix(e.Name(), ".jsonl"), Updated: fi.ModTime().UnixMilli(),
			Path: filepath.Join(dir, e.Name())}
		for _, rec := range headOf(c.Path, headRecords) {
			if c.Started == 0 {
				c.Started = stamp(rec["timestamp"])
			}
			if c.Preview == "" && rec["type"] == "user" {
				if m, ok := rec["message"].(map[string]any); ok {
					c.Preview = textOf(m["content"])
				}
			}
			if c.Started != 0 && c.Preview != "" {
				break
			}
		}
		c.Preview = clip(c.Preview)
		out = append(out, c)
	}
	return newest(out)
}

// CodexHome is $CODEX_HOME, else ~/.codex.
func CodexHome(home string) string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	return filepath.Join(home, ".codex")
}

// CodexConversations lists the rollouts of the last 14 days (or since, if later) recorded
// in cwd, newest first. Rollouts live in <codex home>/sessions/YYYY/MM/DD/rollout-*.jsonl
// and start with a session_meta record {id, timestamp, cwd} (verified with codex 0.163).
func CodexConversations(codexHome, cwd string, since time.Time) []Conversation {
	want := strings.ToLower(resolve(cwd))
	if floor := time.Now().Add(-14 * 24 * time.Hour); since.Before(floor) {
		since = floor
	}
	var out []Conversation
	for _, f := range codexRollouts(codexHome, since) {
		meta, head := codexMeta(f.path)
		if meta.ID == "" || strings.ToLower(resolve(meta.Cwd)) != want {
			continue
		}
		c := Conversation{ID: meta.ID, Started: stamp(meta.Timestamp), Updated: f.mod.UnixMilli(), Path: f.path}
		for _, rec := range head {
			pl, _ := rec["payload"].(map[string]any)
			if pl == nil {
				continue
			}
			var text string
			switch {
			case pl["type"] == "user_message":
				text, _ = pl["message"].(string)
			case pl["type"] == "message" && pl["role"] == "user":
				text = textOf(pl["content"])
			}
			if text = strings.TrimSpace(text); text != "" && !strings.HasPrefix(text, "<") {
				c.Preview = clip(text)
				break
			}
		}
		out = append(out, c)
	}
	return newest(out)
}

// CodexFindByMarker returns the id of the one rollout in cwd, started since, whose developer
// instructions carry CodexLaunchMarker(launchID); "" if none or several.
func CodexFindByMarker(codexHome, cwd, launchID string, since time.Time) string {
	if launchID == "" {
		return ""
	}
	want := strings.ToLower(resolve(cwd))
	marker := CodexLaunchMarker(launchID)
	found := map[string]bool{}
	for _, f := range codexRollouts(codexHome, since.Add(-time.Minute)) {
		meta, head := codexMeta(f.path)
		if meta.ID == "" || strings.ToLower(resolve(meta.Cwd)) != want {
			continue
		}
		for _, rec := range head {
			pl, _ := rec["payload"].(map[string]any)
			if rec["type"] == "response_item" && pl != nil && pl["role"] == "developer" &&
				strings.Contains(allText(pl["content"]), marker) {
				found[meta.ID] = true
			}
		}
	}
	if len(found) != 1 {
		return ""
	}
	for id := range found {
		return id
	}
	return ""
}

// CodexRolloutExists reports whether a rollout of conversation id exists.
func CodexRolloutExists(codexHome, id string) bool {
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return false
	}
	m, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*-"+id+".jsonl"))
	return len(m) > 0
}

type rolloutFile struct {
	path string
	mod  time.Time
}

// codexRollouts lists the rollout files changed since, from the day folders that can hold them.
func codexRollouts(codexHome string, since time.Time) []rolloutFile {
	var out []rolloutFile
	seen := map[string]bool{}
	for t := time.Now().Add(24 * time.Hour); !t.Before(since.Add(-24 * time.Hour)); t = t.Add(-24 * time.Hour) {
		for _, d := range []time.Time{t, t.UTC()} {
			dir := filepath.Join(codexHome, "sessions", d.Format("2006"), d.Format("01"), d.Format("02"))
			if seen[dir] {
				continue
			}
			seen[dir] = true
			ents, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range ents {
				if !strings.HasPrefix(e.Name(), "rollout-") || !strings.HasSuffix(e.Name(), ".jsonl") {
					continue
				}
				if fi, err := e.Info(); err == nil && !fi.ModTime().Before(since) {
					out = append(out, rolloutFile{filepath.Join(dir, e.Name()), fi.ModTime()})
				}
			}
		}
	}
	return out
}

type codexSessionMeta struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

func codexMeta(path string) (codexSessionMeta, []map[string]any) {
	head := headOf(path, 80)
	var meta codexSessionMeta
	if len(head) == 0 || head[0]["type"] != "session_meta" {
		return meta, nil
	}
	b, _ := json.Marshal(head[0]["payload"])
	_ = json.Unmarshal(b, &meta)
	return meta, head[1:]
}

// headOf decodes the first n JSON lines of a file (bad lines skipped).
func headOf(path string, n int) []map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var out []map[string]any
	for i := 0; i < n && sc.Scan(); i++ {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func stamp(v any) int64 {
	s, _ := v.(string)
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// textOf is the first text part of message content that is not markup (<system-reminder>…).
func textOf(content any) string {
	switch c := content.(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		for _, p := range c {
			part, _ := p.(map[string]any)
			if part == nil || (part["type"] != "text" && part["type"] != "input_text") {
				continue
			}
			if t, _ := part["text"].(string); strings.TrimSpace(t) != "" && !strings.HasPrefix(strings.TrimSpace(t), "<") {
				return strings.TrimSpace(t)
			}
		}
	}
	return ""
}

func allText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	var b strings.Builder
	if parts, ok := content.([]any); ok {
		for _, p := range parts {
			if part, _ := p.(map[string]any); part != nil {
				t, _ := part["text"].(string)
				b.WriteString(t + "\n")
			}
		}
	}
	return b.String()
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > previewLen {
		return string(r[:previewLen-1]) + "…"
	}
	return s
}

func newest(cs []Conversation) []Conversation {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Updated > cs[j].Updated })
	if len(cs) > maxConversations {
		cs = cs[:maxConversations]
	}
	return cs
}
