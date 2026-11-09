package antechamber

import (
	"fmt"
	"strings"
)

// The fleet rule (Python transfers.py; the owner, 7 Oct: "I asked for freedom"): between the owner's
// own machines the antechamber informs, it does not hold. A needs_human copy is approved and
// placed, logged with its reasons, unless the passkey rule keeps it for the human: destructive
// commands, disabling defences, and text that tries to steer an agent (tmx-sec M2, 10 Oct), or the
// copy came from a cloud box. A blocked item is still refused.

// PasskeyCodes are finding codes that always wait for the human, at any level.
var PasskeyCodes = map[string]bool{"agent-manipulation": true}

// PasskeyPatterns are pattern categories (the start of a "pattern" finding's text) that wait for the human.
var PasskeyPatterns = []string{"destructive:", "disabling defences:"}

// FleetTrusts is Python's fleet_trusts: true unless config transfers.hold_needs_human is set.
func FleetTrusts(holdNeedsHuman bool) bool { return !holdNeedsHuman }

// AutoLabel is how an auto-released copy is shown everywhere (events, transfers, the dashboard).
func AutoLabel(reasons []string) string {
	why := strings.Join(reasons[:min(3, len(reasons))], "; ")
	if len(reasons) > 3 {
		why += " …"
	}
	if why == "" {
		why = "see the antechamber"
	}
	return fmt.Sprintf("auto-released (would have needed a human: %s)", why)
}

// PasskeyReasons lists the reasons of an item that keep it for the human even inside the fleet.
func PasskeyReasons(it Item) []string {
	var out []string
	for _, r := range it.Reasons {
		if r.Level == Clean && !PasskeyCodes[r.Code] { // steering text counts at any level
			continue
		}
		if PasskeyCodes[r.Code] || (r.Code == "pattern" && hasAnyPrefix(r.Text, PasskeyPatterns)) {
			out = append(out, r.Text)
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ShouldAutoRelease: a needs_human item, the fleet trusts, no passkey reason, not from a cloud box.
func ShouldAutoRelease(it Item, fleetTrusts bool) bool {
	return it.Verdict == NeedsHuman && fleetTrusts && len(PasskeyReasons(it)) == 0 && !it.Origin.Cloud
}

// AutoRelease applies the fleet rule to an admitted copy from another of the owner's machines:
// when it applies, the item is approved as "fleet copy from <from> (owner's ruling)" and the
// label to show is returned with ok true. Otherwise the item is left as it is.
func (a *Antechamber) AutoRelease(id, from string, fleetTrusts bool) (it Item, label string, ok bool, err error) {
	if it, err = a.Get(id); err != nil || !ShouldAutoRelease(it, fleetTrusts) {
		return it, "", false, err
	}
	if it, err = a.Approve(id, fmt.Sprintf("fleet copy from %s (owner's ruling)", from)); err != nil {
		return it, "", false, err
	}
	var why []string
	for _, r := range it.Reasons {
		if r.Level != Clean && len(why) < 10 {
			why = append(why, r.Text)
		}
	}
	return it, AutoLabel(why), true, nil
}
