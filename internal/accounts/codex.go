package accounts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// Codex (schema from `codex app-server generate-ts`, codex-cli 0.165.0; the not-signed-in
// answers checked against it on 29 Oct 2026):
//   - `codex app-server`: initialize → initialized → `account/read` {refreshToken: false} →
//     {account: null | {type: "chatgpt", email, planType} | {type: "apiKey"} | …,
//     requiresOpenaiAuth}; `account/rateLimits/read` → {rateLimits: {limitId, limitName,
//     primary: {usedPercent, windowDurationMins, resetsAt (Unix s)}, secondary, planType,
//     rateLimitReachedType}, rateLimitsByLimitId: {…}}. Without a sign-in rateLimits/read
//     answers error -32600 "codex account authentication required to read rate limits".
//   - Fallbacks: `codex login status` ("Logged in using ChatGPT" | "… an API key …" | "Not
//     logged in", exit 1), and the newest rollout's last token_count event, whose rate_limits
//     carry {used_percent, window_minutes, resets_at | resets_in_seconds}.
//   - `codex login` signs in through the browser (a local callback); `codex logout`.

func codexLogin(context.Context, *scope, string) []string  { return []string{"login"} }
func codexLogout(context.Context, *scope, string) []string { return []string{"logout"} }

func codexCheck(ctx context.Context, s *scope, path string) result {
	ans, err := s.codexAppServer(ctx, path)
	var res result
	if err == nil && ans.accountKnown {
		res = codexAccountResult(ans.account)
	} else {
		// An older Codex, or one whose app-server did not answer: its CLI says how it is signed in.
		st := s.run(ctx, s.o.CheckTimeout, path, "login", "status")
		if st.err != nil {
			return result{err: "Couldn't run codex: " + st.err.Error()}
		}
		var ok bool
		if res, ok = parseCodexLoginStatus(st.text()); !ok {
			why := "it printed nothing"
			if err != nil {
				why = err.Error()
			}
			return result{err: "Couldn't read Codex's sign-in: " + why}
		}
	}
	if !res.connected || res.id.Method != MethodSubscription {
		return res
	}
	if ans.limits != nil {
		res.limits = codexLimits(*ans.limits, s.o.Now())
		if res.id.Plan == "" && ans.limits.RateLimits.PlanType != nil {
			res.id.Plan = humanPlan(*ans.limits.RateLimits.PlanType)
		}
		if len(res.limits) > 0 {
			return res
		}
	}
	// Codex could not say: its last session's numbers.
	if limits, at, ok := codexRolloutLimits(s.codexHome(), s.o.Now()); ok {
		res.limits = limits
		res.note = "From your last Codex session, " + ago(s.o.Now().Sub(at))
		return res
	}
	switch {
	case ans.limitsErr != "":
		res.note = "Couldn't read usage: " + ans.limitsErr
	case err != nil:
		res.note = "Couldn't read usage: " + err.Error()
	default:
		res.note = "Codex reported no limits yet: use Codex once and refresh"
	}
	res.keepLimits = true
	return res
}

// codexAccountResult reads account/read's account (nil: not signed in).
func codexAccountResult(a *codexAccount) result {
	res := result{known: true, connected: a != nil}
	if a == nil {
		return res
	}
	switch a.Type {
	case "chatgpt":
		res.id = Identity{Method: MethodSubscription, Plan: humanPlan(deref(a.PlanType)), Email: deref(a.Email)}
	case "apiKey":
		res.id = Identity{Method: MethodAPIKey}
		res.note = "Signed in with an API key: no plan limits"
	case "amazonBedrock":
		res.id = Identity{Method: MethodAPIKey}
		res.note = "Billed through Amazon Bedrock: no plan limits"
	case "gcp":
		res.id = Identity{Method: MethodAPIKey}
		res.note = "Billed through Google Cloud: no plan limits"
	default:
		res.id = Identity{Method: MethodUnknown}
	}
	return res
}

// parseCodexLoginStatus reads `codex login status`. Only the method is kept: the line for an
// API key shows part of the key.
func parseCodexLoginStatus(text string) (result, bool) {
	l := strings.ToLower(text)
	switch {
	case strings.Contains(l, "not logged in") || strings.Contains(l, "not signed in"):
		return result{known: true}, true
	case strings.Contains(l, "logged in") && strings.Contains(l, "chatgpt"):
		return result{known: true, connected: true, id: Identity{Method: MethodSubscription}}, true
	case strings.Contains(l, "logged in") && (strings.Contains(l, "api key") || strings.Contains(l, "apikey")):
		return result{known: true, connected: true, id: Identity{Method: MethodAPIKey},
			note: "Signed in with an API key: no plan limits"}, true
	case strings.Contains(l, "logged in"):
		return result{known: true, connected: true, id: Identity{Method: MethodUnknown}}, true
	}
	return result{}, false
}

type codexAccount struct {
	Type     string  `json:"type"`
	Email    *string `json:"email"`
	PlanType *string `json:"planType"`
}

type codexWindow struct {
	UsedPercent        float64  `json:"usedPercent"`
	WindowDurationMins *float64 `json:"windowDurationMins"`
	ResetsAt           *float64 `json:"resetsAt"`
}

type codexSnapshot struct {
	LimitID   *string      `json:"limitId"`
	LimitName *string      `json:"limitName"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
	PlanType  *string      `json:"planType"`
}

type codexRateLimits struct {
	RateLimits          codexSnapshot             `json:"rateLimits"`
	RateLimitsByLimitID map[string]*codexSnapshot `json:"rateLimitsByLimitId"`
}

type codexAnswers struct {
	accountKnown bool
	account      *codexAccount // nil = not signed in
	limits       *codexRateLimits
	limitsErr    string
}

type codexMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (m codexMsg) id() string {
	var s string
	if json.Unmarshal(m.ID, &s) == nil {
		return s
	}
	return string(m.ID)
}

func codexLine(v any) []byte {
	b, _ := json.Marshal(v)
	return append(b, '\n')
}

// codexAppServer asks a short-lived `codex app-server` for the account and its rate limits.
func (s *scope) codexAppServer(ctx context.Context, path string) (codexAnswers, error) {
	var ans codexAnswers
	ctx, cancel := context.WithTimeout(ctx, s.o.CheckTimeout)
	defer cancel()
	cmd := hiddenexec.CommandContext(ctx, path, "app-server", "-c", "check_for_update_on_startup=false")
	cmd.Dir, cmd.Env = s.workDir(), s.env()
	setProcAttr(cmd)
	cmd.Cancel = func() error { killTree(cmd); return nil }
	cmd.WaitDelay = 2 * time.Second
	stderr := &capped{max: 4 << 10, tail: true}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ans, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ans, err
	}
	if err := cmd.Start(); err != nil {
		return ans, fmt.Errorf("codex app-server does not start: %w", err)
	}
	if runtime.GOOS == "windows" {
		sysinfo.RegisterChild(cmd.Process.Pid, false)
	}
	msgs := make(chan codexMsg, 16)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(msgs)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 8<<20)
		for sc.Scan() {
			var m codexMsg
			if json.Unmarshal(sc.Bytes(), &m) == nil && len(m.ID) > 0 && m.Method == "" {
				select {
				case msgs <- m:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	finish := sync.OnceFunc(func() {
		stdin.Close() // the app-server exits on EOF
		select {
		case <-readDone:
		case <-time.After(3 * time.Second):
			killTree(cmd)
		}
		_ = cmd.Wait()
		reap(cmd)
	})
	defer finish()
	write := func(v any) error {
		_, err := stdin.Write(codexLine(v))
		return err
	}
	await := func(want map[string]bool) (map[string]codexMsg, error) {
		got := map[string]codexMsg{}
		for len(got) < len(want) {
			select {
			case m, ok := <-msgs:
				if !ok {
					// stdout EOF does not imply that os/exec's stderr copier
					// has finished. Wait before building the diagnostic.
					finish()
					return got, errors.New("codex app-server ended" + stderrHint(stderr.String()))
				}
				if want[m.id()] {
					got[m.id()] = m
				}
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return got, fmt.Errorf("codex app-server gave no answer within %s", s.o.CheckTimeout.Round(time.Second))
				}
				return got, ctx.Err()
			}
		}
		return got, nil
	}
	if err := write(map[string]any{"id": "tmx-1", "method": "initialize", "params": map[string]any{
		"clientInfo": map[string]any{"name": "tmx", "title": "tmx", "version": config.Version}, "capabilities": nil}}); err != nil {
		return ans, err
	}
	got, err := await(map[string]bool{"tmx-1": true})
	if err != nil {
		return ans, err
	}
	if e := got["tmx-1"].Error; e != nil {
		return ans, fmt.Errorf("codex app-server refused to start: %s", e.Message)
	}
	_ = write(map[string]any{"method": "initialized"})
	_ = write(map[string]any{"id": "tmx-2", "method": "account/read", "params": map[string]any{"refreshToken": false}})
	_ = write(map[string]any{"id": "tmx-3", "method": "account/rateLimits/read", "params": map[string]any{"excludeResetCreditDetails": true}})
	got, err = await(map[string]bool{"tmx-2": true, "tmx-3": true})
	if m, ok := got["tmx-2"]; ok {
		if m.Error != nil {
			return ans, fmt.Errorf("codex app-server: %s", m.Error.Message)
		}
		var r struct {
			Account *codexAccount `json:"account"`
		}
		if json.Unmarshal(m.Result, &r) != nil {
			return ans, errors.New("codex app-server answered account/read with something else")
		}
		ans.accountKnown, ans.account = true, r.Account
	}
	if _, ok := got["tmx-3"]; !ok && err != nil {
		ans.limitsErr = err.Error()
	}
	if m, ok := got["tmx-3"]; ok {
		var rl codexRateLimits
		switch {
		case m.Error != nil:
			ans.limitsErr = m.Error.Message
		case json.Unmarshal(m.Result, &rl) == nil:
			ans.limits = &rl
		default:
			ans.limitsErr = "Codex answered with something else"
		}
	}
	if !ans.accountKnown {
		return ans, err
	}
	return ans, nil
}

func stderrHint(s string) string {
	if l := firstLine(s, 160); l != "" {
		return ": " + l
	}
	return ""
}

// codexLimits turns account/rateLimits/read into limits: the main bucket's windows first, then
// any other bucket's (named after it).
func codexLimits(rl codexRateLimits, now time.Time) []Limit {
	limits := codexSnapshotLimits(rl.RateLimits, "", "", now)
	main := deref(rl.RateLimits.LimitID)
	keys := make([]string, 0, len(rl.RateLimitsByLimitID))
	for k := range rl.RateLimitsByLimitID {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		snap := rl.RateLimitsByLimitID[k]
		if snap == nil || k == main || (main == "" && len(limits) > 0 && k == "codex") {
			continue
		}
		name := deref(snap.LimitName)
		if name == "" {
			name = k
		}
		limits = append(limits, codexSnapshotLimits(*snap, slug(k)+"_", name+" · ", now)...)
	}
	return limits
}

func codexSnapshotLimits(snap codexSnapshot, idPrefix, labelPrefix string, now time.Time) []Limit {
	var out []Limit
	for i, w := range []*codexWindow{snap.Primary, snap.Secondary} {
		if w == nil {
			continue
		}
		var mins float64
		if w.WindowDurationMins != nil {
			mins = *w.WindowDurationMins
		}
		id, label := windowName(mins, i == 0)
		l := Limit{ID: idPrefix + id, Label: labelPrefix + label, UsedPercent: clampPercent(w.UsedPercent)}
		if w.ResetsAt != nil && *w.ResetsAt > 0 {
			l.ResetsAt = unixMillis(*w.ResetsAt)
		}
		out = append(out, l)
	}
	return out
}

// windowName names a rate-limit window by its length: 300 min → 5-hour limit, 10080 → weekly.
func windowName(mins float64, primary bool) (id, label string) {
	m := int64(mins + 0.5)
	switch {
	case m <= 0 && primary:
		return "primary", "Short-term limit"
	case m <= 0:
		return "secondary", "Long-term limit"
	case m == 7*24*60:
		return "week", "Weekly limit"
	case m == 24*60:
		return "day", "Daily limit"
	case m >= 28*24*60 && m <= 31*24*60:
		return "month", "Monthly limit"
	case m%(24*60) == 0:
		d := itoa(int(m / (24 * 60)))
		return d + "d", d + "-day limit"
	case m%60 == 0:
		h := itoa(int(m / 60))
		return h + "h", h + "-hour limit"
	}
	return itoa(int(m)) + "m", itoa(int(m)) + "-minute limit"
}

// unixMillis reads a Unix time in seconds (or already in ms) as ms.
func unixMillis(v float64) int64 {
	if v > 1e12 {
		return int64(v)
	}
	return int64(v * 1000)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// codexHome is Codex's folder: CODEX_HOME (the account's, Options.Env's or the daemon's), else
// ~/.codex.
func (s *scope) codexHome() string {
	env := s.env()
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "CODEX_HOME="); ok && v != "" {
			return v
		}
	}
	home, _ := os.UserHomeDir()
	return harness.CodexHome(home)
}

// rolloutRateLimits is a token_count event's rate_limits in a rollout.
type rolloutRateLimits struct {
	Primary   *rolloutWindow `json:"primary"`
	Secondary *rolloutWindow `json:"secondary"`
}

type rolloutWindow struct {
	UsedPercent     float64  `json:"used_percent"`
	WindowMinutes   *float64 `json:"window_minutes"`
	ResetsAt        *float64 `json:"resets_at"`
	ResetsInSeconds *float64 `json:"resets_in_seconds"`
}

// codexRolloutLimits reads the rate limits of the newest rollout that has any (at most the 5
// newest of the last 14 days' folders): the limits, and when Codex reported them.
func codexRolloutLimits(codexHome string, now time.Time) ([]Limit, time.Time, bool) {
	for _, f := range newestRollouts(filepath.Join(codexHome, "sessions"), 5) {
		if rl, at, ok := lastRateLimits(f); ok {
			return rolloutLimits(rl, at, now), at, true
		}
	}
	return nil, time.Time{}, false
}

// newestRollouts lists rollout-*.jsonl files under sessions/YYYY/MM/DD, newest change first,
// looking into the 14 newest day folders.
func newestRollouts(sessions string, n int) []string {
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	days := 0
years:
	for _, y := range dirsDesc(sessions) {
		for _, m := range dirsDesc(y) {
			for _, d := range dirsDesc(m) {
				if days++; days > 14 {
					break years
				}
				ents, _ := os.ReadDir(d)
				for _, e := range ents {
					if e.IsDir() || !strings.HasPrefix(e.Name(), "rollout-") || !strings.HasSuffix(e.Name(), ".jsonl") {
						continue
					}
					if info, err := e.Info(); err == nil {
						files = append(files, file{filepath.Join(d, e.Name()), info.ModTime()})
					}
				}
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	var out []string
	for i := 0; i < len(files) && i < n; i++ {
		out = append(out, files[i].path)
	}
	return out
}

func dirsDesc(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for i := len(ents) - 1; i >= 0; i-- { // ReadDir sorts by name
		if ents[i].IsDir() {
			out = append(out, filepath.Join(dir, ents[i].Name()))
		}
	}
	return out
}

// lastRateLimits finds the last token_count event with rate_limits in a rollout's last 2 MiB.
func lastRateLimits(path string) (rolloutRateLimits, time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return rolloutRateLimits{}, time.Time{}, false
	}
	defer f.Close()
	const tail = 2 << 20
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		_, _ = f.Seek(st.Size()-tail, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, tail))
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if !bytes.Contains(l, []byte(`"token_count"`)) || !bytes.Contains(l, []byte(`"rate_limits"`)) {
			continue
		}
		var ev struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type       string             `json:"type"`
				RateLimits *rolloutRateLimits `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(l, &ev) != nil || ev.Type != "event_msg" || ev.Payload.Type != "token_count" || ev.Payload.RateLimits == nil {
			continue
		}
		rl := *ev.Payload.RateLimits
		if rl.Primary == nil && rl.Secondary == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if err != nil {
			if st, err := os.Stat(path); err == nil {
				at = st.ModTime()
			}
		}
		return rl, at, true
	}
	return rolloutRateLimits{}, time.Time{}, false
}

// rolloutLimits: a window that has reset since the event counts as unused.
func rolloutLimits(rl rolloutRateLimits, at, now time.Time) []Limit {
	var out []Limit
	for i, w := range []*rolloutWindow{rl.Primary, rl.Secondary} {
		if w == nil {
			continue
		}
		var mins float64
		if w.WindowMinutes != nil {
			mins = *w.WindowMinutes
		}
		id, label := windowName(mins, i == 0)
		l := Limit{ID: id, Label: label, UsedPercent: clampPercent(w.UsedPercent)}
		switch {
		case w.ResetsAt != nil && *w.ResetsAt > 0:
			l.ResetsAt = unixMillis(*w.ResetsAt)
		case w.ResetsInSeconds != nil && !at.IsZero():
			l.ResetsAt = at.Add(time.Duration(*w.ResetsInSeconds * float64(time.Second))).UnixMilli()
		}
		if l.ResetsAt > 0 && l.ResetsAt <= now.UnixMilli() {
			l.UsedPercent, l.ResetsAt = 0, 0
		}
		out = append(out, l)
	}
	return out
}

// ago says how long ago d was, roughly: "just now", "5 min ago", "3 h ago", "2 days ago".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d/time.Minute)) + " min ago"
	case d < 48*time.Hour:
		return itoa(int(d/time.Hour)) + " h ago"
	}
	return itoa(int(d/(24*time.Hour))) + " days ago"
}
