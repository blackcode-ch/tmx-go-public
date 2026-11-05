package cli

import (
	"os"
	"path/filepath"

	"github.com/blackcode-ch/tmx-go/internal/runner/host"
)

// cmdHost is `tmx host DIR` (an agent's session host, started by the daemon: it runs the agent's
// program so the agent outlives a daemon restart) and `tmx host --launch DIR` (starts
// that host detached from the daemon and returns at once). Not for people.
func cmdHost(e *env, args []string) error {
	switch {
	case len(args) == 2 && args[0] == "--launch":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return host.Launch(exe, filepath.Clean(args[1]))
	case len(args) == 1 && args[0] != "" && args[0][0] != '-':
		return host.Run(filepath.Clean(args[0]))
	}
	return usageErr("usage: tmx host [--launch] DIR (the daemon runs it for terminal agents)")
}
