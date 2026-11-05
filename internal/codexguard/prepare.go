package codexguard

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Lease is daemon-created launch evidence, never an API request field. Keep it
// alive until the harness exits. It certifies Python-parity setup, not isolation.
type Lease struct {
	Config     *ConfigLease
	Binary     *Pin
	Shims      string
	Manifest   string
	pins       []*Pin
	shimBinary *Pin
}

func (l *Lease) Close() error {
	if l.Config != nil {
		l.Config.Close()
	}
	for _, pin := range l.pins {
		pin.Close()
	}
	return nil
}

func (l *Lease) Verify() error {
	if l == nil || l.Config == nil || l.Binary == nil {
		return fmt.Errorf("Codex parity setup is missing")
	}
	if err := l.Config.Verify(); err != nil {
		return err
	}
	for _, pin := range l.pins {
		if err := pin.Verify(); err != nil {
			return err
		}
	}
	if l.shimBinary != nil {
		src, err := os.Stat(l.shimBinary.Path)
		if err != nil {
			return err
		}
		for _, name := range ShimNames {
			dst, err := os.Stat(filepath.Join(l.Shims, name+".exe"))
			if err != nil || !os.SameFile(src, dst) {
				return fmt.Errorf("Codex shim identity changed: %s", name)
			}
		}
	}
	return nil
}

func (l *Lease) Env(searchPath string) []string {
	return []string{"CODEX_HOME=" + l.Config.Home, "PATH=" + l.Shims + string(os.PathListSeparator) + searchPath}
}

// Prepare only writes beneath base. Existing files must match exactly, so a
// restart cannot silently trust an agent-modified config/manifest/shim.
// guardCtx is pinned into the manifest so a later shim run authorizes against
// the boundary tmx chose at launch, never the agent's own environment.
func Prepare(base, cwd, codex, tmx, searchPath string, guardCtx Guard) (lease *Lease, err error) {
	if !filepath.IsAbs(base) || !filepath.IsAbs(codex) || !filepath.IsAbs(tmx) {
		return nil, fmt.Errorf("Codex guard paths must be absolute")
	}
	if strings.ToLower(filepath.Ext(codex)) != ".exe" {
		return nil, fmt.Errorf("Codex guard needs the native codex.exe, not a shell wrapper")
	}
	lease = &Lease{Shims: filepath.Join(base, "shims"), Manifest: filepath.Join(base, "manifest.json")}
	defer func() {
		if err != nil {
			lease.Close()
		}
	}()
	lease.Binary, err = OpenPin(codex)
	if err != nil {
		return lease, err
	}
	lease.pins = append(lease.pins, lease.Binary)
	lease.Config, err = NewConfig(filepath.Join(base, "home"), cwd, "tmx supplies this agent's current instructions through its managed launch.\n")
	if err != nil {
		return lease, err
	}
	if err = os.MkdirAll(lease.Shims, 0700); err != nil {
		return lease, err
	}
	manifest := Manifest{Targets: map[string]Target{}, Codex: Target{Path: lease.Binary.Path, SHA256: lease.Binary.SHA256}, Guard: guardCtx}
	lease.shimBinary, err = OpenPin(tmx)
	if err != nil {
		return lease, err
	}
	lease.pins = append(lease.pins, lease.shimBinary)
	for _, name := range ShimNames {
		if target, resolveErr := ResolveTarget(name, searchPath); resolveErr == nil {
			p, pinErr := OpenPin(target)
			if pinErr != nil {
				return lease, pinErr
			}
			manifest.Targets[name] = Target{Path: p.Path, SHA256: p.SHA256}
			p.Close()
		}
		path := filepath.Join(lease.Shims, name+".exe")
		if linkErr := os.Link(tmx, path); linkErr != nil && !os.IsExist(linkErr) {
			return lease, linkErr
		}
		src, statErr := os.Stat(tmx)
		if statErr != nil {
			return lease, statErr
		}
		dst, statErr := os.Stat(path)
		if statErr != nil || !os.SameFile(src, dst) {
			return lease, fmt.Errorf("Codex shim identity changed: %s", name)
		}
	}
	if shell := os.Getenv("SystemRoot"); shell != "" {
		p, pinErr := OpenPin(filepath.Join(shell, "System32", "cmd.exe"))
		if pinErr != nil {
			return lease, pinErr
		}
		manifest.Shell = Target{Path: p.Path, SHA256: p.SHA256}
		p.Close()
	}
	b, marshalErr := json.Marshal(manifest)
	if marshalErr != nil {
		return lease, marshalErr
	}
	p, pinErr := writePinned(lease.Manifest, b)
	if pinErr != nil {
		return lease, pinErr
	}
	lease.pins = append(lease.pins, p)
	return lease, lease.Verify()
}

func writePinned(path string, body []byte) (*Pin, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = f.Write(body)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	} else if os.IsExist(err) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	p, err := OpenPin(path)
	if err != nil {
		return nil, err
	}
	if p.SHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) {
		p.Close()
		return nil, fmt.Errorf("managed Codex file changed: %s", filepath.Base(path))
	}
	return p, nil
}
