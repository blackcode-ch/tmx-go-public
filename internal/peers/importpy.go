package peers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// Moving a machine from the Python tmx daemon to this one (DESIGN.md "Python fleet interop",
// the switch runbook): the fleet's other machines already hold a pair token for this machine and
// keep sending it, so this daemon must know it. ImportPython copies the machine's own Python
// pair tokens (for the configured peers only) into this daemon's secrets, inside tmx: nobody
// reads or copies a secrets file by hand, and only fingerprints are shown. The fleet token is
// deliberately not imported: the fleet has retired it (pair tokens only), which is also why
// importing the existing pair tokens is the only way the switched machine stays paired.

// Import statuses.
const (
	ImportImported  = "imported"
	ImportUnchanged = "unchanged"
	ImportMissing   = "missing"  // the Python daemon holds no token for it
	ImportConflict  = "conflict" // this daemon already holds a different one: never overwritten
	ImportInvalid   = "invalid"  // the Python token is not a usable pair token
	ImportSkipped   = "skipped"  // not explicitly a full peer there (role "cloud", or none): not imported
)

// Imported is one pair token ImportPython considered.
type Imported struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// PythonHome is the Python tmx daemon's state folder (its paths.home()).
func PythonHome() (string, error) {
	if h := os.Getenv("TMX_HOME"); h != "" {
		return h, nil
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "TerminalMatrix2"), nil
		}
		return "", errors.New("LOCALAPPDATA is not set")
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "tmx"), nil
}

// ImportPython imports from the Python daemon at pyHome (the CLI passes only PythonHome()).
// Production profile with peers only; full peers only, and only those whose Python entry says
// "role": "full" explicitly: a token with no role is not given a full peer's reach here.
// A secret this daemon already holds with another value is never overwritten (ImportConflict).
func ImportPython(cfg config.Config, p config.Paths, pyHome string) ([]Imported, error) {
	if !cfg.Production() || len(cfg.Peers) == 0 {
		return nil, fmt.Errorf("import needs the %q profile with \"peers\" in config.json", config.ProfileProduction)
	}
	var pyTokens map[string]map[string]any
	if err := readPlainJSON(pyHome, []string{"secrets", "peers.json"}, &pyTokens); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("the Python daemon's pair tokens: %w", err)
	}

	var out []Imported
	tokens := NewTokens(p.PeersFile())
	for _, peer := range cfg.Peers {
		row := Imported{Name: peer.Name}
		tok, _ := pyTokens[peer.Name]["token"].(string)
		// No role is the restricted case here, although Python itself reads a missing role as
		// "full": a full peer operates this machine's agents, so that takes an explicit choice.
		role, _ := pyTokens[peer.Name]["role"].(string)
		cur := tokens.For(peer.Name)
		switch {
		case tok == "":
			row.Status = ImportMissing
		case role != "full": // a cloud box's token, or one with no role: never a full peer here
			row.Status = ImportSkipped
		case len(tok) < minTokenLen:
			row.Status = ImportInvalid
		case cur == tok:
			row.Status, row.Fingerprint = ImportUnchanged, Fingerprint(tok)
		case cur != "":
			row.Status, row.Fingerprint = ImportConflict, Fingerprint(cur)
		default:
			fp, err := tokens.Store(peer.Name, tok, "python-import")
			if err != nil {
				return out, err
			}
			row.Status, row.Fingerprint = ImportImported, fp
		}
		out = append(out, row)
	}

	return out, nil
}

// readPlainJSON reads home/<parts...> as JSON. Every step must be what it says: home and each
// folder a real directory, the file a regular file. A symlink, junction or other reparse point
// anywhere is refused, so the import never follows a link to somewhere else (security-review
// #108). The opened file is checked to be the one inspected.
func readPlainJSON(home string, parts []string, v any) error {
	if !filepath.IsAbs(home) {
		return fmt.Errorf("%s is not an absolute path", home)
	}
	path := filepath.Clean(home)
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			path = filepath.Join(path, parts[i])
		}
		fi, err := os.Lstat(path)
		if err != nil {
			return err
		}
		last := i == len(parts)-1
		if last && !fi.Mode().IsRegular() || !last && fi.Mode().Type() != os.ModeDir {
			return fmt.Errorf("%s is a link or not a plain %s: refused", path, map[bool]string{true: "file", false: "folder"}[last])
		}
	}
	want, err := os.Lstat(path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(want, got) {
		return fmt.Errorf("%s changed while it was read: refused", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err) // never the content: it holds secrets
	}
	return nil
}
