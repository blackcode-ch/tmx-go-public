package cli

import (
	"github.com/blackcode-ch/tmx-go/internal/config"
	"strings"
	"testing"
)

func TestTrialCompanionCommandRefused(t *testing.T) {
	for _, sub := range []string{"status", "install", "connect", "uninstall", "message", "token"} {
		e := &env{loaded: true, cfg: config.Config{Profile: "trial"}}
		if err := cmdCompanion(e, []string{sub}); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Errorf("%s: %v", sub, err)
		}
	}
}
