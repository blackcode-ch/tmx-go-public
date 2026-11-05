package accounts

import (
	"math"
	"strconv"
	"strings"
)

// plans maps the harnesses' plan ids (Claude's subscriptionType, Codex's planType) to names.
var plans = map[string]string{
	"free": "Free", "go": "Go", "plus": "Plus", "pro": "Pro", "max": "Max",
	"prolite": "Pro Lite", "promax": "Pro Max", "team": "Team", "business": "Business",
	"enterprise": "Enterprise", "ent26": "Enterprise", "edu": "Edu", "edu_plus": "Edu Plus",
	"edu_pro": "Edu Pro", "sci": "Sci", "sci_plus": "Sci Plus",
	"self_serve_business_prolite": "Business", "self_serve_business_usage_based": "Business",
	"enterprise_cbp_automation": "Enterprise", "enterprise_cbp_usage_based": "Enterprise",
	"claude_max": "Max", "claude_pro": "Pro", "claude_team": "Team", "claude_enterprise": "Enterprise",
}

// humanPlan names a plan id: "max" → Max, "max_20x" → Max 20x, "unknown" → "".
func humanPlan(id string) string {
	id = strings.TrimSpace(id)
	k := strings.ToLower(id)
	if k == "" || k == "unknown" || k == "none" || k == "null" {
		return ""
	}
	if p, ok := plans[k]; ok {
		return p
	}
	words := strings.FieldsFunc(k, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	for i, w := range words {
		if p, ok := plans[w]; ok {
			words[i] = p
		} else if w[0] >= '0' && w[0] <= '9' { // "20x" stays as it is
			words[i] = w
		} else {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// parsePercent reads "54" or "54.5" as a percentage within 0–100, to one decimal.
func parsePercent(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return clampPercent(f), true
}

func clampPercent(f float64) float64 {
	f = math.Round(f*10) / 10
	return math.Max(0, math.Min(100, f))
}

func itoa(n int) string { return strconv.Itoa(n) }

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
