package release

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

func TestSignFileRebuildGate(t *testing.T) {
	for _, failure := range []string{"mismatch", "build failed"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			sums, out := filepath.Join(dir, "SHA256SUMS"), filepath.Join(dir, "sig")
			if err := os.WriteFile(sums, []byte("ci bytes\n"), 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			err := signFile(sums, "missing-key", out, config.PublicUpdateRepo, "fleet-v1.0.0", config.LegacyFleetKeyID, "source", "commit", func(source, tag, commit string) ([]byte, error) {
				called = true
				if source != "source" || commit != "commit" || tag != "fleet-v1.0.0" {
					t.Fatal("lost source binding")
				}
				if failure == "build failed" {
					return nil, errors.New("build failed")
				}
				return []byte("local bytes\n"), nil
			})
			if !called || err == nil {
				t.Fatal("rebuild gate skipped")
			}
			if failure == "mismatch" && (!strings.Contains(err.Error(), "ci bytes") || !strings.Contains(err.Error(), "local bytes")) {
				t.Fatal("missing diff", err)
			}
			if failure == "build failed" && !strings.Contains(err.Error(), failure) {
				t.Fatal(err)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("signature created")
			}
		})
	}
	if err := compareRebuild([]byte("same\n"), []byte("same\n")); err != nil {
		t.Fatal(err)
	}
	if err := compareRebuild([]byte("same\n"), []byte("same\r\n")); err == nil {
		t.Fatal("comparison is not byte exact")
	}
}

func TestRebuildRejectsWrongTaggedCommit(t *testing.T) {
	source := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := hiddenexec.Command("git", args...)
		c.Dir = source
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	git("tag", "fleet-v1.0.0")
	sha := git("rev-parse", "HEAD")
	_, err := rebuild(source, "fleet-v1.0.0", strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "not reviewed commit") || !strings.Contains(err.Error(), sha) {
		t.Fatal(err)
	}
	_, err = rebuild(source, "fleet-v1.0.1", sha)
	if err == nil {
		t.Fatal("missing tag accepted")
	}
}

func TestRebuildUsesCleanTaggedSource(t *testing.T) {
	source := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := rebuildCommand(source, "git", args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	write(filepath.Join(source, "go.mod"), "module fixture\n\ngo 1.26.0\n\ntoolchain go1.26.6\n")
	write(filepath.Join(source, "tracked"), "reviewed source")
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture")
	git("tag", "fleet-v1.0.0")
	sha := git("rev-parse", "HEAD")
	write(filepath.Join(source, "tracked"), "dirty source")
	write(filepath.Join(source, "untracked"), "unreviewed source")
	var commands []string
	sums, err := rebuildWithCommand(source, "fleet-v1.0.0", sha, func(dir, program string, args ...string) (string, error) {
		if program == "git" {
			return rebuildCommand(dir, program, args...)
		}
		commands = append(commands, program+" "+strings.Join(args, " "))
		b, err := os.ReadFile(filepath.Join(dir, "tracked"))
		if err != nil || string(b) != "reviewed source" {
			t.Fatal("rebuilt dirty source", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "untracked")); !os.IsNotExist(err) {
			t.Fatal("untracked source copied")
		}
		if program == "go" {
			if err := os.Mkdir(filepath.Join(dir, "release-dist"), 0700); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(dir, "release-dist", "SHA256SUMS"), "rebuilt manifest\n")
		}
		return "", nil
	})
	if err != nil || string(sums) != "rebuilt manifest\n" {
		t.Fatal(string(sums), err)
	}
	want := []string{"pnpm --dir ui install --offline --frozen-lockfile", "pnpm --dir ui build", "go run ./cmd/public-release -mode build -tag fleet-v1.0.0 -dir release-dist"}
	if strings.Join(commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("build recipe: %v", commands)
	}
}
