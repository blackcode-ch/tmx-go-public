package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/blackcode-ch/tmx-go/internal/peers"
)

// cmdPeers: `tmx peers import-python [--json]` moves this machine's Python tmx pair
// tokens into this daemon's secrets (production profile with "peers"; DESIGN.md
// "Python fleet interop"). It prints fingerprints only, never a secret, and never overwrites a
// secret this daemon already holds with another value.
func cmdPeers(e *env, args []string) error {
	fs := newFlags("peers")
	asJSON := fs.Bool("json", false, "print the JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "import-python" {
		return usageErr("usage: tmx peers import-python [--json]")
	}
	if err := e.load(); err != nil {
		return err
	}
	// Only the Python daemon's own state folder: no --from, so the import can't be pointed at
	// another folder (security-review #108).
	dir, err := peers.PythonHome()
	if err != nil {
		return err
	}
	rows, err := peers.ImportPython(e.cfg, e.paths, dir)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Fprintln(e.out, string(b))
	} else {
		w := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SECRET\tSTATUS\tFINGERPRINT")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Name, r.Status, r.Fingerprint)
		}
		w.Flush()
	}
	for _, r := range rows {
		if r.Status == peers.ImportConflict || r.Status == peers.ImportInvalid {
			return fmt.Errorf("%s: %s (nothing overwritten; compare fingerprints with that machine)", r.Name, r.Status)
		}
	}
	return nil
}
