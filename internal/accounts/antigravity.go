package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Antigravity's CLI, agy. UNVERIFIED: agy was not installed where this was written, so all of
// this follows its documentation (antigravity.google/docs/cli) and third-party guides, and is
// kept in small functions that are easy to correct:
//   - state lives under ~/.gemini/antigravity-cli/ (never read here);
//   - sign-in is a browser OAuth on first run; guides mention `agy auth login | status | logout`,
//     the official reference only the TUI's /logout;
//   - the quota is the TUI's /usage (alias /quota); print mode (`-p … --output-format json`)
//     expands slash commands unless --disable-slash-commands;
//   - a statusline command receives {"quota": {"<bucket>": {"remaining_fraction", "reset_time",
//     "reset_in_seconds"}}} on stdin, the shape parseAgyUsage looks for first.
//
// VERIFIED on a real install (Windows, 17 Nov 2026):
//   - agy has no `auth` subcommand: `agy auth --help` prints the main usage;
//   - `agy -p /usage --output-format json` answers {"status":"SUCCESS","response": "..."} whose
//     response is one TAB-separated line per limit: model group, limit name, "N%", reset time
//     (RFC 3339), e.g. "Gemini Models<TAB>Weekly Limit Remaining<TAB>100%<TAB>2026-11-24T13:57:20Z";
//   - its home is <home>/.gemini/ (google_accounts.json, settings.json, state.json), with
//     antigravity-cli/ beneath it; with no auth command, google_accounts.json is the only cheap
//     sign that an account is signed in (agySignedIn).

const agyNoQuota = "Antigravity didn't report its quota here. Run /usage in Antigravity to see it."

// agyProbe is what one agy executable is known to support.
type agyProbe struct {
	key         string // path and modification time: a new agy is probed again
	authChecked bool
	hasAuth     bool
	// usageBroken: print-mode /usage gave no quota once. It is not asked again: if this agy
	// does not expand the command, every refresh would send "/usage" to the model.
	usageBroken bool
}

func (s *Service) agyProbe(path string, fn func(p *agyProbe)) agyProbe {
	key := path
	if st, err := os.Stat(path); err == nil {
		key += "|" + st.ModTime().String()
	}
	s.agyMu.Lock()
	defer s.agyMu.Unlock()
	if s.agy.key != key {
		s.agy = agyProbe{key: key}
	}
	if fn != nil {
		fn(&s.agy)
	}
	return s.agy
}

var agyAuthHelpRx = regexp.MustCompile(`(?im)^\s*(agy\s+auth\s+)?login\b`)

// agyHasAuth: does this agy have the `auth` subcommand (login, status, logout)?
func (s *scope) agyHasAuth(ctx context.Context, path string) bool {
	if p := s.agyProbe(path, nil); p.authChecked {
		return p.hasAuth
	}
	r := s.run(ctx, min(s.o.CheckTimeout, 10*time.Second), path, "auth", "--help")
	has := r.err == nil && r.exitCode == 0 && agyAuthHelpRx.MatchString(string(r.out)+"\n"+r.errText)
	s.agyProbe(path, func(p *agyProbe) { p.authChecked, p.hasAuth = true, has })
	return has
}

// agyForget drops what was learned about the agy executable (the auth probe and the "print
// mode gives no quota" latch): after a sign-in or sign-out its answers may be different.
func (s *Service) agyForget() {
	s.agyMu.Lock()
	s.agy = agyProbe{}
	s.agyMu.Unlock()
}

// agyHome is agy's home folder for s: the account's (HomeEnv moves HOME and USERPROFILE), else
// the user's.
func (s *scope) agyHome() string {
	for i := len(s.home) - 1; i >= 0; i-- {
		for _, k := range []string{"USERPROFILE=", "HOME="} {
			if v, ok := strings.CutPrefix(s.home[i], k); ok && v != "" {
				return v
			}
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// agySignedIn: agy keeps the signed-in Google account in <home>/.gemini/google_accounts.json
// (the file Gemini CLI uses too). Signed in = the file is there and not empty and, where it has
// an "active" entry, that entry names an account. The card shows that address (agyActiveEmail);
// nothing else of the file is kept or shown, and nothing is logged.
func agySignedIn(s *scope) bool {
	home := s.agyHome()
	if home == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "google_accounts.json"))
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return false
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(b, &f) != nil {
		return true // not the shape known from Gemini CLI: present and not empty is all we know
	}
	active, has := f["active"]
	if !has {
		return len(f) > 0
	}
	var email string
	return json.Unmarshal(active, &email) == nil && email != ""
}

// agyActiveEmail is the address of the Google account agy is signed in with: the "active"
// entry of its sign-in file, when that is an e-mail address. It is shown on the account's card
// like Claude's and Codex's; nothing else of the file is read out, and nothing is logged.
func agyActiveEmail(s *scope) string {
	home := s.agyHome()
	if home == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "google_accounts.json"))
	if err != nil {
		return ""
	}
	var f struct {
		Active string `json:"active"`
	}
	if json.Unmarshal(b, &f) != nil {
		return ""
	}
	if a := strings.TrimSpace(f.Active); len(a) <= 254 && emailRx.FindString(a) == a {
		return a
	}
	return ""
}

// agyByFile is the account as far as the sign-in file tells, for an agy that says nothing
// usable itself.
func agyByFile(s *scope) result {
	if !agySignedIn(s) {
		return result{known: true}
	}
	return result{known: true, connected: true, id: Identity{Method: MethodSubscription}, note: agyNoQuota}
}

func agyLogin(ctx context.Context, s *scope, path string) []string {
	if s.agyHasAuth(ctx, path) {
		return []string{"auth", "login"}
	}
	return nil // agy's own first-run sign-in
}

func agyLogout(ctx context.Context, s *scope, path string) []string {
	if s.agyHasAuth(ctx, path) {
		return []string{"auth", "logout"}
	}
	return []string{"-p", "/logout"}
}

// agyCheck is the account's state, with the signed-in Google address from agy's sign-in file
// where agy itself named none (its status and usage output often do not).
func agyCheck(ctx context.Context, s *scope, path string) result {
	res := agyState(ctx, s, path)
	if res.connected && res.id.Email == "" {
		res.id.Email = agyActiveEmail(s)
	}
	return res
}

func agyState(ctx context.Context, s *scope, path string) result {
	var res result
	authKnown := false
	if s.agyHasAuth(ctx, path) {
		if r := s.run(ctx, s.o.CheckTimeout, path, "auth", "status"); r.err == nil {
			res, authKnown = parseAgyStatus(r.text())
		}
	}
	if authKnown && !res.connected {
		return res
	}
	if s.agyQuotaOnDemand() && !s.measureAgyQuota {
		if !authKnown {
			res = agyByFile(s)
		}
		if res.connected {
			res.note, res.keepLimits = "Quota not measured automatically.", true
		}
		s.log.Info("Antigravity quota probe skipped", "background_usage", false)
		return res
	}
	if !s.measureAgyQuota && s.agyProbe(path, nil).usageBroken {
		if !authKnown {
			return agyByFile(s)
		}
		res.note = agyNoQuota
		return res
	}
	u := s.runHeadlessAgyQuota(ctx, path)
	if u.err != nil {
		if runtime.GOOS == "windows" {
			s.log.Info("Antigravity quota probe", "updater_opt_out", true, "result", "execution_failed")
		}
		if !authKnown {
			res = agyByFile(s)
			if !res.connected {
				return result{err: "Couldn't read usage: " + u.err.Error()}
			}
			res.id.Email = agyActiveEmail(s)
		}
		res.note, res.keepLimits = "Quota unavailable: "+u.err.Error(), authKnown || res.id.Email != ""
		return res
	}
	limits, signedOut, ok := parseAgyUsage(append(append([]byte{}, u.out...), u.errText...), s.o.Now())
	if runtime.GOOS == "windows" {
		s.log.Info("Antigravity quota probe", "updater_opt_out", true, "valid_quota", ok, "signed_out", signedOut, "quota_rows", len(limits))
	}
	switch {
	case ok:
		if !authKnown {
			res = result{known: true, connected: true, id: Identity{Method: MethodSubscription}}
		}
		res.limits = limits
	case signedOut && !authKnown:
		return result{known: true}
	default:
		s.agyProbe(path, func(p *agyProbe) { p.usageBroken = true })
		if !authKnown {
			return agyByFile(s)
		}
		res.note = agyNoQuota
	}
	return res
}

// agyUsageEnv disables the CLI's documented auto-update in the Windows quota
// probe. Its updater can start a new console for agy --version.
func agyUsageEnv() []string {
	if runtime.GOOS == "windows" {
		return []string{"AGY_CLI_DISABLE_AUTO_UPDATE=true"}
	}
	return nil
}

var (
	emailRx     = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)
	signedOutRx = regexp.MustCompile(`(?i)\bnot\s+(logged|signed)\s+in\b|\b(logged|signed)\s+out\b|\bunauthenticated\b|\bnot\s+authenticated\b|\bplease\s+(log|sign)\s*in\b|\b(log|sign)\s*in\s+(required|first)\b|\bno\s+account\b`)
	signedInRx  = regexp.MustCompile(`(?i)\b(logged|signed)\s+in\b|\bauthenticated\b`)
	planRx      = regexp.MustCompile(`(?im)\b(?:plan|tier|subscription)\s*[:=]\s*([A-Za-z][A-Za-z0-9 +]*?)\s*$`)
)

// parseAgyStatus reads `agy auth status` (JSON or text). ok: it said whether agy is signed in.
func parseAgyStatus(text string) (result, bool) {
	var obj map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &obj) == nil {
		in, found := false, false
		for _, k := range []string{"loggedIn", "logged_in", "signedIn", "signed_in", "authenticated", "isLoggedIn"} {
			if b, ok := obj[k].(bool); ok {
				in, found = b, true
				break
			}
		}
		if found {
			res := result{known: true, connected: in}
			if in {
				res.id = Identity{Method: MethodSubscription, Email: jsonString(obj, "email", "account", "user"),
					Plan: humanPlan(jsonString(obj, "plan", "tier", "subscription", "subscriptionType"))}
			}
			return res, true
		}
	}
	switch {
	case signedOutRx.MatchString(text):
		return result{known: true}, true
	case signedInRx.MatchString(text):
		id := Identity{Method: MethodSubscription, Email: emailRx.FindString(text)}
		if m := planRx.FindStringSubmatch(text); m != nil {
			id.Plan = humanPlan(m[1])
		}
		if strings.Contains(strings.ToLower(text), "api key") {
			id.Method = MethodAPIKey
		}
		return result{known: true, connected: true, id: id}, true
	}
	return result{}, false
}

// jsonString is the first of keys that holds a string (or an object with an "email").
func jsonString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := obj[k].(type) {
		case string:
			return v
		case map[string]any:
			if e, ok := v["email"].(string); ok {
				return e
			}
		}
	}
	return ""
}

var (
	agyLineRx   = regexp.MustCompile(`(?i)^\s*[^\pL\pN]*?([\pL\pN][^:]*?)\s*:\s*(\d+(?:\.\d+)?)\s*%\s*(used|remaining|left)\b(.*)$`)
	agyResetsRx = regexp.MustCompile(`(?i)\bresets?\s+(?:at\s+)?(.+?)\s*\)?\s*$`)
)

// parseAgyUsage reads what print-mode /usage printed: a "quota" object anywhere in its JSON
// (the statusline's shape), else "<bucket>: <n>% used|remaining" lines in its text. signedOut:
// it asked to sign in.
func parseAgyUsage(out []byte, now time.Time) (limits []Limit, signedOut, ok bool) {
	var texts []string
	for _, doc := range jsonDocs(out) {
		if q := findQuota(doc); q != nil {
			if l := quotaLimits(q, now); len(l) > 0 {
				return l, false, true
			}
		}
		texts = append(texts, jsonTexts(doc)...)
	}
	if len(texts) == 0 {
		texts = []string{string(out)}
	}
	text := ansiRx.ReplaceAllString(strings.Join(texts, "\n"), "")
	if l := agyTabLimits(text); len(l) > 0 {
		return l, false, true
	}
	for _, line := range strings.Split(text, "\n") {
		m := agyLineRx.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pct, okp := parsePercent(m[2])
		if !okp {
			continue
		}
		if w := strings.ToLower(m[3]); w == "remaining" || w == "left" {
			pct = clampPercent(100 - pct)
		}
		label := strings.TrimSpace(m[1])
		l := Limit{ID: slug(label), Label: label, UsedPercent: pct}
		if r := agyResetsRx.FindStringSubmatch(m[4]); r != nil {
			var at time.Time
			l.ResetsText, at = parseResets(r[1], now)
			if !at.IsZero() {
				l.ResetsAt = at.UnixMilli()
			}
		}
		limits = append(limits, l)
	}
	if len(limits) > 0 {
		return limits, false, true
	}
	return nil, signedOutRx.MatchString(text), false
}

var agyTabPercentRx = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*%$`)

// agyTabLimits reads the real /usage answer: one line per limit, TAB-separated: model group,
// limit name, "N%", reset time (RFC 3339; may be missing). agy reports what is LEFT ("Weekly
// Limit Remaining"); a limit name that says "used" is read as used. Lines of another shape are
// skipped.
func agyTabLimits(text string) []Limit {
	var out []Limit
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		m := agyTabPercentRx.FindStringSubmatch(f[2])
		if m == nil || f[0] == "" || f[1] == "" {
			continue
		}
		pct, ok := parsePercent(m[1])
		if !ok {
			continue
		}
		name := f[1]
		if !strings.Contains(strings.ToLower(name), "used") {
			pct = clampPercent(100 - pct) // "remaining", or no word: agy's /usage counts down
		}
		for _, w := range []string{" Remaining", " remaining", " Used", " used"} {
			name = strings.TrimSuffix(name, w)
		}
		label := name // "Gemini 4 Argon Weekly Limit" already names its group
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(f[0])) {
			label = f[0] + " · " + name
		}
		l := Limit{ID: slug(label), Label: label, UsedPercent: pct}
		if l.ID == "" || seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		if len(f) > 3 {
			if t, err := time.Parse(time.RFC3339Nano, f[3]); err == nil {
				l.ResetsAt = t.UnixMilli()
			}
		}
		out = append(out, l)
	}
	return out
}

// jsonDocs reads out as one JSON value, or as one per line.
func jsonDocs(out []byte) []any {
	var v any
	if json.Unmarshal(bytes.TrimSpace(out), &v) == nil {
		return []any{v}
	}
	var docs []any
	for _, l := range bytes.Split(out, []byte("\n")) {
		if json.Unmarshal(bytes.TrimSpace(l), &v) == nil {
			docs = append(docs, v)
		}
	}
	return docs
}

// findQuota finds a "quota" object of buckets anywhere in v.
func findQuota(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if q, ok := val.(map[string]any); ok && strings.EqualFold(k, "quota") {
				return q
			}
		}
		for _, val := range x {
			if q := findQuota(val); q != nil {
				return q
			}
		}
	case []any:
		for _, val := range x {
			if q := findQuota(val); q != nil {
				return q
			}
		}
	}
	return nil
}

// jsonTexts collects the strings under the keys a print-mode answer keeps its text in.
func jsonTexts(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for _, k := range []string{"result", "response", "text", "output", "content", "message"} {
			if s, ok := x[k].(string); ok {
				out = append(out, s)
			}
		}
		for _, val := range x {
			if _, ok := val.(string); !ok {
				out = append(out, jsonTexts(val)...)
			}
		}
	case []any:
		for _, val := range x {
			out = append(out, jsonTexts(val)...)
		}
	}
	return out
}

// quotaLimits: {"<bucket>": {"remaining_fraction", "reset_time", "reset_in_seconds"}} → limits,
// buckets in name order.
func quotaLimits(q map[string]any, now time.Time) []Limit {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Limit
	for _, k := range keys {
		b, ok := q[k].(map[string]any)
		if !ok {
			continue
		}
		frac, ok := number(b, "remaining_fraction", "remainingFraction")
		if !ok {
			continue
		}
		l := Limit{ID: slug(k), Label: bucketLabel(k), UsedPercent: clampPercent((1 - frac) * 100)}
		if s, ok := b["reset_time"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				l.ResetsAt = t.UnixMilli()
			}
		} else if s, ok := b["resetTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				l.ResetsAt = t.UnixMilli()
			}
		}
		if l.ResetsAt == 0 {
			if secs, ok := number(b, "reset_in_seconds", "resetInSeconds"); ok && secs > 0 {
				l.ResetsAt = now.Add(time.Duration(secs * float64(time.Second))).UnixMilli()
			}
		}
		if l.ID == "" {
			l.ID = "quota"
		}
		out = append(out, l)
	}
	return out
}

func number(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return f, true
		}
	}
	return 0, false
}

// bucketLabel: "gemini-3-pro" → "Gemini 3 Pro".
func bucketLabel(k string) string {
	words := strings.FieldsFunc(k, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	if len(words) == 0 {
		return k
	}
	return strings.Join(words, " ")
}
