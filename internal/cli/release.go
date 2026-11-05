package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/release"
)

// releaseGitHub is the GitHub the signer talks to (tests replace it; it is gh otherwise).
var releaseGitHub release.GitHub = release.Gh{}

// cmdRelease is the fleet release signer (DESIGN.md "Signing a release"); it runs on the release
// machine only and needs no daemon.
func cmdRelease(e *env, args []string) error {
	const use = "usage: tmx release keygen [--id ID] | tmx release pubkey [--id ID] | tmx release sign TAG [--repo OWNER/NAME] [--id ID] [--reviewer LOGIN] [--dry-run] | tmx release sign-extra whisper-engine-vX.Y.Z [--id ID] [--dry-run] | tmx release keys --serial N --set NEW-SET.json [--id ID] [--repo OWNER/NAME] [--dir DIR]"
	if len(args) == 0 {
		return usageErr("%s", use)
	}
	home, err := release.Home()
	if err != nil {
		return err
	}
	switch args[0] {
	case "keygen":
		fs := newFlags("release keygen")
		id := fs.String("id", config.LegacyFleetKeyID, "key id")
		rest, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return usageErr("%s", use)
		}
		pub, err := release.Keygen(home, *id)
		if err != nil {
			return err
		}
		// The public key only: the private key stays in its file.
		fmt.Fprintf(e.out, "Release key %s created in %s.\n", pub.ID, release.KeyFile(home, pub.ID))
		fmt.Fprintf(e.out, "public key:  %s\nfingerprint: %s\n\nPin it in config.json on every daemon:\n%s\n", pub.Key, pub.Fingerprint, pub.Snippet())
		return nil
	case "pubkey":
		fs := newFlags("release pubkey")
		id := fs.String("id", config.LegacyFleetKeyID, "key id")
		rest, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return usageErr("%s", use)
		}
		pub, err := release.PublicOf(home, *id)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Release key %s in %s.\npublic key:  %s\nfingerprint: %s\n", pub.ID, release.KeyFile(home, pub.ID), pub.Key, pub.Fingerprint)
		return nil
	case "sign":
		fs := newFlags("release sign")
		repo := fs.String("repo", release.DefaultRepo, "repository")
		id := fs.String("id", config.LegacyFleetKeyID, "key id")
		reviewer := fs.String("reviewer", "", "login whose security-review status counts (default: the repo variable FLEET_REVIEW_LOGIN)")
		dry := fs.Bool("dry-run", false, "check every gate and print the signature; upload nothing")
		rest, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return usageErr("%s", use)
		}
		_, err = release.Sign(context.Background(), release.Options{GitHub: releaseGitHub, Home: home, Repo: *repo,
			Tag: rest[0], KeyID: *id, DryRun: *dry, Reviewer: *reviewer, Out: e.out})
		return err
	case "sign-extra":
		// An on-demand extra (the voice engine) built and published unsigned by the public
		// mirror's CI; the repository is fixed to release.PublicRepo (internal/release/extra.go).
		fs := newFlags("release sign-extra")
		id := fs.String("id", config.LegacyFleetKeyID, "key id")
		dry := fs.Bool("dry-run", false, "check every gate and print the signature; upload nothing")
		rest, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return usageErr("%s", use)
		}
		_, err = release.SignExtra(context.Background(), release.ExtraOptions{GitHub: releaseGitHub, Home: home,
			Tag: rest[0], KeyID: *id, DryRun: *dry, Out: e.out})
		return err
	case "keys":
		fs := newFlags("release keys")
		repo := fs.String("repo", release.DefaultRepo, "repository")
		id := fs.String("id", config.LegacyFleetKeyID, "the pinned key that signs the statement")
		serial := fs.Uint64("serial", 0, "the statement's serial, above every one signed before")
		setPath := fs.String("set", "", "the new pin set: JSON {fleet_keys, require}")
		dir := fs.String("dir", ".", "where KEYS and KEYS.sig are written")
		rest, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 0 || *setPath == "" {
			return usageErr("%s", use)
		}
		set, err := os.ReadFile(*setPath)
		if err != nil {
			return err
		}
		return release.SignKeys(release.KeysOptions{Home: home, Repo: *repo, KeyID: *id, Serial: *serial, Set: set, Dir: *dir, Out: e.out})
	default:
		return usageErr("%s", use)
	}
}
