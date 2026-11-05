package runner

import (
	"fmt"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// elevated is processElevated; tests replace it.
var elevated = processElevated

// Elevated reports whether tmx itself runs with administrator rights: an elevated token on
// Windows, root elsewhere. Everything tmx starts inherits them.
func Elevated() bool { return elevated() }

// RefuseElevated refuses to start an agent while tmx runs elevated, so no harness (Claude,
// Codex, opencode, pi) gets administrator rights it would use without asking (#51). The
// owner's shell is exempt: it is the owner's own terminal, and agents cannot type into it.
func RefuseElevated(spec Spec) error {
	if spec.Harness == model.HarnessShell || !Elevated() {
		return nil
	}
	h := spec.Harness
	if h == "" {
		h = model.HarnessClaude
	}
	return fmt.Errorf("refusing to launch %s from an elevated process: tmx is running as administrator (root); restart it as a normal user", h)
}
