package runner

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestRefuseElevated(t *testing.T) {
	defer func(f func() bool) { elevated = f }(elevated)

	agents := []model.Harness{"", model.HarnessClaude, model.HarnessCodex, model.HarnessOpenCode, model.HarnessPi}

	elevated = func() bool { return true }
	for _, h := range agents {
		err := RefuseElevated(Spec{Harness: h})
		if err == nil || !strings.Contains(err.Error(), "elevated") {
			t.Errorf("harness %q elevated: err = %v, want a refusal", h, err)
		}
	}
	if err := RefuseElevated(Spec{Harness: model.HarnessShell}); err != nil {
		t.Errorf("the owner's shell elevated: err = %v, want nil", err)
	}
	if err := RefuseElevated(Spec{}); err == nil || !strings.Contains(err.Error(), "claude") {
		t.Errorf("empty harness: err = %v, want it named as claude", err)
	}

	elevated = func() bool { return false }
	for _, h := range append(agents, model.HarnessShell) {
		if err := RefuseElevated(Spec{Harness: h}); err != nil {
			t.Errorf("harness %q not elevated: err = %v, want nil", h, err)
		}
	}
}
