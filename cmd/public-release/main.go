// public-release builds and publishes headless fleet releases. It never reads a private key.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/release"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

var platforms = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "windows/amd64"}

func names(tag string) []string {
	var out []string
	for _, p := range platforms {
		n := "tmxg_" + tag + "_" + strings.ReplaceAll(p, "/", "_")
		if strings.HasPrefix(p, "windows/") {
			n += ".exe"
		}
		out = append(out, n)
	}
	return out
}

func validTag(tag string) bool {
	_, ok := release.ParseTag(tag)
	return ok
}

func checksums(dir, tag string) ([]byte, error) {
	var sums strings.Builder
	for _, name := range names(tag) {
		p := filepath.Join(dir, name)
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, fmt.Errorf("%s must be a nonempty regular file", name)
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h.Sum(nil)), name)
	}
	return []byte(sums.String()), nil
}

func verify(dir, tag, sigPath, pinsPath string) error {
	if !validTag(tag) {
		return errors.New("expected exact fleet-vX.Y.Z[-rcN] tag")
	}
	var pins config.UpdateConfig
	b, err := os.ReadFile(pinsPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &pins); err != nil {
		return err
	}
	keys, require, err := pins.PinnedKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("no pinned release key")
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return err
	}
	if err := update.VerifyRelease(keys, require, config.PublicUpdateRepo, tag, sums, sig); err != nil {
		return err
	}
	want, err := checksums(dir, tag)
	if err != nil {
		return err
	}
	if string(want) != string(sums) {
		return errors.New("SHA256SUMS differs from the four release binaries (or is not canonical)")
	}
	return nil
}

var run = func(program string, args ...string) error {
	c := hiddenexec.Command(program, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func build(dir, tag string) error {
	if !validTag(tag) {
		return errors.New("expected exact fleet-vX.Y.Z[-rcN] tag")
	}
	if _, err := os.Stat("ui/dist/index.html"); err != nil {
		return errors.New("build the UI first: pnpm --dir ui install --frozen-lockfile && pnpm --dir ui build")
	}
	// Refuse to mix a fresh build with old or already-signed artifacts.
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	for i, platform := range platforms {
		fmt.Println("Building", platform)
		parts := strings.Split(platform, "/")
		c := hiddenexec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X github.com/blackcode-ch/tmx-go/internal/config.Version="+tag, "-o", filepath.Join(dir, names(tag)[i]), ".")
		c.Env = append(os.Environ(), "GOTOOLCHAIN=go1.26.6", "GOENV=off", "GOFLAGS=", "GOWORK=off", "GOEXPERIMENT=", "GOAMD64=v1", "GOARM64=v8.0", "CGO_ENABLED=0", "GOOS="+parts[0], "GOARCH="+parts[1])
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return err
		}
	}
	sums, err := checksums(dir, tag)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), sums, 0o644)
}

// publish snapshots only allowed assets, verifies the snapshot, and uploads that exact set.
// No directory glob, generated notes, source archive, private git metadata, or signing key.
func publish(dir, tag, sigPath, pinsPath string) error {
	if !validTag(tag) {
		return errors.New("expected exact fleet-vX.Y.Z[-rcN] tag")
	}
	stage, err := os.MkdirTemp("", "tmx-public-release-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	assets := append(names(tag), "SHA256SUMS", "SHA256SUMS.sig")
	for _, name := range assets {
		source := filepath.Join(dir, name)
		if name == "SHA256SUMS.sig" {
			source = sigPath
		}
		if err := copyFile(source, filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	if err := verify(stage, tag, filepath.Join(stage, "SHA256SUMS.sig"), pinsPath); err != nil {
		return err
	}
	// --verify-tag requires a pre-existing public tag, established by the release operator.
	args := []string{"release", "create", tag, "--repo", config.PublicUpdateRepo, "--verify-tag", "--draft", "--title", tag, "--notes", "Signed fleet headless binaries. Verify SHA256SUMS.sig against your pinned fleet release key before installing."}
	if strings.Contains(tag, "-rc") {
		args = append(args, "--prerelease")
	}
	for _, name := range assets {
		args = append(args, filepath.Join(stage, name))
	}
	// Existing tags/releases are never deleted or overwritten, including interrupted drafts.
	if err := run("gh", args...); err != nil {
		return err
	}
	// Re-download the draft and verify bytes before exposing it to unauthenticated clients.
	download := filepath.Join(stage, "download")
	if err := os.Mkdir(download, 0o755); err != nil {
		return err
	}
	if err := run("gh", "release", "download", tag, "--repo", config.PublicUpdateRepo, "--dir", download); err != nil {
		return err
	}
	entries, err := os.ReadDir(download)
	if err != nil {
		return err
	}
	if len(entries) != len(assets) {
		return errors.New("draft has unexpected assets; left unpublished")
	}
	if err := verify(download, tag, filepath.Join(download, "SHA256SUMS.sig"), pinsPath); err != nil {
		return err
	}
	return run("gh", "release", "edit", tag, "--repo", config.PublicUpdateRepo, "--draft=false", "--latest=false")
}

func copyFile(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func main() {
	mode := flag.String("mode", "verify", "build, verify, or publish")
	tag := flag.String("tag", "", "fleet release tag")
	dir := flag.String("dir", "release-dist", "directory containing the four binaries and SHA256SUMS")
	sig := flag.String("sig", "", "already-made detached SHA256SUMS.sig")
	pins := flag.String("pins", "", "trusted public pin JSON: {fleet_keys, require}; never private key material")
	flag.Parse()
	var err error
	switch *mode {
	case "build":
		err = build(*dir, *tag)
	case "verify":
		err = verify(*dir, *tag, *sig, *pins)
	case "publish":
		err = publish(*dir, *tag, *sig, *pins)
	default:
		err = errors.New("mode must be build, verify, or publish")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
