package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

type releaseVectorTransport func(*http.Request) (*http.Response, error)

func (f releaseVectorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSprintS22ReleaseFailureVectors(t *testing.T) {
	// Test-only deterministic keys, never release credentials.
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize))
	for _, env := range []string{"TMX_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(env, "")
	}
	for _, tc := range []struct {
		name, want      string
		fetches, probes int
	}{
		{"signed", "", 3, 1},
		{"unsigned", "is not signed", 0, 0},
		{"wrong-key", "not signed with a key", 2, 0},
		{"digest-mismatch", "does not match its checksum", 3, 0},
		{"modified-sums", "not signed with a key", 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tg := target{path: filepath.Join(dir, "tmx.exe"), goos: "windows", goarch: "amd64"}
			old := []byte("inert old artifact")
			if err := os.WriteFile(tg.path, old, 0600); err != nil {
				t.Fatal(err)
			}
			const version = "fleet-v0.2.0"
			name := tg.asset(version)
			payload := []byte("inert release fixture fleet-v0.2.0")
			sums := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(payload), name))
			signer := key
			if tc.name == "wrong-key" {
				signer = other
			}
			sig := []byte(FleetKeyID + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(signer, signatureMessage(FleetSignatureContext, "blackcode-ch/tmx-go", version, sums))))
			if tc.name == "modified-sums" {
				sums = append(sums, '\n')
			}
			files := map[string][]byte{name: payload, "SHA256SUMS": sums, "SHA256SUMS.sig": sig}
			if tc.name == "digest-mismatch" {
				files[name] = []byte("different inert bytes")
			}
			r := release{TagName: version}
			for _, n := range []string{name, "SHA256SUMS", "SHA256SUMS.sig"} {
				if tc.name == "unsigned" && n == "SHA256SUMS.sig" {
					continue
				}
				r.Assets = append(r.Assets, asset{Name: n, URL: "https://release.invalid/" + n})
			}
			fetches, probes := 0, 0
			u := &Updater{o: Options{
				Paths: config.Paths{Home: dir}, Version: "fleet-v0.1.0", Keys: []string{base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))},
				GH: func() string { return "" },
				HTTP: &http.Client{Transport: releaseVectorTransport(func(req *http.Request) (*http.Response, error) {
					fetches++
					b, ok := files[strings.TrimPrefix(req.URL.Path, "/")]
					if !ok || req.URL.Host != "release.invalid" || req.Method != http.MethodGet {
						t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
					}
					if req.Header.Get("Authorization") != "" {
						t.Fatal("fixture acquired credentials")
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}, nil
				})},
				Probe: func(_ context.Context, p string) (string, error) {
					probes++
					b, err := os.ReadFile(p)
					if err != nil || !bytes.Equal(b, payload) {
						t.Fatalf("probe received wrong bytes: %v", err)
					}
					return "tmx " + version, nil
				},
				VerifyBundle: func(context.Context, string) error { t.Fatal("unexpected bundle probe"); return nil },
			}}
			s, err := u.stage(context.Background(), tg, r, version)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if s.version != version {
					t.Fatalf("staged version: %q", s.version)
				}
				if err := os.RemoveAll(s.cleanup); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
			if fetches != tc.fetches || probes != tc.probes {
				t.Fatalf("fetches/probes = %d/%d; want %d/%d", fetches, probes, tc.fetches, tc.probes)
			}
			b, err := os.ReadFile(tg.path)
			if err != nil || !bytes.Equal(b, old) {
				t.Fatalf("old artifact changed: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging leftovers: %v, %v", entries, err)
			}
		})
	}
	for _, platform := range []string{"windows", "bundle", "unix"} {
		for _, failure := range []string{"none", "first-rename", "install-rename", "rollback"} {
			if platform == "unix" && (failure == "first-rename" || failure == "rollback") {
				continue
			}
			t.Run(platform+"/"+failure, func(t *testing.T) {
				tg := target{path: filepath.Join("fixture", "tmx"), goos: "linux"}
				if platform == "windows" {
					tg.goos = "windows"
				}
				if platform == "bundle" {
					tg.bundle = true
					tg.goos = "darwin"
					tg.path = filepath.Join("fixture", "tmxg.app")
				}
				s := staged{path: filepath.Join("fixture", "stage"), cleanup: filepath.Join("fixture", "staging-dir")}
				files := map[string]string{tg.path: "old", s.path: "new"}
				original := map[string]string{tg.path: "old", s.path: "new"}
				old := tg.path + ".old-123"
				if tg.bundle {
					old = filepath.Join(filepath.Dir(tg.path), ".tmxg.app.old-123")
				}
				calls, removed := 0, 0
				injected := errors.New("injected rename failure")
				rename := func(from, to string) error {
					calls++
					if failure == "first-rename" && calls == 1 || (failure == "install-rename" || failure == "rollback") && from == s.path || failure == "rollback" && calls == 3 {
						return injected
					}
					v, ok := files[from]
					if !ok {
						t.Fatalf("rename missing path %s", from)
					}
					files[to] = v
					delete(files, from)
					return nil
				}
				got, err := swapWithOps(tg, s, time.Unix(123, 0), rename, func(p string) error {
					removed++
					if p != s.cleanup {
						t.Fatalf("wrong cleanup: %s", p)
					}
					return nil
				})
				wantCalls := 1
				if platform != "unix" {
					wantCalls = 2
					if failure == "first-rename" {
						wantCalls = 1
					}
					if failure == "install-rename" || failure == "rollback" {
						wantCalls = 3
					}
				}
				if calls != wantCalls {
					t.Fatalf("rename count %d, want %d", calls, wantCalls)
				}
				if failure == "none" {
					if err != nil || got != tg.executable(tg.path) || files[tg.path] != "new" {
						t.Fatalf("swap: %q %v %v", got, err, files)
					}
					if platform != "unix" && files[old] != "old" {
						t.Fatal("old artifact lost")
					}
				} else {
					if err == nil || got != "" {
						t.Fatalf("failure returned %q, %v", got, err)
					}
					if failure == "rollback" {
						if files[old] != "old" || files[s.path] != "new" || !strings.Contains(err.Error(), old) {
							t.Fatalf("recovery artifact/location lost: %v %v", files, err)
						}
					} else if !reflect.DeepEqual(files, original) || !errors.Is(err, injected) {
						t.Fatalf("old artifact not preserved: %v %v", files, err)
					}
				}
				wantRemoved := 0
				if tg.bundle && failure == "none" {
					wantRemoved = 1
				}
				if removed != wantRemoved {
					t.Fatalf("cleanup calls %d, want %d", removed, wantRemoved)
				}
			})
		}
	}
}
