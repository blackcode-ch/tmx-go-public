package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// SignFile rebuilds the exact reviewed tag before accessing the existing key.
// source is a local public repository; commit is the independently reviewed SHA.
func SignFile(sumsPath, keyPath, outPath, repo, tag, id, source, commit string) error {
	return signFile(sumsPath, keyPath, outPath, repo, tag, id, source, commit, rebuild)
}

func signFile(sumsPath, keyPath, outPath, repo, tag, id, source, commit string, build func(string, string, string) ([]byte, error)) error {
	if owner, name, _ := strings.Cut(repo, "/"); !repoRx.MatchString(repo) || owner == "." || owner == ".." || name == "." || name == ".." {
		return errors.New("expected owner/repository")
	}
	if _, ok := ParseTag(tag); !ok {
		return errors.New("expected fleet-vX.Y.Z[-rcN] tag")
	}
	if err := validID(id); err != nil {
		return err
	}
	if keyPath == "" || sumsPath == "" || outPath == "" {
		return errors.New("checksum file, existing key file and output path are required")
	}
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	if len(sums) == 0 || len(sums) > 64<<10 {
		return errors.New("checksum file must contain 1..65536 bytes")
	}
	rebuilt, err := build(source, tag, commit)
	if err != nil {
		return err
	}
	if err := compareRebuild(sums, rebuilt); err != nil {
		return err
	}
	key, err := LoadKeyFile(keyPath)
	if err != nil {
		return err
	}
	sig := []byte(update.SignReleaseLine(id, key, repo, tag, sums) + "\n")
	pub := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	if err := update.VerifyRelease([]config.FleetKey{{ID: id, Type: config.KeyEd25519, Key: pub}}, []string{config.KeyEd25519}, repo, tag, sums, sig); err != nil {
		return err
	}
	// Never overwrite a signature, checksum file, or private key by mistake.
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(sig)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(outPath)
	}
	return err
}
