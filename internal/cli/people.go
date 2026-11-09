package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/people"
)

// tmx people import [--from DIR] [--apply]: Python tmx's People (team/) into this tmx.
// Dry run unless --apply. The Python files are only read. Prints counts, never names.
func cmdPeople(e *env, args []string) error {
	if len(args) == 0 || args[0] != "import" {
		return usageErr("usage: tmx people import [--from PYTHON_STATE_DIR] [--apply]")
	}
	fs := newFlags("people import")
	from := fs.String("from", pythonStateDir(), "Python tmx state dir (holds team/)")
	apply := fs.Bool("apply", false, "write into this tmx (default: dry run)")
	if _, err := parse(fs, args[1:]); err != nil {
		return err
	}
	plan, err := people.ReadPython(*from)
	if err != nil {
		return err
	}
	fmt.Fprintln(e.out, "found:", plan.Counts())
	if !*apply {
		fmt.Fprintln(e.out, "dry run: nothing written (add --apply)")
		return nil
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var rep fleet.ImportReport
		if err := c.Post(cx, "/api/team/import", plan, &rep); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "stored:", rep.Stored)
		if rep.Rejected > 0 {
			fmt.Fprintf(e.out, "rejected: %d (a phone or email that no longer validates)\n", rep.Rejected)
		}
		if !rep.Verified {
			return fmt.Errorf("import not verified: the stored counts differ from the plan")
		}
		fmt.Fprintln(e.out, "verified; the Python files are unchanged")
		return nil
	})
}

func pythonStateDir() string {
	if d := os.Getenv("TMX_PY_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "TerminalMatrix2")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "tmx")
}
