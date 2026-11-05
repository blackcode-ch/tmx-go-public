package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Effects classification, ported from v2's companion_client.effects_of (tmx-sec review, 4 Oct
// 2026): tmx decides a task's effects itself, because an agent (or text injected into one)
// could declare "none" on a payment. The stronger of the declared and the found effects wins;
// an unknown declared value counts as money.

var effectRank = map[string]int{model.EffectsNone: 0, model.EffectsAccount: 1, model.EffectsOutward: 2, model.EffectsMoney: 3}

// Word lists from v2 (the "sign" of signing a contract is outward; "sign in/up" is account).
var effectWords = []struct {
	kind string
	rx   *regexp.Regexp
}{
	{model.EffectsMoney, wordRx(`money|pay|payment|paid|invoice|iban|bank|price|pricing|refund|charge|purchase|buy|order|subscription|` +
		`billing|budget|cost|eur|chf|usd|[$€£]`)},
	{model.EffectsOutward, wordRx(`e-?mail|send|message|reply|post|publish|deploy|tweet|share|invite|client|customer|outside|public|` +
		`announce|submit|legal|tax|contract|sign`)},
	{model.EffectsAccount, wordRx(`account|password|passkey|key|token|secret|credential|login|log in|sign in|sign up|signin|signup|2fa|permission|` +
		`access|delete|remove|disable|revoke|admin|iam`)},
}

// wordRx matches any alternative not inside a longer word or name (letters, "-" or "_" on
// either side: "billing-portal" is a name, not billing).
func wordRx(alts string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\pL_-])(?:` + alts + `)(?:[^\pL_-]|$)`)
}

var (
	signInRx = regexp.MustCompile(`\bsign[ -]?(?:in|up)\b`)
	// Addresses are names too: URLs, domains, e-mail addresses and paths are left out.
	addressRx = regexp.MustCompile(`\S*(?:://|[./@\\])\S*\pL\S*`)
)

// ClassifyEffects returns none | account | outward | money for a spec. The do-not-touch list
// is not read (as in v2): it only names what Companion must leave alone.
func ClassifyEffects(s model.CompanionSpec) string {
	text := strings.ToLower(strings.Join(append([]string{s.Goal, s.Context, s.Where, s.Success}, s.Steps...), " \n "))
	// "sign in" / "sign up" are account words, not the outward "sign".
	text = signInRx.ReplaceAllString(text, " signin ")
	text = addressRx.ReplaceAllStringFunc(text, func(m string) string {
		if strings.HasSuffix(m, ".") && !strings.ContainsAny(strings.TrimSuffix(m, "."), "./@\\:") {
			return m // a word at the end of a sentence
		}
		return " "
	})
	found := model.EffectsNone
	for _, w := range effectWords {
		if effectRank[w.kind] > effectRank[found] && w.rx.MatchString(text) {
			found = w.kind
		}
	}
	declared := strings.ToLower(strings.TrimSpace(s.Effects))
	if declared == "" {
		declared = model.EffectsNone
	}
	if _, ok := effectRank[declared]; !ok {
		declared = model.EffectsMoney // an unknown value: the strictest
	}
	if effectRank[declared] >= effectRank[found] {
		return declared
	}
	return found
}

// EffectsLabel says in words what an effects value means (approvals, the gate's prompt).
func EffectsLabel(e string) string {
	switch e {
	case model.EffectsMoney:
		return "may spend or move money"
	case model.EffectsOutward:
		return "may act outward (send, post, sign, contact people)"
	case model.EffectsAccount:
		return "may touch accounts or access (log in, keys, permissions, deleting)"
	}
	return "no money, accounts or outward actions found"
}

// Limits of a spec (Companion takes up to 50k characters; tmx keeps tasks short).
const (
	MaxGoal  = 500
	MaxText  = 12 << 10
	MaxSteps = 30
)

// CheckSpec trims a spec and refuses an empty or oversized one.
func CheckSpec(s model.CompanionSpec) (model.CompanionSpec, error) {
	s.Goal = strings.TrimSpace(s.Goal)
	s.Context = strings.TrimSpace(s.Context)
	s.Success = strings.TrimSpace(s.Success)
	s.Where = strings.TrimSpace(s.Where)
	s.Effects = strings.ToLower(strings.TrimSpace(s.Effects))
	s.Steps = cleanLines(s.Steps)
	s.DoNotTouch = cleanLines(s.DoNotTouch)
	switch {
	case s.Goal == "":
		return s, fmt.Errorf("the task needs a goal")
	case len(s.Goal) > MaxGoal:
		return s, fmt.Errorf("the goal is longer than %d characters; put details in context or steps", MaxGoal)
	case len(s.Steps) > MaxSteps:
		return s, fmt.Errorf("at most %d steps", MaxSteps)
	}
	if s.Effects != "" {
		if _, ok := effectRank[s.Effects]; !ok {
			return s, fmt.Errorf("effects must be none, account, outward or money")
		}
	}
	return s, nil
}

func cleanLines(in []string) []string {
	var out []string
	for _, l := range in {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// RenderInfo is who a task is from, for its first line.
type RenderInfo struct {
	Machine string // this machine's name
	Agent   string // the agent that asked (agent origin) or gets the outcome (owner origin)
	Origin  string // owner | agent
}

// Render turns a spec into the exact text Companion receives: one clear instruction (goal,
// context, numbered steps, where, done-when, don'ts, effects), with anything that looks like a
// key or password replaced (task text lands in Companion's memory). The owner sees this text
// before it is sent; it never changes afterwards.
func Render(s model.CompanionSpec, effects string, info RenderInfo) string {
	var b strings.Builder
	if info.Origin == "agent" {
		fmt.Fprintf(&b, "Task from tmx on %s, asked by its agent %q and approved by the owner in tmx: %s\n", info.Machine, info.Agent, s.Goal)
	} else {
		fmt.Fprintf(&b, "Task from the owner, sent through tmx on %s: %s\n", info.Machine, s.Goal)
	}
	if s.Context != "" {
		fmt.Fprintf(&b, "Context: %s\n", s.Context)
	}
	if s.Where != "" {
		fmt.Fprintf(&b, "Where: %s\n", s.Where)
	}
	if len(s.Steps) > 0 {
		b.WriteString("Steps:\n")
		for i, st := range s.Steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, st)
		}
	}
	if s.Success != "" {
		fmt.Fprintf(&b, "Done when: %s\n", s.Success)
	}
	if len(s.DoNotTouch) > 0 {
		fmt.Fprintf(&b, "Do not touch: %s\n", strings.Join(s.DoNotTouch, "; "))
	}
	if effects != model.EffectsNone {
		fmt.Fprintf(&b, "Effects: %s (tmx's classification). Ask your owner before any step your own rules gate.\n", effects)
	}
	b.WriteString("Report back what you did and what you found, or ask if anything is unclear.")
	out := Scrub(b.String())
	if len(out) > MaxText {
		out = out[:MaxText] + "…"
	}
	return out
}

// TextHash is the hash a preview hands back so the created task is exactly what was shown.
func TextHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

// Secret-looking strings: known key prefixes, "password: …" pairs, private keys, and long
// unbroken hex/base64 runs.
var secretRx = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr|github_pat|glpat|xox[abpors])[-_][A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`(?i)\b(password|passwd|pwd|passphrase|pin|secret|token|api[_ -]?key)\s*[:=]\s*\S+`),
	regexp.MustCompile(`\b[A-Fa-f0-9]{48,}\b`), // longer than a git commit hash (40)
}

// longRunRx: a long unbroken base64-like run; replaced only when it mixes upper case, lower
// case and digits (a key, not a word or a path: "/" and "." break runs).
var longRunRx = regexp.MustCompile(`[A-Za-z0-9+_-]{32,}={0,2}`)

const removed = "[removed by tmx]"

// Scrub replaces secret-looking strings with "[removed by tmx]".
func Scrub(s string) string {
	for i, rx := range secretRx {
		if i == 5 { // keep the word, drop the value
			s = rx.ReplaceAllString(s, "$1: "+removed)
			continue
		}
		s = rx.ReplaceAllString(s, removed)
	}
	return longRunRx.ReplaceAllStringFunc(s, func(m string) string {
		var up, low, dig bool
		for _, r := range m {
			switch {
			case r >= 'A' && r <= 'Z':
				up = true
			case r >= 'a' && r <= 'z':
				low = true
			case r >= '0' && r <= '9':
				dig = true
			}
		}
		if up && low && dig {
			return removed
		}
		return m
	})
}
