package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackcode-ch/tmx-go/internal/skill"
)

const skillUsage = `usage: tmx skill                     print SKILL.md (for AI agents)
       tmx skill install [--dir DIR]   write it to DIR/SKILL.md (default ~/.claude/skills/tmxg)
       tmx skill path                print the default install path`

// cmdSkill prints or installs SKILL.md; it needs no daemon.
func cmdSkill(e *env, args []string) error {
	if len(args) == 0 {
		_, err := e.out.Write(skill.Content())
		return err
	}
	switch args[0] {
	case "path":
		if len(args) != 1 {
			return usageErr(skillUsage)
		}
		dir, err := skill.DefaultDir()
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out, filepath.Join(dir, "SKILL.md"))
		return nil
	case "install":
		fs := newFlags("skill install")
		dir := fs.String("dir", "", "folder to write SKILL.md into (default ~/.claude/skills/tmxg)")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 0 {
			return usageErr(skillUsage)
		}
		path, changed, err := installSkill(*dir)
		if err != nil {
			return err
		}
		if !changed {
			fmt.Fprintf(e.errw, "already up to date\n")
		}
		fmt.Fprintln(e.out, path)
		return nil
	case "help", "-h", "--help":
		fmt.Fprintln(e.out, skillUsage)
		return nil
	}
	return usageErr(skillUsage)
}

// installSkill writes SKILL.md into dir (default ~/.claude/skills/tmxg) unless it is there
// with the same content already. It reports the file's path and whether it wrote it.
func installSkill(dir string) (string, bool, error) {
	if dir == "" {
		d, err := skill.DefaultDir()
		if err != nil {
			return "", false, err
		}
		dir = d
	}
	if st, err := os.Stat(dir); err == nil && !st.IsDir() {
		return "", false, fmt.Errorf("%s is a file, not a folder", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	path := filepath.Join(dir, "SKILL.md")
	want := skill.Content()
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, want) {
		return path, false, nil
	}
	if err := os.WriteFile(path, want, 0o644); err != nil {
		return "", false, err
	}
	return path, true, nil
}
