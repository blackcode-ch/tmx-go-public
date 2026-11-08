package antechamber

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rename moves a file or folder; tests replace it to simulate a move across drives.
var rename = os.Rename

func lexists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// freeName is folder/name, or 'name (2).ext', 'name (3).ext' … when that exists.
func freeName(folder, name string) (string, error) {
	p := filepath.Join(folder, name)
	if !lexists(p) {
		return p, nil
	}
	stem, e, dot := name, "", false
	if strings.Contains(strings.TrimLeft(name, "."), ".") {
		i := strings.LastIndex(name, ".")
		stem, e, dot = name[:i], name[i+1:], true
	}
	for i := 2; i < 10_000; i++ {
		q := filepath.Join(folder, fmt.Sprintf("%s (%d)", name, i))
		if dot {
			q = filepath.Join(folder, fmt.Sprintf("%s (%d).%s", stem, i, e))
		}
		if !lexists(q) {
			return q, nil
		}
	}
	return "", errf(409, "too many files named %s in %s", name, folder)
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// copyTree copies a file, link or folder (shutil.move's fallback across drives).
func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		t, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(t, dst)
	case st.IsDir():
		if err := os.Mkdir(dst, st.Mode().Perm()|0o700); err != nil {
			return err
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	default:
		if err := copyFile(src, dst, st.Mode().Perm()|0o600); err != nil {
			return err
		}
	}
	_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	return nil
}

// move renames src to dst, or copies then removes src when a rename cannot cross drives
// (tmx-sec, 7 Oct). The source is removed only after a complete copy.
func move(src, dst string) error {
	if err := rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return fmt.Errorf("antechamber: move %s: %w", src, err)
	}
	return os.RemoveAll(src)
}

// placeFile copies the blob to target (another free name if it appeared meanwhile); never overwrites.
func placeFile(blob, target, referrer string) (string, error) {
	dir := filepath.Dir(target)
	tmp := filepath.Join(dir, "."+filepath.Base(target)+"."+randHex(4)+".tmx-part")
	if err := copyFile(blob, tmp, 0o600); err != nil {
		return "", fmt.Errorf("antechamber: place: %w", err)
	}
	_ = os.Chmod(tmp, 0o600)
	noExec(tmp)
	// os.Rename replaces an existing file on every system, so the name is claimed first with an
	// exclusive create, then the copy is renamed over that empty placeholder of ours.
	for tries := 0; ; tries++ {
		if lexists(target) {
			var err error
			if target, err = freeName(dir, filepath.Base(target)); err != nil {
				os.Remove(tmp)
				return "", err
			}
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.Close()
			break
		}
		if !errors.Is(err, os.ErrExist) || tries > 20 {
			os.Remove(tmp)
			return "", fmt.Errorf("antechamber: place: %w", err)
		}
	}
	if err := rename(tmp, target); err != nil {
		os.Remove(tmp)
		os.Remove(target)
		return "", fmt.Errorf("antechamber: place: %w", err)
	}
	markFromElsewhere(target, referrer)
	return target, nil
}

// MtimesName is the root-level sidecar of folders packed for a transfer: {relative path: epoch
// seconds} for files ZIP can't date (before 1980). SafeExtract puts those times back on the files
// it unpacked from the same archive and never extracts the sidecar itself.
const MtimesName = ".tmx-mtimes.json"

// ZipEpoch is 1980-01-01 00:00 UTC: the earliest time a ZIP entry can hold.
const ZipEpoch = 315532800.0

// zipErr marks an error of the archive itself (damaged or unsupported): status 422.
type zipErr struct{ err error }

func (e zipErr) Error() string { return e.err.Error() }

// SafeExtract unpacks a ZIP into a NEW folder dest (or 'dest (2)' … if it exists) and returns it.
// Enforced while unpacking, whatever the archive's headers claim: member count, total size,
// per-member compression ratio, free disk space. Absolute paths, '..', links and encrypted
// entries are refused; names are made safe; nothing is overwritten; no execute bits; every file
// gets the "came from another machine" mark when referrer is given. On any refusal the folder it
// created is removed (it only ever contains what this call wrote) and an *Error is returned.
func SafeExtract(zipPath, dest string, lim Limits, referrer, trust string) (string, error) {
	root, err := freeName(filepath.Dir(dest), filepath.Base(dest))
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		return "", fmt.Errorf("antechamber: extract: %w", err)
	}
	err = extract(zipPath, root, lim, referrer, trust)
	if err == nil {
		return root, nil
	}
	os.RemoveAll(root)
	var be *bombError
	var ze zipErr
	switch {
	case errors.As(err, &be):
		return "", errf(403, "refused: %s", be.msg)
	case errors.As(err, &ze):
		return "", errf(422, "refused: damaged or unsupported ZIP: %v", ze.err)
	}
	return "", err
}

func extract(zipPath, root string, lim Limits, referrer, trust string) error {
	b := newBudget(lim, trust)
	base, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
		return zipErr{err}
	}
	defer zr.Close()
	mtimes := map[string]float64{}
	placed := map[string]string{}
	for _, f := range zr.File {
		dir := strings.HasSuffix(f.Name, "/")
		if f.Name == MtimesName && !dir && f.UncompressedSize64 <= 8<<20 {
			mtimes = readMtimes(f)
			continue
		}
		b.members++
		if b.members > lim.MaxMembers {
			return bomb("more than %d files (archive bomb limit)", lim.MaxMembers)
		}
		if bad := unsafeMember(f.Name); bad != "" {
			return errf(403, "refused: archive entry %s has %s", pyRepr(f.Name), bad)
		}
		if zipIsLink(f) {
			continue // links are never created
		}
		if f.Flags&0x1 != 0 {
			return errf(422, "refused: %s is password-protected", pyRepr(f.Name))
		}
		var rel []string
		for _, p := range strings.Split(strings.ReplaceAll(f.Name, "\\", "/"), "/") {
			if p != "" && p != "." {
				s, _ := CleanName(p)
				rel = append(rel, s)
			}
		}
		if len(rel) == 0 {
			continue
		}
		target := filepath.Join(append([]string{base}, rel...)...)
		if r, err := filepath.Rel(base, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return errf(403, "refused: %s would land outside the folder", pyRepr(f.Name))
		}
		if dir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		keep := ReserveAt(base)
		if lim.MinFree != nil {
			keep = *lim.MinFree
		}
		if free, _, err := diskSpace(base); err == nil && int64(free)-int64(f.UncompressedSize64) < keep {
			return errf(507, "not enough free disk space to unpack it safely")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if target, err = freeName(filepath.Dir(target), filepath.Base(target)); err != nil {
			return err
		}
		if err := unpackOne(f, target, b, lim); err != nil {
			return err
		}
		noExec(target)
		if referrer != "" {
			markFromElsewhere(target, referrer)
		}
		placed[f.Name] = target
	}
	for name, mt := range mtimes { // only files this call unpacked from this archive
		if t, ok := placed[name]; ok && mt >= 0 && mt < ZipEpoch {
			when := time.Unix(0, int64(mt*1e9))
			_ = os.Chtimes(t, when, when)
		}
	}
	return nil
}

func readMtimes(f *zip.File) map[string]float64 {
	out := map[string]float64{}
	rc, err := f.Open()
	if err != nil {
		return out
	}
	defer rc.Close()
	var raw map[string]any
	if err := json.NewDecoder(rc).Decode(&raw); err != nil {
		return map[string]float64{} // a bad sidecar only loses the old times
	}
	for k, v := range raw {
		if n, ok := v.(float64); ok {
			out[k] = n
		}
	}
	return out
}

func unpackOne(f *zip.File, target string, b *budget, lim Limits) error {
	src, err := f.Open()
	if err != nil {
		return zipErr{err}
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer dst.Close()
	buf := make([]byte, 1<<20)
	var n int64
	for {
		k, rerr := src.Read(buf)
		if k > 0 {
			n += int64(k)
			b.total += int64(k)
			if b.total > b.maxTotal {
				return bomb("more than %d MB once unpacked (archive bomb limit)", b.maxTotal>>20)
			}
			if n > lim.RatioFloor && n > lim.MaxRatio*max(int64(f.CompressedSize64), 1) {
				return bomb("%s unpacks more than %dx its packed size (archive bomb)", f.Name, lim.MaxRatio)
			}
			if _, err := dst.Write(buf[:k]); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return zipErr{rerr}
		}
	}
}
