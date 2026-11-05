package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// hostExecutable is the copy of this tmx that session hosts run (fleet.Options.HostExecutable):
// <home>/bin/tmx-host-<hash of the binary>. A host runs for as long as its agent, often across
// updates, and Windows cannot replace a running executable: hosts running the installed tmx
// would block its update. Each version gets its own copy, made once.
func hostExecutable(home, exe string) (string, error) {
	f, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	dir := filepath.Join(home, "bin")
	dst := filepath.Join(dir, "tmx-host-"+hex.EncodeToString(h.Sum(nil))[:16]+filepath.Ext(exe))
	if st, err := os.Stat(dst); err == nil && !st.IsDir() {
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if err := config.WriteFileAtomic(dst, b, 0o755); err != nil {
		return "", err
	}
	return dst, nil
}
