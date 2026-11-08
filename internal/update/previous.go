package update

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Keep two recent copies. A protected running copy is retained in addition if
// necessary; callers must never unlink the executable they are running from.
func prunePrevious(t target, current, running string) error {
	entries, err := os.ReadDir(filepath.Dir(t.path))
	if err != nil {
		return err
	}
	type backup struct {
		path string
		info os.FileInfo
	}
	var candidates []backup
	prefix := filepath.Base(t.path) + ".prev-"
	runningInfo, _ := os.Stat(running)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		hash := strings.TrimPrefix(entry.Name(), prefix)
		if t.goos == "windows" {
			if !strings.HasSuffix(hash, ".exe") {
				continue
			}
			hash = strings.TrimSuffix(hash, ".exe")
		}
		if len(hash) != 64 {
			continue
		}
		if _, err := hex.DecodeString(hash); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		candidates = append(candidates, backup{filepath.Join(filepath.Dir(t.path), entry.Name()), info})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].info.ModTime().Equal(candidates[j].info.ModTime()) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].info.ModTime().After(candidates[j].info.ModTime())
	})
	kept := 1 // current is always retained, even when its timestamp is older.
	for _, b := range candidates {
		if b.path == current {
			continue
		}
		if kept < 2 {
			kept++
			continue
		}
		if runningInfo != nil && os.SameFile(b.info, runningInfo) {
			continue
		}
		if err := os.Remove(b.path); err != nil {
			return err
		}
	}
	return nil
}
