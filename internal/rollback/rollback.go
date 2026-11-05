// Package rollback retains a consistent N-1 database and installation. Callers
// must hold the daemon lock for restore and keep all database users closed.
package rollback

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type manifest struct {
	Binary         string
	Database       string
	Hashes         map[string]string
	BinaryUnproven bool `json:",omitempty"`
}

// MaxCheckpointBytes bounds the retained binary and database pair to 1 GiB.
// Publication keeps one pair; staging temporarily needs room for a second.
const MaxCheckpointBytes int64 = 1 << 30

// Refresh checkpoints a proven-good clean boot only when its installation bytes
// changed. An updater's pending pair must survive until migration completes.
func Refresh(ctx context.Context, dir, binary, dbPath string) error {
	if Pending(dir) || AwaitingMigration(dir) {
		return errors.New("rollback recovery is pending")
	}
	if Available(dir) {
		pair, err := load(dir, binary, dbPath)
		if err != nil {
			return err
		}
		old, err := treeHashes(filepath.Join(pair, "binary"))
		if err != nil {
			return err
		}
		current, err := treeHashes(binary)
		if err != nil {
			return err
		}
		unproven, err := Unproven(dir, binary, dbPath)
		if err != nil {
			return err
		}
		if !unproven && maps.Equal(old, current) {
			return nil
		}
	}
	return Prepare(ctx, dir, binary, dbPath)
}

func treeSize(root string, limit int64) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() {
			size += info.Size()
			if size > limit {
				return errors.New("rollback checkpoint exceeds size limit")
			}
		}
		return nil
	})
	return size, err
}

func database(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	// A Windows drive path needs a leading slash in a file URI.
	if filepath.VolumeName(abs) != "" {
		u.Path = "/" + u.Path
	}
	return sql.Open("sqlite", u.String()+"?mode=rw&_pragma=busy_timeout(10000)")
}

// Snapshot uses SQLite itself, including committed WAL pages, never a raw DB copy.
func Snapshot(ctx context.Context, path, destination string) error {
	db, err := database(path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", destination)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Prepare publishes a complete pair only after both copies have been synced.
// An unsuccessful preparation leaves the last published pair untouched.
func Prepare(ctx context.Context, dir, binary, dbPath string) error {
	return prepareWithLimit(ctx, dir, binary, dbPath, MaxCheckpointBytes, false)
}

// PrepareUnproven preserves the database before a first manual migration when
// no proven binary was retained. Restore will leave the live binary untouched.
func PrepareUnproven(ctx context.Context, dir, binary, dbPath string) error {
	return prepareWithLimit(ctx, dir, binary, dbPath, MaxCheckpointBytes, true)
}

func prepareWithLimit(ctx context.Context, dir, binary, dbPath string, limit int64, unproven bool) error {
	if Pending(dir) || AwaitingMigration(dir) {
		return errors.New("rollback recovery is pending")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	dbPath, err = filepath.Abs(dbPath)
	if err != nil {
		return err
	}
	binSize, err := treeSize(binary, limit)
	if err != nil {
		return err
	}
	db, err := database(dbPath)
	if err != nil {
		return err
	}
	var pages, pageSize int64
	err = db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages)
	if err == nil {
		err = db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize)
	}
	err = errors.Join(err, db.Close())
	if err != nil {
		return err
	}
	if pageSize <= 0 || pages > (limit-binSize)/pageSize {
		return errors.New("rollback checkpoint exceeds size limit")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, "pair-")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(stage)
		}
	}()
	if err := copyTree(binary, filepath.Join(stage, "binary")); err != nil {
		return fmt.Errorf("retain binary: %w", err)
	}
	if err := Snapshot(ctx, dbPath, filepath.Join(stage, "database")); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	if _, err := treeSize(stage, limit); err != nil {
		return err
	}
	hashes, err := treeHashes(stage)
	if err != nil {
		return err
	}
	m := manifest{Binary: binary, Database: dbPath, Hashes: hashes, BinaryUnproven: unproven}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := writeSync(filepath.Join(stage, "manifest.json"), b); err != nil {
		return err
	}
	pointer, err := os.CreateTemp(dir, "current-")
	if err != nil {
		return err
	}
	name := pointer.Name()
	pointer.Close()
	defer os.Remove(name)
	if err := writeSync(name, []byte(filepath.Base(stage))); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, "current")); err != nil {
		return err
	}
	published = true
	// Keep just the N-1 generation after publication. Incomplete generations from
	// an interrupted preparation are also safe to discard at this point.
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "pair-") && entry.Name() != filepath.Base(stage) {
			os.RemoveAll(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

// Available reports whether a checkpoint pointer exists. Restore validates its contents.
func Available(dir string) bool { _, err := os.Stat(filepath.Join(dir, "current")); return err == nil }

// Unproven reports whether recovery can only restore the database, not an older binary.
func Unproven(dir, binary, dbPath string) (bool, error) {
	pair, err := load(dir, binary, dbPath)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(pair, "manifest.json"))
	if err != nil {
		return false, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return false, err
	}
	return m.BinaryUnproven, nil
}

// AwaitingMigration preserves the pre-update pair across the successor's startup.
func AwaitingMigration(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "updating"))
	return err == nil
}
func MarkUpdate(dir string) error {
	return writeSync(filepath.Join(dir, "updating"), []byte("pending startup\n"))
}
func CompleteUpdate(dir string) error {
	err := os.Remove(filepath.Join(dir, "updating"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Pending marks an interrupted restore. Startup must finish it before opening the store.
func Pending(dir string) bool { _, err := os.Stat(filepath.Join(dir, "restoring")); return err == nil }

func load(dir, binary, dbPath string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "current"))
	if err != nil {
		return "", err
	}
	name := string(b)
	if !strings.HasPrefix(name, "pair-") || filepath.Base(name) != name {
		return "", errors.New("invalid rollback pointer")
	}
	pair := filepath.Join(dir, name)
	b, err = os.ReadFile(filepath.Join(pair, "manifest.json"))
	if err != nil {
		return "", err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return "", err
	}
	dbPath, err = filepath.Abs(dbPath)
	if err != nil {
		return "", err
	}
	if m.Binary != binary || m.Database != dbPath {
		return "", errors.New("checkpoint belongs to a different installation")
	}
	hashes, err := treeHashes(pair)
	if err != nil {
		return "", err
	}
	delete(hashes, "manifest.json")
	if len(hashes) != len(m.Hashes) {
		return "", errors.New("checkpoint files changed")
	}
	for name, hash := range m.Hashes {
		if hashes[name] != hash {
			return "", errors.New("checkpoint checksum mismatch")
		}
	}
	return pair, nil
}

// Restore replaces both members under an on-disk recovery marker. If interrupted,
// repeating Restore finishes the same pair; startup refuses to use a mixed pair.
// The checkpoint remains available after restoration.
func Restore(dir, binary, dbPath string) error {
	return restoreWithReplace(dir, binary, dbPath, replace)
}

func restoreWithReplace(dir, binary, dbPath string, install func(string, string) error) error {
	pair, err := load(dir, binary, dbPath)
	if err != nil {
		return err
	}
	unproven, err := Unproven(dir, binary, dbPath)
	if err != nil {
		return err
	}
	// Stage on each target filesystem before changing either live member.
	var binStage string
	if !unproven {
		binStage, err = stageCopy(filepath.Join(pair, "binary"), binary)
		if err != nil {
			return err
		}
	}
	clean := true
	defer func() {
		if clean && binStage != "" {
			os.RemoveAll(binStage)
		}
	}()
	dbStage, err := stageCopy(filepath.Join(pair, "database"), dbPath)
	if err != nil {
		return err
	}
	defer func() {
		if clean {
			os.RemoveAll(dbStage)
		}
	}()
	if err := writeSync(filepath.Join(dir, "restoring"), []byte("restore pair before startup\n")); err != nil {
		return err
	}
	clean = false // Preserve displaced files if recovery itself fails.
	// A snapshot is standalone. Preserve displaced WAL/SHM/journal files with the
	// old DB so none can be replayed onto the restored snapshot. The caller holds
	// the daemon lock and all SQLite connections are closed.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Rename(dbPath+suffix, filepath.Join(dbStage, "previous"+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("move database sidecar (retry rollback): %w", err)
		}
	}
	if err := install(dbStage, dbPath); err != nil {
		return fmt.Errorf("restore database (retry rollback): %w", err)
	}
	// Install the old binary last: until then the current executable can recover
	// the marker, even when the retained release predates rollback support.
	if !unproven {
		if err := install(binStage, binary); err != nil {
			return fmt.Errorf("restore binary (retry rollback): %w", err)
		}
	}
	if err := CompleteUpdate(dir); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "restoring")); err != nil {
		return err
	}
	clean = true
	return nil
}

func stageCopy(src, dst string) (string, error) {
	dir, err := os.MkdirTemp(filepath.Dir(dst), ".tmx-restore-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "content")
	if err := copyTree(src, path); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	// Return the enclosing directory; replace consumes its content.
	return dir, nil
}

func replace(stage, dst string) error {
	old := filepath.Join(stage, "previous")
	moved := false
	if err := os.Rename(dst, old); err == nil {
		moved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(filepath.Join(stage, "content"), dst); err != nil {
		if moved {
			if undo := os.Rename(old, dst); undo != nil {
				return errors.Join(err, undo)
			}
		}
		return err
	}
	return nil
}

func writeSync(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("installation root must not be a symlink")
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Mkdir(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(link) || !filepath.IsLocal(filepath.Join(filepath.Dir(rel), link)) {
				return errors.New("bundle symlink escapes installation")
			}
			return os.Symlink(link, target)
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	if !mode.IsRegular() {
		return errors.New("checkpoint requires regular files")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	return errors.Join(err, out.Close())
}

func treeHashes(root string) (map[string]string, error) {
	hashes := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		h := sha256.New()
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			io.WriteString(h, "link:"+link)
		} else {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
		}
		hashes[rel] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	return hashes, err
}
