package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Installing a release, in two steps:
//
//   - stage: apply the release's signed KEYS rotation statement if it has one (rotation.go),
//     download the release's SHA256SUMS and SHA256SUMS.sig, check the signature, download
//     this machine's file next to the install (same volume, so the swap is a rename), check its
//     SHA-256, unpack a .app zip, and run the new binary's "version" to see that it starts and is
//     the release it claims to be. Nothing in use changes; a failure leaves the install as it was.
//   - swap: put the staged copy in place with renames. Unix: rename over the executable (running
//     processes keep the old file). Windows: a running .exe cannot be replaced but can be renamed,
//     so the old one moves aside first. A bundle: the old tmxg.app moves aside, the new one takes
//     its name. If the second rename fails the first is undone.
//
// What moved aside (".tmxg.app.old-*", "tmx.exe.old-*") and leftovers of an interrupted stage
// (".tmxg-update-*") are removed at the next start (cleanup); a Windows .exe still running then
// stays until a later start.

const (
	maxAsset    = 300 << 20
	maxSums     = 64 << 10
	maxSig      = 16 << 10
	stagePrefix = ".tmxg-update-"
)

// staged is a downloaded, verified release ready to swap in.
type staged struct {
	version string
	path    string // the staged executable or bundle
	cleanup string // what to remove if it is not used (a file, or the staging directory)
}

func (u *Updater) stage(ctx context.Context, t target, r release, version string) (staged, error) {
	rv, err := u.releaseVersion(r)
	if err != nil {
		return staged{}, err
	}
	if r.TagName != version {
		return staged{}, errors.New("fleet release tag/version mismatch")
	}
	// No downgrade, checked here too and not only where the release was chosen.
	if cur, ok := ParseVersion(u.o.Version); !ok || !cur.Less(rv) {
		return staged{}, fmt.Errorf("release %s is not newer than this tmx (%s)", r.TagName, u.o.Version)
	}
	// A rotation statement first: it may pin the key this release is signed with (rotation.go).
	if err := u.applyKeys(ctx, r); err != nil {
		return staged{}, err
	}
	keys, err := u.trustedKeys()
	if err != nil {
		return staged{}, err
	}
	if keys.empty() {
		return staged{}, errors.New("this build has no release key; updates are off")
	}
	name := t.asset(version)
	a, ok := r.find(name)
	if !ok {
		return staged{}, fmt.Errorf("release %s has no %s (no build for this machine)", r.TagName, name)
	}
	sumsA, ok1 := r.find("SHA256SUMS")
	sigA, ok2 := r.find("SHA256SUMS.sig")
	if !ok1 || !ok2 {
		return staged{}, fmt.Errorf("release %s is not signed (no SHA256SUMS.sig)", r.TagName)
	}
	var sums, sig bytes.Buffer
	if err := u.fetch(ctx, sumsA, &sums, maxSums); err != nil {
		return staged{}, err
	}
	if err := u.fetch(ctx, sigA, &sig, maxSig); err != nil {
		return staged{}, err
	}
	if err := verifySums(FleetSignatureContext, u.Repo(), r.TagName, sums.Bytes(), sig.Bytes(), keys); err != nil {
		return staged{}, fmt.Errorf("release %s: %w", r.TagName, err)
	}
	want, ok := parseSums(sums.Bytes())[name]
	if !ok {
		return staged{}, fmt.Errorf("release %s: SHA256SUMS does not list %s", r.TagName, name)
	}

	dir := filepath.Dir(t.path)
	tmp, err := os.CreateTemp(dir, stagePrefix+"dl-*")
	if err != nil {
		return staged{}, fmt.Errorf("cannot write next to %s: %w", t.path, err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	err = u.fetch(ctx, a, io.MultiWriter(tmp, h), maxAsset)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return staged{}, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return staged{}, fmt.Errorf("%s does not match its checksum (got %s…)", name, got[:12])
	}

	var s staged
	if t.bundle {
		sdir, err := os.MkdirTemp(dir, stagePrefix+version+"-")
		if err != nil {
			return staged{}, err
		}
		app, err := unzipBundle(tmp.Name(), sdir)
		if err != nil {
			os.RemoveAll(sdir)
			return staged{}, err
		}
		s = staged{version: version, path: app, cleanup: sdir}
	} else {
		ext := ""
		if t.goos == "windows" {
			ext = ".exe"
		}
		p := filepath.Join(dir, stagePrefix+version+ext)
		os.Remove(p)
		if err := os.Rename(tmp.Name(), p); err != nil {
			return staged{}, err
		}
		if err := os.Chmod(p, 0o755); err != nil {
			os.Remove(p)
			return staged{}, err
		}
		s = staged{version: version, path: p, cleanup: p}
	}
	if err := u.check(ctx, t, s); err != nil {
		os.RemoveAll(s.cleanup)
		return staged{}, err
	}
	return s, nil
}

// check runs the staged binary ("version") and, for a bundle, the code signature check.
func (u *Updater) check(ctx context.Context, t target, s staged) error {
	if t.bundle && u.o.VerifyBundle != nil {
		if err := u.o.VerifyBundle(ctx, s.path); err != nil {
			return fmt.Errorf("the new tmxg.app fails its signature check: %w", err)
		}
	}
	out, err := u.o.Probe(ctx, t.executable(s.path))
	if err != nil {
		return fmt.Errorf("the new version does not start: %w", err)
	}
	// Exactly the version (as the probation's "to" must equal it, rollback.StartProbation): a
	// build that says fleet-v1.2.3-dirty is not fleet-v1.2.3.
	for _, f := range strings.Fields(out) {
		if f == s.version {
			return nil
		}
	}
	return fmt.Errorf("the new binary says %q, not %s", strings.TrimSpace(out), s.version)
}

// probe runs "<exe> version" (the default Options.Probe).
func probe(ctx context.Context, exe string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := hiddenexec.CommandContext(ctx, exe, "version").CombinedOutput()
	return string(out), err
}

// codesignVerify checks a bundle's (ad-hoc) code signature on macOS (the default VerifyBundle).
func codesignVerify(ctx context.Context, bundle string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	out, err := hiddenexec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", bundle).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unzipBundle unpacks a zipped tmxg.app (ditto -c -k --keepParent) into dir and returns the .app.
func unzipBundle(zipPath, dir string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("the app download is not a zip: %w", err)
	}
	defer zr.Close()
	app := ""
	var total int64
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		// ditto keeps extended attributes as AppleDouble files: in __MACOSX/, or as ._name next to
		// each file. They are not part of the bundle (written out, they break its sealed signature).
		if strings.HasPrefix(f.Name, "__MACOSX/") || strings.HasPrefix(path.Base(f.Name), "._") {
			continue
		}
		if !filepath.IsLocal(name) {
			return "", fmt.Errorf("the app download has a bad path %q", f.Name)
		}
		top := strings.SplitN(filepath.ToSlash(name), "/", 2)[0]
		if !strings.HasSuffix(top, ".app") {
			return "", fmt.Errorf("the app download holds %q, not an .app", top)
		}
		if app == "" {
			app = top
		} else if app != top {
			return "", errors.New("the app download holds more than one .app")
		}
		dst := filepath.Join(dir, name)
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return "", err
			}
			continue
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			link, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return "", err
			}
			// Links stay inside the bundle (Frameworks/Current and the like).
			if filepath.IsAbs(string(link)) || !filepath.IsLocal(filepath.Join(filepath.Dir(name), string(link))) {
				return "", fmt.Errorf("the app download links outside itself (%s)", f.Name)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return "", err
			}
			if err := os.Symlink(string(link), dst); err != nil {
				return "", err
			}
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > 2*maxAsset {
			return "", errors.New("the app download unpacks to too much")
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		perm := mode.Perm() | 0o600
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, err = io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", err
		}
	}
	if app == "" {
		return "", errors.New("the app download is empty")
	}
	return filepath.Join(dir, app), nil
}

// swap puts s in place of t and returns the executable to start.
func swap(t target, s staged) (string, error) {
	if err := keepPrevious(t); err != nil {
		return "", fmt.Errorf("cannot retain previous executable: %w", err)
	}
	return swapWithOps(t, s, time.Now(), os.Rename, os.RemoveAll)
}

// keepPrevious retains an executable beside the install, named by its full SHA-256.
// Unlike .old-* staging files, these copies survive cleanup and later updates.
// A bundle's executable is kept beside the bundle; the rollback checkpoint still
// retains the complete bundle and database for automatic recovery.
func keepPrevious(t target) error {
	src, err := os.Open(t.executable(t.path))
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.path), stagePrefix+"prev-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), src)
	if err == nil {
		err = tmp.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	name := t.path + ".prev-" + hex.EncodeToString(h.Sum(nil))
	if t.goos == "windows" {
		name += ".exe"
	}
	// Link publishes the complete copy without ever overwriting an older backup.
	if err := os.Link(tmp.Name(), name); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, err := os.Open(name)
		if err != nil {
			return err
		}
		defer existing.Close()
		oldHash := sha256.New()
		if _, err := io.Copy(oldHash, existing); err != nil {
			return err
		}
		if !bytes.Equal(oldHash.Sum(nil), h.Sum(nil)) {
			return errors.New("previous executable backup has unexpected content")
		}
	}
	now := time.Now()
	if err := os.Chtimes(name, now, now); err != nil {
		return err
	}
	running, err := os.Executable()
	if err != nil {
		return err
	} // Fail closed: do not prune without identifying ourselves.
	return prunePrevious(t, name, running)
}

// swapWithOps keeps failure injection local to one swap; production uses OS operations.
func swapWithOps(t target, s staged, now time.Time, rename func(string, string) error, removeAll func(string) error) (string, error) {
	stamp := fmt.Sprintf("%d", now.Unix())
	if t.bundle {
		old := filepath.Join(filepath.Dir(t.path), ".tmxg.app.old-"+stamp)
		if err := rename(t.path, old); err != nil {
			return "", fmt.Errorf("cannot move the old tmxg.app aside: %w", err)
		}
		if err := rename(s.path, t.path); err != nil {
			if rerr := rename(old, t.path); rerr != nil {
				return "", fmt.Errorf("cannot put the new tmxg.app in place (%v), nor the old one back (%v): it is at %s", err, rerr, old)
			}
			return "", fmt.Errorf("cannot put the new tmxg.app in place: %w", err)
		}
		removeAll(s.cleanup) // the staging directory, now empty
		return t.executable(t.path), nil
	}
	if t.goos == "windows" {
		old := t.path + ".old-" + stamp
		if err := rename(t.path, old); err != nil {
			return "", fmt.Errorf("cannot move the old %s aside: %w", filepath.Base(t.path), err)
		}
		if err := rename(s.path, t.path); err != nil {
			if rerr := rename(old, t.path); rerr != nil {
				return "", fmt.Errorf("cannot put the new %s in place (%v), nor the old one back (%v): it is at %s", filepath.Base(t.path), err, rerr, old)
			}
			return "", fmt.Errorf("cannot put the new %s in place: %w", filepath.Base(t.path), err)
		}
		return t.path, nil
	}
	if err := rename(s.path, t.path); err != nil {
		return "", fmt.Errorf("cannot replace %s: %w", t.path, err)
	}
	return t.path, nil
}

// cleanup removes what earlier updates left next to the install.
func cleanup(t target) {
	dir := filepath.Dir(t.path)
	pats := []string{filepath.Join(dir, stagePrefix+"*")}
	if t.bundle {
		pats = append(pats, filepath.Join(dir, ".tmxg.app.old-*"))
	} else {
		pats = append(pats, t.path+".old-*")
	}
	for _, p := range pats {
		m, _ := filepath.Glob(p)
		for _, f := range m {
			os.RemoveAll(f) // a running Windows .exe refuses: it goes at a later start
		}
	}
}
