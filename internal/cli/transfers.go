package cli

import (
	"github.com/blackcode-ch/tmx-go/internal/transfers"
	"path/filepath"
)

// Read-only local listing does not introduce a new owner HTTP surface.
func cmdTransfers(e *env, args []string) error {
	if len(args) != 1 || args[0] != "list" {
		return usageErr("tmx transfers list")
	}
	if err := e.load(); err != nil {
		return err
	}
	e.printJSON(map[string]any{"host": e.cfg.Machine, "transfers": transfers.ListRecords(filepath.Join(e.paths.Home, "transfers"))})
	return nil
}
