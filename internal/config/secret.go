package config

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

// Secret files (tokens, keys, the WhatsApp session, gate.json) are private to the current user
// from the moment they exist: 0600 in a 0700 folder on Unix. On Windows those modes do nothing
// and a new file takes its folder's ACL, so there they get a protected DACL (secret_windows.go).

// WriteSecretFile writes a secret atomically: a private temp file next to path, synced, then
// renamed over path, so readers see the old secret or the new one. If the private file cannot
// be made, nothing is written. A missing folder is made private; an existing one is left as is.
func WriteSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := mkdirSecret(dir); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+"."+rand.Text())
	f, err := createSecret(tmp)
	if err != nil {
		return fmt.Errorf("secret not written: %w", err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	// The rename keeps the temp file's own permissions whatever the replaced file had. On Windows
	// os.Rename is MoveFileEx: within one folder (one volume) the file keeps its DACL and does not
	// inherit again; being protected, it would ignore the folder's entries anyway.
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// CreateSecretFile creates a new secret file for the caller to write, private from the moment
// it exists. It fails with os.ErrExist when the file is there already, so two processes racing
// on first use end up with one secret. A missing folder is made private.
func CreateSecretFile(path string) (*os.File, error) {
	if err := mkdirSecret(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return createSecret(path)
}

// MkdirSecret creates a private secrets folder. Existing folders retain their permissions;
// ownership cannot be inferred from a pathname. Unsafe paths fail closed.
func MkdirSecret(dir string) error {
	if err := mkdirSecret(dir); err != nil {
		return err
	}
	return nil
}
