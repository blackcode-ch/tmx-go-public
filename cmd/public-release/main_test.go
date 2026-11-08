package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

func TestVerifyRelease(t *testing.T) {
	for _, failure := range []string{"", "signature", "checksum", "missing binary", "missing signature", "missing sums", "untrusted", "repo", "tag", "empty pins"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			tag := "fleet-v2.0.0-rc1"
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			write := func(name string, b []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range names(tag) {
				write(name, []byte(name))
			}
			sums, err := checksums(dir, tag)
			if err != nil {
				t.Fatal(err)
			}
			write("SHA256SUMS", sums)
			repo, signedTag := config.PublicUpdateRepo, tag
			if failure == "repo" {
				repo = config.DefaultUpdateRepo
			}
			if failure == "tag" {
				signedTag = "fleet-v2.0.0"
			}
			write("SHA256SUMS.sig", []byte(update.SignReleaseLine(config.LegacyFleetKeyID, key, repo, signedTag, sums)))
			if failure == "untrusted" {
				pub, _, _ = ed25519.GenerateKey(rand.Reader)
			}
			pins := config.UpdateConfig{FleetPublicKey: base64.StdEncoding.EncodeToString(pub)}
			if failure == "empty pins" {
				pins = config.UpdateConfig{}
			}
			b, _ := json.Marshal(pins)
			write("pins.json", b)
			switch failure {
			case "signature":
				write("SHA256SUMS.sig", []byte("broken"))
			case "checksum":
				write(names(tag)[0], []byte("changed"))
			case "missing binary":
				os.Remove(filepath.Join(dir, names(tag)[0]))
			case "missing signature":
				os.Remove(filepath.Join(dir, "SHA256SUMS.sig"))
			case "missing sums":
				os.Remove(filepath.Join(dir, "SHA256SUMS"))
			}
			err = verify(dir, tag, filepath.Join(dir, "SHA256SUMS.sig"), filepath.Join(dir, "pins.json"))
			if (err != nil) != (failure != "") {
				t.Fatalf("verify: %v", err)
			}
		})
	}
}

func TestRejectNonReleaseTag(t *testing.T) {
	for _, tag := range []string{"", "v1.2.3", "fleet-v1.2.3-4-gabcd", "fleet-v1.2.3-0-gabcd", "fleet-v1.2.3-dirty", "../fleet-v1.2.3", "fleet-v1.2.3-rc0"} {
		if validTag(tag) {
			t.Fatalf("accepted %q", tag)
		}
	}
}

func TestPublishOnlyVerifiedSnapshot(t *testing.T) {
	for _, corruptDownload := range []bool{false, true} {
		t.Run(fmtBool(corruptDownload), func(t *testing.T) {
			dir := t.TempDir()
			tag := "fleet-v2.0.0"
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range names(tag) {
				write(name, []byte(name))
			}
			sums, err := checksums(dir, tag)
			if err != nil {
				t.Fatal(err)
			}
			write("SHA256SUMS", sums)
			write("offline.sig", []byte(update.SignReleaseLine(config.LegacyFleetKeyID, key, config.PublicUpdateRepo, tag, sums)))
			pins, _ := json.Marshal(config.UpdateConfig{FleetPublicKey: base64.StdEncoding.EncodeToString(pub)})
			write("pins.json", pins)
			write("private-notes.txt", []byte("must never upload"))
			oldRun := run
			defer func() { run = oldRun }()
			calls := 0
			var uploaded []string
			run = func(program string, args ...string) error {
				calls++
				if program != "gh" || !strings.Contains(strings.Join(args, " "), "--repo "+config.PublicUpdateRepo) {
					t.Fatal("wrong publication destination")
				}
				switch args[1] {
				case "create":
					if !strings.Contains(strings.Join(args, " "), "--verify-tag --draft") {
						t.Fatal("must create a draft for an existing tag")
					}
					uploaded = args[len(args)-6:]
					for _, p := range uploaded {
						if filepath.Dir(p) == dir {
							t.Fatal("did not snapshot assets")
						}
						if filepath.Base(p) == "private-notes.txt" || filepath.Base(p) == "pins.json" {
							t.Fatal("uploaded unlisted file")
						}
					}
					// Mutating originals after the first upload cannot change later uploads.
					write(names(tag)[0], []byte("local file modified"))
				case "download":
					dst := args[len(args)-1]
					for _, p := range uploaded {
						if err := copyFile(p, filepath.Join(dst, filepath.Base(p))); err != nil {
							return err
						}
					}
					if corruptDownload {
						return os.WriteFile(filepath.Join(dst, names(tag)[0]), []byte("remote corruption"), 0o600)
					}
				case "edit":
					if corruptDownload {
						t.Fatal("published corrupted draft")
					}
				default:
					t.Fatalf("unexpected gh call: %v", args)
				}
				return nil
			}
			err = publish(dir, tag, filepath.Join(dir, "offline.sig"), filepath.Join(dir, "pins.json"))
			if (err != nil) != corruptDownload {
				t.Fatalf("publish: %v", err)
			}
			wantCalls := 3
			if corruptDownload {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls %d, want %d", calls, wantCalls)
			}
			// An invalid local signature must fail before even creating a draft.
			calls = 0
			write("offline.sig", []byte("invalid"))
			if err := publish(dir, tag, filepath.Join(dir, "offline.sig"), filepath.Join(dir, "pins.json")); err == nil || calls != 0 {
				t.Fatal("invalid input reached GitHub")
			}
		})
	}
}

func fmtBool(b bool) string {
	if b {
		return "corrupt remote"
	}
	return "valid"
}
