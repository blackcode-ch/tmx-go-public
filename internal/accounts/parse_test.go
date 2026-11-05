package accounts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/accounts/accountstest"
)

func karachi(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Karachi")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	return loc
}

func TestParseClaudeStatus(t *testing.T) {
	st, ok := parseClaudeStatus([]byte(accountstest.ClaudeStatusMax))
	if !ok || !st.LoggedIn {
		t.Fatalf("max: %+v %v", st, ok)
	}
	if id := st.identity(); id != (Identity{Email: "me@example.com", Plan: "Max", Method: MethodSubscription, Org: "Example Org"}) {
		t.Fatalf("identity %+v", id)
	}
	st, ok = parseClaudeStatus([]byte("warning: something\n" + accountstest.ClaudeStatusAPIKey))
	if !ok || st.method() != MethodAPIKey || st.identity().Plan != "" {
		t.Fatalf("api key: %+v %v", st, ok)
	}
	if st, ok = parseClaudeStatus([]byte(accountstest.ClaudeStatusOut)); !ok || st.LoggedIn {
		t.Fatalf("logged out: %+v %v", st, ok)
	}
	for _, bad := range []string{"", "garbage", `{"foo":1}`, `[1,2]`, "Error: unknown command 'auth'"} {
		if _, ok := parseClaudeStatus([]byte(bad)); ok {
			t.Errorf("%q read as a status", bad)
		}
	}
	for _, c := range []struct {
		st   claudeStatus
		want string
	}{
		{claudeStatus{AuthMethod: "oauth_token"}, MethodSubscription},
		{claudeStatus{AuthMethod: "console"}, MethodAPIKey},
		{claudeStatus{AuthMethod: "claude.ai", APIProvider: "bedrock"}, MethodAPIKey},
		{claudeStatus{SubscriptionType: "pro"}, MethodSubscription},
		{claudeStatus{AuthMethod: "something"}, MethodUnknown},
	} {
		if got := c.st.method(); got != c.want {
			t.Errorf("%+v: %s, want %s", c.st, got, c.want)
		}
	}
}

func TestParseClaudeUsage(t *testing.T) {
	loc := karachi(t)
	now := time.Date(2026, 10, 29, 17, 0, 0, 0, loc)
	limits, apiKey := parseClaudeUsage(accountstest.ClaudeUsageText, now)
	if apiKey || len(limits) != 3 {
		t.Fatalf("%v %+v", apiKey, limits)
	}
	want := []Limit{
		{ID: "session", Label: "Current session", UsedPercent: 8, ResetsText: "Oct 29 at 9:10pm",
			ResetsAt: time.Date(2026, 10, 29, 21, 10, 0, 0, loc).UnixMilli()},
		{ID: "week", Label: "Current week (all models)", UsedPercent: 54, ResetsText: "Nov 3 at 6am",
			ResetsAt: time.Date(2026, 11, 3, 6, 0, 0, 0, loc).UnixMilli()},
		{ID: "week_fable", Label: "Current week (Fable)", UsedPercent: 6, ResetsText: "Nov 3 at 6am",
			ResetsAt: time.Date(2026, 11, 3, 6, 0, 0, 0, loc).UnixMilli()},
	}
	for i := range want {
		if limits[i] != want[i] {
			t.Errorf("limit %d: %+v, want %+v", i, limits[i], want[i])
		}
	}
	// The zone in brackets wins over the local one.
	if got := limits[0].ResetsAt; got != time.Date(2026, 10, 29, 16, 10, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("session resets at %v", time.UnixMilli(got).UTC())
	}

	// No reset text, a decimal, an unknown zone, colors, a box border.
	limits, _ = parseClaudeUsage("\x1b[1mCurrent session\x1b[0m: 0% used\n│ Extra usage: 12.5% used · resets Dec 1 (Mars/Olympus)\n", now)
	if len(limits) != 2 || limits[0] != (Limit{ID: "session", Label: "Current session"}) {
		t.Fatalf("%+v", limits)
	}
	if l := limits[1]; l.ID != "extra_usage" || l.UsedPercent != 12.5 || l.ResetsText != "Dec 1" ||
		l.ResetsAt != time.Date(2026, 12, 1, 0, 0, 0, 0, time.Local).UnixMilli() {
		t.Errorf("extra: %+v", l)
	}

	// An API-key login has no such lines.
	for _, text := range []string{
		"You are currently using API usage billing. /usage shows plan limits for Claude subscriptions only.",
		"/usage is only available for subscription plans.",
	} {
		if limits, apiKey := parseClaudeUsage(text, now); len(limits) != 0 || !apiKey {
			t.Errorf("%q: %v %+v", text, apiKey, limits)
		}
	}
	// Garbage: nothing, and not an API key.
	for _, text := range []string{"", "Unknown command: /usage", "You are currently using your subscription to power your Claude Code usage", "50% of the time it works"} {
		if limits, apiKey := parseClaudeUsage(text, now); len(limits) != 0 || apiKey {
			t.Errorf("%q: %v %+v", text, apiKey, limits)
		}
	}
}

func TestClaudeResultText(t *testing.T) {
	if s, ok := claudeResultText([]byte(accountstest.ClaudeUsage("a\n\"b\""))); !ok || s != "a\n\"b\"" {
		t.Fatalf("%q %v", s, ok)
	}
	if s, ok := claudeResultText([]byte(`[{"type":"system"},{"type":"result","result":"x"}]`)); !ok || s != "x" {
		t.Fatalf("array: %q %v", s, ok)
	}
	if _, ok := claudeResultText([]byte("not json")); ok {
		t.Fatal("garbage read")
	}
}

func TestParseWhen(t *testing.T) {
	loc := karachi(t)
	now := time.Date(2026, 12, 30, 22, 0, 0, 0, loc)
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"Jan 2 at 6am", at(2027, 1, 2, 6, 0)}, // next year
		{"Dec 30 at 11:30pm", at(2026, 12, 30, 23, 30)},
		{"Dec 31, 2026 at 9pm", at(2026, 12, 31, 21, 0)},
		{"11pm", at(2026, 12, 30, 23, 0)},
		{"9:10pm", at(2026, 12, 31, 21, 10)}, // passed today: tomorrow
		{"Thu at 6am", at(2026, 12, 31, 6, 0)},
		{"Wed at 6am", at(2027, 1, 6, 6, 0)}, // today is a Wednesday, 6am has passed
		{"in 2h 13m", now.Add(2*time.Hour + 13*time.Minute)},
		{"in 3 days", now.Add(72 * time.Hour)},
		{"soon", time.Time{}},
		{"in a while", time.Time{}},
	} {
		if got := parseWhen(c.in, now); !got.Equal(c.want) {
			t.Errorf("%q: %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHumanPlan(t *testing.T) {
	for in, want := range map[string]string{"max": "Max", "pro": "Pro", "plus": "Plus", "team": "Team", "max_20x": "Max 20x",
		"prolite": "Pro Lite", "unknown": "", "": "", "enterprise_cbp_usage_based": "Enterprise", "new_thing": "New Thing"} {
		if got := humanPlan(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestCodexLimits(t *testing.T) {
	limits := codexLimits(mustCodexLimits(t, accountstest.CodexLimits), time.Now())
	want := []Limit{
		{ID: "5h", Label: "5-hour limit", UsedPercent: 12.5, ResetsAt: 1730000000000},
		{ID: "week", Label: "Weekly limit", UsedPercent: 40, ResetsAt: 1730500000000},
	}
	if len(limits) != 2 || limits[0] != want[0] || limits[1] != want[1] {
		t.Fatalf("%+v", limits)
	}
	// Another bucket is listed after, named after itself.
	rl := mustCodexLimits(t, `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":1,"windowDurationMins":300,"resetsAt":null}},
		"rateLimitsByLimitId":{"codex":{"limitId":"codex","primary":{"usedPercent":1}},
		"codex_other":{"limitId":"codex_other","limitName":"GPT-5.5 Pro","secondary":{"usedPercent":99.96,"windowDurationMins":1440}}}}`)
	limits = codexLimits(rl, time.Now())
	if len(limits) != 2 || limits[1] != (Limit{ID: "codex_other_day", Label: "GPT-5.5 Pro · Daily limit", UsedPercent: 100}) {
		t.Fatalf("buckets: %+v", limits)
	}
	if limits[0].ResetsAt != 0 {
		t.Errorf("null resetsAt: %+v", limits[0])
	}
}

func mustCodexLimits(t *testing.T, s string) codexRateLimits {
	t.Helper()
	var rl codexRateLimits
	if err := json.Unmarshal([]byte(s), &rl); err != nil {
		t.Fatal(err)
	}
	return rl
}

func TestCodexAccountAndLoginStatus(t *testing.T) {
	email, plan := "me@example.com", "pro"
	r := codexAccountResult(&codexAccount{Type: "chatgpt", Email: &email, PlanType: &plan})
	if !r.connected || r.id != (Identity{Email: email, Plan: "Pro", Method: MethodSubscription}) {
		t.Fatalf("%+v", r)
	}
	if r := codexAccountResult(&codexAccount{Type: "apiKey"}); r.id.Method != MethodAPIKey || !strings.Contains(r.note, "API key") {
		t.Fatalf("%+v", r)
	}
	if r := codexAccountResult(nil); !r.known || r.connected {
		t.Fatalf("%+v", r)
	}
	for in, want := range map[string]string{"Logged in using ChatGPT": MethodSubscription,
		"Logged in using an API key - sk-proj-***ABCD": MethodAPIKey, "Not logged in": ""} {
		r, ok := parseCodexLoginStatus(in)
		if !ok || r.id.Method != want || r.connected != (want != "") {
			t.Errorf("%q: %+v %v", in, r, ok)
		}
		if strings.Contains(r.note+r.id.Email, "sk-") {
			t.Errorf("%q: the key was kept", in)
		}
	}
	if _, ok := parseCodexLoginStatus("error: unexpected argument"); ok {
		t.Error("garbage read")
	}
}

// rollout writes a rollout file with lines into codexHome/sessions/<day>.
func rollout(t *testing.T, codexHome, day, name string, mod time.Time, lines ...string) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", day)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestCodexRolloutLimits(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	if _, _, ok := codexRolloutLimits(home, now); ok {
		t.Fatal("limits without rollouts")
	}
	meta := `{"timestamp":"2026-10-29T09:00:00Z","type":"session_meta","payload":{"id":"x","cwd":"/p"}}`
	old := `{"timestamp":"2026-10-28T09:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":90,"window_minutes":300,"resets_at":1793000000}}}}`
	rollout(t, home, "2026/10/28", "rollout-2026-10-28T09-00-00-a.jsonl", now.Add(-27*time.Hour), meta, old)
	// The newest: an older token_count with limits, then one without (an API-key turn).
	withLimits := `{"timestamp":"2026-10-29T11:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{},` +
		`"rate_limits":{"primary":{"used_percent":20.0,"window_minutes":300,"resets_in_seconds":7200},` +
		`"secondary":{"used_percent":55.5,"window_minutes":10080,"resets_at":1730000000}}}}`
	noLimits := `{"timestamp":"2026-10-29T11:30:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{},"rate_limits":null}}`
	rollout(t, home, "2026/10/29", "rollout-2026-10-29T09-00-00-b.jsonl", now.Add(-30*time.Minute), meta, withLimits, noLimits, "garbage{")
	limits, at, ok := codexRolloutLimits(home, now)
	if !ok || !at.Equal(time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)) || len(limits) != 2 {
		t.Fatalf("%v %v %+v", ok, at, limits)
	}
	if l := limits[0]; l != (Limit{ID: "5h", Label: "5-hour limit", UsedPercent: 20, ResetsAt: at.Add(2 * time.Hour).UnixMilli()}) {
		t.Errorf("primary %+v", l)
	}
	// resets_at in the past: that window has started over.
	if l := limits[1]; l != (Limit{ID: "week", Label: "Weekly limit"}) {
		t.Errorf("secondary %+v", l)
	}
}

func TestParseAgyUsage(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	limits, _, ok := parseAgyUsage([]byte(accountstest.AgyQuota), now)
	if !ok || len(limits) != 2 {
		t.Fatalf("%v %+v", ok, limits)
	}
	if l := limits[0]; l != (Limit{ID: "claude_sonnet", Label: "Claude Sonnet", UsedPercent: 50, ResetsAt: now.Add(2 * time.Hour).UnixMilli()}) {
		t.Errorf("%+v", l)
	}
	if l := limits[1]; l.ID != "gemini_3_pro" || l.UsedPercent != 6 || l.ResetsAt != time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli() {
		t.Errorf("%+v", l)
	}
	// The statusline's shape, nested; camelCase too.
	limits, _, ok = parseAgyUsage([]byte(`{"session":{"quota":{"flash":{"remainingFraction":0.25}}}}`), now)
	if !ok || len(limits) != 1 || limits[0].UsedPercent != 75 {
		t.Fatalf("nested: %+v", limits)
	}
	// Text in a print-mode answer.
	limits, _, ok = parseAgyUsage([]byte(`{"result":"Usage\n  Gemini 3 Pro: 94% remaining · resets in 3h\n  Flash: 10% used"}`), now)
	if !ok || len(limits) != 2 || limits[0].UsedPercent != 6 || limits[0].ResetsAt != now.Add(3*time.Hour).UnixMilli() || limits[1].UsedPercent != 10 {
		t.Fatalf("text: %+v", limits)
	}
	if _, out, ok := parseAgyUsage([]byte("Please sign in to continue"), now); ok || !out {
		t.Fatalf("signed out: %v %v", out, ok)
	}
	for _, bad := range []string{"", "Sure! Here is a poem about usage.", `{"result":"I can't run /usage"}`} {
		if l, out, ok := parseAgyUsage([]byte(bad), now); ok || out || len(l) > 0 {
			t.Errorf("%q: %+v %v %v", bad, l, out, ok)
		}
	}
}

func TestParseAgyStatus(t *testing.T) {
	r, ok := parseAgyStatus(accountstest.AgyStatusIn)
	if !ok || !r.connected || r.id != (Identity{Email: "me@example.com", Plan: "Pro", Method: MethodSubscription}) {
		t.Fatalf("%+v %v", r, ok)
	}
	if r, ok := parseAgyStatus(accountstest.AgyStatusOut); !ok || r.connected {
		t.Fatalf("%+v %v", r, ok)
	}
	if r, ok := parseAgyStatus(`{"loggedIn":true,"user":{"email":"a@b.co"},"tier":"ultra"}`); !ok || r.id.Email != "a@b.co" || r.id.Plan != "Ultra" {
		t.Fatalf("json: %+v %v", r, ok)
	}
	if _, ok := parseAgyStatus("whatever"); ok {
		t.Fatal("garbage read")
	}
}
