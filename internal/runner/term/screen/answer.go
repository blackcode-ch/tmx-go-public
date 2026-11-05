package screen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Synonyms map the generic answers "approve", "approve_always" and "deny" to label
// fragments of one harness's menus, tried in order.
type Synonyms map[string][]string

// PlanAnswer returns the keys (a Keys spec) that pick choice in the menu d: an option
// number, a label fragment, or a synonym name. It navigates with arrows from the highlighted
// option, so it works whether or not the harness takes digit shortcuts and never relies on
// the default; without a highlighted option it types the number.
func PlanAnswer(d Detection, choice string, syn Synonyms) (string, error) {
	if len(d.Choices) == 0 {
		return "", fmt.Errorf("no menu on screen (%s)", d.State)
	}
	target := 0
	if n, err := strconv.Atoi(strings.TrimSpace(choice)); err == nil {
		target = n
	} else {
		wants, ok := syn[choice]
		if !ok {
			wants = []string{choice}
		}
		var pick []Choice
		for _, w := range wants {
			w = strings.ToLower(strings.TrimSpace(w))
			if w == "" {
				continue
			}
			var exact, loose []Choice
			for _, c := range d.Choices {
				l := strings.ToLower(c.Label)
				if strings.HasPrefix(l, w) {
					exact = append(exact, c)
				}
				if strings.Contains(l, w) {
					loose = append(loose, c)
				}
			}
			if choice == "approve" { // "Yes" must not pick "Yes, and don't ask again"
				sort.SliceStable(exact, func(i, j int) bool { return len(exact[i].Label) < len(exact[j].Label) })
			}
			if pick = exact; len(pick) == 0 {
				pick = loose
			}
			if len(pick) > 0 {
				break
			}
		}
		if len(pick) == 0 {
			return "", fmt.Errorf("no option matches %q; the options are: %s", choice, strings.Join(d.Labels(), "; "))
		}
		target = pick[0].N
	}
	found := false
	for _, c := range d.Choices {
		found = found || c.N == target
	}
	if !found {
		return "", fmt.Errorf("option %d is not on the screen", target)
	}
	if d.NumericEnter {
		return strconv.Itoa(target) + " enter", nil
	}
	if d.Selected == 0 {
		return strconv.Itoa(target), nil
	}
	delta := target - d.Selected
	key, back := "down", "up"
	if d.Horizontal {
		key, back = "right", "left"
	}
	if delta < 0 {
		key, delta = back, -delta
	}
	return strings.TrimSpace(strings.Repeat(key+" ", delta) + "enter"), nil
}

var namedKeys = map[string]string{
	"enter": "\r", "tab": "\t", "esc": "\x1b", "escape": "\x1b", "backspace": "\x7f", "space": " ",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F", "pgup": "\x1b[5~", "pgdn": "\x1b[6~", "delete": "\x1b[3~",
}

// Keys encodes a space-separated key spec ("down down enter", "ctrl+c", "2", "y") as the
// bytes a VT terminal sends, one element per key.
func Keys(spec string) ([][]byte, error) {
	var out [][]byte
	for _, tok := range strings.Fields(spec) {
		k := strings.ToLower(tok)
		switch {
		case namedKeys[k] != "":
			out = append(out, []byte(namedKeys[k]))
		case k == "shift+tab":
			out = append(out, []byte("\x1b[Z"))
		case strings.HasPrefix(k, "ctrl+") && len(k) == 6 && k[5] >= 'a' && k[5] <= 'z':
			out = append(out, []byte{k[5] - 'a' + 1})
		case len([]rune(tok)) == 1:
			out = append(out, []byte(tok))
		default:
			return nil, fmt.Errorf("unknown key %q", tok)
		}
	}
	return out, nil
}
