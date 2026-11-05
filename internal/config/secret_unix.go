//go:build !windows

package config

import "os"

func createSecret(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	// The umask can only have taken bits away; put the owner's back.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return f, nil
}

func mkdirSecret(dir string) error { return os.MkdirAll(dir, 0o700) }
