package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

// Claude Code (verified with 2.1.311 on 29 Oct 2026):
//   - `claude auth status --json` → {"loggedIn", "authMethod", "apiProvider", "email", "orgId",
//     "orgName", "subscriptionType", …}: no network, no secret.
//   - `claude -p /usage --output-format json --no-session-persistence` → a result whose text has
//     "<label>: <n>% used · resets <when> (<IANA zone>)" lines; no model turn, no tokens.
//   - `claude auth login` signs in through the browser; `claude auth logout` signs out.

// claudeUsageArgs: --strict-mcp-config keeps the owner's MCP servers from starting for it.
var claudeUsageArgs = []string{"-p", "/usage", "--output-format", "json", "--no-session-persistence", "--strict-mcp-config"}

func claudeLogin(context.Context, *scope, string) []string  { return []string{"auth", "login"} }
func claudeLogout(context.Context, *scope, string) []string { return []string{"auth", "logout"} }

// claudeRun is s.run for claude: in the daemon's start queue (one claude start at a time per
// CLAUDE_CONFIG_DIR, claude.BeginStart), its turn held until it exited: it may write
// .claude.json until then. s.run bounds it (timeout, then the process tree is killed).
func (s *scope) claudeRun(ctx context.Context, timeout time.Duration, path string, args ...string) ran {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	exited := make(chan struct{})
	// s.run's own timeout ends it first; the backstop cancels it (killing the tree) and waits.
	ticket, err := claude.BeginStartHeld(ctx, s.env(), claude.HolderOther, timeout+claudeHoldSlack, func() { cancel(); <-exited })
	if err != nil {
		return ran{exitCode: -1, err: err}
	}
	defer ticket.Release() // s.run returns after Wait: the process has exited (or never started)
	defer close(exited)
	return s.run(runCtx, timeout, path, args...)
}

// claudeHoldSlack is how much longer than its own timeout a claude run may hold the start
// queue before the queue stops it: the kill, WaitDelay and reaping after the timeout.
const claudeHoldSlack = 30 * time.Second

func claudeCheck(ctx context.Context, s *scope, path string) result {
	r := s.claudeRun(ctx, s.o.CheckTimeout, path, "auth", "status", "--json")
	if r.err != nil {
		return result{err: "Couldn't run claude auth status: " + r.err.Error()}
	}
	st, ok := parseClaudeStatus(r.out)
	if !ok {
		return result{err: "Couldn't read claude auth status: " + orNothing(firstLine(r.text(), 160))}
	}
	res := result{known: true, connected: st.LoggedIn}
	if !st.LoggedIn {
		return res
	}
	res.id = st.identity()
	if res.id.Method == MethodAPIKey {
		res.note = claudeNoLimitsNote(st)
		return res
	}
	u := s.runUsage(ctx, path)
	if u.err != nil {
		res.note, res.keepLimits = "Couldn't read usage: "+u.err.Error(), true
		return res
	}
	text, ok := claudeResultText(u.out)
	if !ok {
		res.note, res.keepLimits = "Couldn't read usage: "+orNothing(firstLine(u.text(), 160)), true
		return res
	}
	limits, apiKey := parseClaudeUsage(text, s.o.Now())
	switch {
	case len(limits) > 0:
		res.limits = limits
	case apiKey:
		res.note = "Signed in with an API key: no plan limits"
	default:
		res.note, res.keepLimits = "Couldn't read usage: "+orNothing(firstLine(text, 160)), true
	}
	return res
}

// runUsage runs /usage; an older claude that does not know an optional flag runs without it.
func (s *scope) runUsage(ctx context.Context, path string) ran {
	args := append([]string(nil), claudeUsageArgs...)
	for range 2 {
		u := s.claudeRun(ctx, s.o.UsageTimeout, path, args...)
		if u.err != nil || u.exitCode == 0 {
			return u
		}
		m := unknownOptRx.FindStringSubmatch(u.errText + "\n" + string(u.out))
		if m == nil || (m[1] != "--strict-mcp-config" && m[1] != "--no-session-persistence") {
			return u
		}
		args = without(args, m[1])
	}
	return s.claudeRun(ctx, s.o.UsageTimeout, path, args...)
}

var unknownOptRx = regexp.MustCompile(`unknown option '(--[a-z-]+)'`)

func without(args []string, flag string) []string {
	out := args[:0:0]
	for _, a := range args {
		if a != flag {
			out = append(out, a)
		}
	}
	return out
}

// orNothing is what a harness printed, for a note: its first line, unless that is empty or
// broken JSON (not worth showing).
func orNothing(s string) string {
	switch {
	case s == "":
		return "it printed nothing"
	case strings.HasPrefix(s, "{") || strings.HasPrefix(s, "["):
		return "it printed something else"
	}
	return s
}

// claudeStatus is `claude auth status --json` (only what tmx shows; orgId is not kept).
type claudeStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	APIProvider      string `json:"apiProvider"`
	APIKeySource     string `json:"apiKeySource"`
	Email            string `json:"email"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
}

func parseClaudeStatus(out []byte) (claudeStatus, bool) {
	out = bytes.TrimSpace(out)
	if i := bytes.IndexByte(out, '{'); i > 0 { // a warning line before the JSON
		out = out[i:]
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(out, &raw) != nil {
		return claudeStatus{}, false
	}
	if _, ok := raw["loggedIn"]; !ok {
		return claudeStatus{}, false
	}
	var st claudeStatus
	if json.Unmarshal(out, &st) != nil {
		return claudeStatus{}, false
	}
	return st, true
}

func (st claudeStatus) identity() Identity {
	return Identity{Email: st.Email, Plan: humanPlan(st.SubscriptionType), Method: st.method(), Org: st.OrgName}
}

// method: a Claude subscription (claude.ai, or its long-lived OAuth token), or usage billed per
// token (an API key, the Console, or a cloud provider).
func (st claudeStatus) method() string {
	m := strings.ToLower(st.AuthMethod)
	p := strings.ToLower(st.APIProvider)
	switch {
	case p != "" && p != "firstparty":
		return MethodAPIKey
	case strings.Contains(m, "claude.ai") || strings.Contains(m, "oauth") || strings.Contains(m, "subscription"):
		return MethodSubscription
	case strings.Contains(m, "api") || strings.Contains(m, "key") || strings.Contains(m, "console") || st.APIKeySource != "":
		return MethodAPIKey
	case st.SubscriptionType != "":
		return MethodSubscription
	}
	return MethodUnknown
}

func claudeNoLimitsNote(st claudeStatus) string {
	switch strings.ToLower(st.APIProvider) {
	case "", "firstparty":
		return "Signed in with an API key: no plan limits"
	case "bedrock":
		return "Billed through Amazon Bedrock: no plan limits"
	case "vertex":
		return "Billed through Google Vertex AI: no plan limits"
	}
	return "Billed through " + st.APIProvider + ": no plan limits"
}

// claudeResultText is the text of `claude -p --output-format json`'s result (one object, an
// array of messages, or stream-json lines).
func claudeResultText(out []byte) (string, bool) {
	type res struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}
	out = bytes.TrimSpace(out)
	var r res
	if json.Unmarshal(out, &r) == nil && r.Type == "result" {
		return r.Result, true
	}
	var list []res
	if json.Unmarshal(out, &list) == nil {
		for i := len(list) - 1; i >= 0; i-- {
			if list[i].Type == "result" {
				return list[i].Result, true
			}
		}
	}
	lines := bytes.Split(out, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var l res
		if json.Unmarshal(bytes.TrimSpace(lines[i]), &l) == nil && l.Type == "result" {
			return l.Result, true
		}
	}
	return "", false
}

var (
	usageLineRx = regexp.MustCompile(`^\s*[^\pL\pN]*?([\pL\pN][^:]*?)\s*:\s*(\d+(?:\.\d+)?)\s*%\s*used\b(.*)$`)
	resetsRx    = regexp.MustCompile(`(?i)\bresets?\s+(.+?)\s*$`)
	ansiRx      = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
	apiKeyRx    = regexp.MustCompile(`(?i)\bapi[ -]?(key|usage|billing)\b|\bconsole\b|pay[ -]as[ -]you[ -]go|only (available )?for (claude )?(subscription|subscribers|pro|max)`)
)

// parseClaudeUsage reads /usage's "<label>: <n>% used[ · resets <when>]" lines. apiKey: the
// text says usage is billed per token (no plan limits).
func parseClaudeUsage(text string, now time.Time) (limits []Limit, apiKey bool) {
	text = ansiRx.ReplaceAllString(text, "")
	seen := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		m := usageLineRx.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		label := strings.TrimSpace(m[1])
		pct, ok := parsePercent(m[2])
		if !ok || label == "" {
			continue
		}
		l := Limit{ID: limitID(label), Label: label, UsedPercent: pct}
		if r := resetsRx.FindStringSubmatch(m[3]); r != nil {
			var at time.Time
			l.ResetsText, at = parseResets(r[1], now)
			if !at.IsZero() {
				l.ResetsAt = at.UnixMilli()
			}
		}
		if n := seen[l.ID]; n > 0 { // two lines with the same name: keep both
			seen[l.ID]++
			l.ID += "_" + itoa(n+1)
		} else {
			seen[l.ID] = 1
		}
		limits = append(limits, l)
	}
	if len(limits) == 0 && apiKeyRx.MatchString(text) && !strings.Contains(strings.ToLower(text), "using your subscription") {
		apiKey = true
	}
	return limits, apiKey
}

// limitID: "Current session" → session, "Current week (all models)" → week,
// "Current week (Opus)" → week_opus, anything else → its words joined by _.
func limitID(label string) string {
	l := strings.ToLower(strings.TrimSpace(label))
	l = strings.TrimPrefix(l, "current ")
	if inner, ok := strings.CutPrefix(l, "week ("); ok {
		inner = strings.TrimSuffix(inner, ")")
		if inner == "all models" || inner == "all" {
			return "week"
		}
		return "week_" + slug(inner)
	}
	if s := slug(l); s != "" {
		return s
	}
	return "limit"
}

func slug(s string) string {
	var b strings.Builder
	under := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			under = false
		} else if !under && b.Len() > 0 {
			b.WriteByte('_')
			under = true
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

var tzRx = regexp.MustCompile(`\s*\(([A-Za-z][A-Za-z0-9_+\-/]*)\)\s*$`)

// parseResets splits "Oct 29 at 9:10pm (Asia/Karachi)" into the text shown ("Oct 29 at
// 9:10pm") and the time it means in that zone (zero when it cannot be read). An unknown zone
// is read as the local one.
func parseResets(s string, now time.Time) (string, time.Time) {
	s = strings.TrimSpace(s)
	loc := time.Local
	if m := tzRx.FindStringSubmatchIndex(s); m != nil {
		if l, err := time.LoadLocation(s[m[2]:m[3]]); err == nil {
			loc = l
		}
		s = strings.TrimSpace(s[:m[0]])
	}
	return s, parseWhen(s, now.In(loc))
}

var (
	inRx      = regexp.MustCompile(`(?i)^in\s+(.+)$`)
	durPartRx = regexp.MustCompile(`(?i)(\d+)\s*(days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)
	weekdayRx = regexp.MustCompile(`(?i)^(mon|tue|wed|thu|fri|sat|sun)[a-z]*\.?\s+(.+)$`)
)

// parseWhen reads a reset time as Claude writes it, relative to now (in its zone): "Oct 29 at
// 9:10pm", "Nov 3 at 6am", "Oct 29, 2026 at 9pm", "9:10pm", "Mon at 6am", "in 2h 13m".
func parseWhen(s string, now time.Time) time.Time {
	s = strings.TrimSpace(s)
	if m := inRx.FindStringSubmatch(s); m != nil {
		var d time.Duration
		parts := durPartRx.FindAllStringSubmatch(m[1], -1)
		for _, p := range parts {
			n := time.Duration(atoi(p[1]))
			switch u := strings.ToLower(p[2]); {
			case strings.HasPrefix(u, "d"):
				d += n * 24 * time.Hour
			case strings.HasPrefix(u, "h"):
				d += n * time.Hour
			case strings.HasPrefix(u, "m"):
				d += n * time.Minute
			default:
				d += n * time.Second
			}
		}
		if len(parts) == 0 {
			return time.Time{}
		}
		return now.Add(d)
	}
	s = strings.ToLower(strings.ReplaceAll(s, ",", " "))
	s = strings.Join(strings.Fields(strings.ReplaceAll(" "+s+" ", " at ", " ")), " ")
	s = strings.ReplaceAll(strings.ReplaceAll(s, " am", "am"), " pm", "pm")
	loc := now.Location()
	for _, layout := range []string{"Jan 2 2006 3:04pm", "Jan 2 2006 3pm", "Jan 2 2006 15:04", "Jan 2 2006"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t
		}
	}
	for _, layout := range []string{"Jan 2 3:04pm", "Jan 2 3pm", "Jan 2 15:04", "Jan 2"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(now.Add(-24 * time.Hour)) { // "Jan 2" read in December
				t = t.AddDate(1, 0, 0)
			}
			return t
		}
	}
	day, next := now, 1 // a time that has passed today means tomorrow
	if m := weekdayRx.FindStringSubmatch(s); m != nil {
		ahead := (weekdayIndex(m[1]) - int(now.Weekday()) + 7) % 7
		day, next, s = now.AddDate(0, 0, ahead), 7, m[2] // … or that day next week
	}
	for _, layout := range []string{"3:04pm", "3pm", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			t = time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(now.Add(-time.Minute)) {
				t = t.AddDate(0, 0, next)
			}
			return t
		}
	}
	return time.Time{}
}

func weekdayIndex(s string) int {
	for i, d := range []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} {
		if strings.HasPrefix(strings.ToLower(s), d) {
			return i
		}
	}
	return 0
}

// installHint is how to install a harness (shown when it is missing).
func installHint(h model.Harness) string {
	switch h {
	case Claude:
		if runtime.GOOS == "windows" {
			return "irm https://claude.ai/install.ps1 | iex"
		}
		return "curl -fsSL https://claude.ai/install.sh | bash"
	case Antigravity:
		return "Install the Antigravity CLI (agy): antigravity.google/docs/cli"
	}
	if i, ok := harness.Get(h); ok {
		return i.Install
	}
	return ""
}
