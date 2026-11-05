// Package codexguard supplies the Windows Codex parity guard. It is not a sandbox.
package codexguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Pin holds a read handle for the lifetime of a launch. On Windows the handle
// denies writes and replacement without changing permissions or ACLs.
type Pin struct {
	Path   string
	SHA256 string
	file   *os.File
}

func OpenPin(path string) (*Pin, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	f, err := openPinned(real)
	if err != nil {
		return nil, fmt.Errorf("pin file: %w", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("pin requires a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return nil, err
	}
	return &Pin{Path: real, SHA256: hex.EncodeToString(h.Sum(nil)), file: f}, nil
}

func (p *Pin) Close() error { return p.file.Close() }

// Verify checks the pathname as well as the open file, detecting redirection or
// a replaced parent directory before using the path for process creation.
func (p *Pin) Verify() error {
	f, err := os.Open(p.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	want, err := p.file.Stat()
	if err != nil {
		return err
	}
	got, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(want, got) {
		return fmt.Errorf("pinned file identity changed")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return fmt.Errorf("pinned file content changed")
	}
	return nil
}
