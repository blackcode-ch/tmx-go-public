package fleet

import (
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// projectLeadLocked resolves an assignment, never a launch. Remote assignments
// are durable peer addresses; they do not confer local authentication rights.
func (f *Fleet) projectLeadLocked(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "none" {
		return "", nil
	}
	target, err := f.peerTargetLocked(name)
	if err != nil {
		return "", err
	}
	if IsPeer(target) {
		name, _, _ := SplitPeer(target)
		if ReservedAgentName(name) {
			return "", errf(KindInvalid, "%s is not an agent lead", name)
		}
		return target, nil
	}
	r, err := f.resolveLocked(target)
	if err != nil {
		return "", err
	}
	if r.a.Harness == model.HarnessShell || (r.a.Level != model.LevelLead && r.a.Level != model.LevelHead) {
		return "", errf(KindInvalid, "%s is not a lead or head", r.a.Name)
	}
	return r.a.ID, nil
}
