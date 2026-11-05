package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func TestFleetSignatureIdentity(t *testing.T) {
	priv, pub := newKey(t) // ephemeral test fixture, never stored
	other, _ := newKey(t)
	var cfg config.UpdateConfig
	if err := json.Unmarshal([]byte(`{"fleet_public_key":"`+pub+`"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Settings: func() config.UpdateConfig { return cfg }})
	sums := []byte(strings.Repeat("0", 64) + "  fixture\n")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signatureMessage(FleetExtraSignatureContext, u.Repo(), "whisper-engine-v1", sums)))
	raw := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums))
	wrongContext := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, append([]byte("upstream-release-v1\n"), sums...)))
	for _, bad := range []string{sig, "upstream:" + sig, FleetKeyID + ":" + raw, FleetKeyID + ":" + wrongContext, FleetKeyID + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(other, signatureMessage(FleetExtraSignatureContext, u.Repo(), "whisper-engine-v1", sums)))} {
		if _, err := u.VerifySums("whisper-engine-v1", sums, []byte(bad)); err == nil {
			t.Fatal("accepted wrong signer/key identity")
		}
	}
	if _, err := u.VerifySums("whisper-engine-v1", sums, []byte("fleet-release-v1:"+sig)); err != nil {
		t.Fatalf("fleet signature rejected: %v", err)
	}
}

func TestUpdateAutoDefaultsOffInFleetBuild(t *testing.T) {
	for _, c := range []*config.UpdateConfig{nil, {}, {Auto: new(bool)}} {
		if *c.Resolved().Auto {
			t.Fatal("automatic updates enabled by default")
		}
	}
}

func TestFleetSignedContextBinding(t *testing.T) {
	priv, pub := newKey(t)
	keys, err := publicKeys([]string{pub})
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte(strings.Repeat("0", 64) + "  fixture\n")
	const repo, tag = "o/r", "fleet-v1.2.3"
	for _, tc := range []struct {
		name, purpose, repo, tag string
		valid                    bool
	}{
		{"daemon", FleetSignatureContext, repo, tag, true},
		{"extras", FleetExtraSignatureContext, repo, tag, false},
		{"other repository", FleetSignatureContext, "other/repo", tag, false},
		{"other tag", FleetSignatureContext, repo, "fleet-v9.9.9", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sig := []byte(FleetKeyID + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signatureMessage(tc.purpose, tc.repo, tc.tag, sums))))
			err := verifySums(FleetSignatureContext, repo, tag, sums, sig, keys)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if tc.valid && verifySums(FleetExtraSignatureContext, repo, tag, sums, sig, keys) == nil {
				t.Fatal("daemon signature accepted for extras")
			}
		})
	}
}

func TestFleetTagFilter(t *testing.T) {
	for _, tag := range []string{"v99.0.0", "latest", "whisper-engine-v1"} {
		if _, ok := ParseVersion(tag); ok {
			t.Errorf("accepted upstream tag %q", tag)
		}
		u := &Updater{target: target{goos: "linux", goarch: "amd64"}}
		if _, err := u.releaseVersion(release{TagName: tag, Assets: []asset{{Name: "tmx_fleet-v99.0.0_linux_amd64"}}}); err == nil {
			t.Errorf("accepted %q through asset fallback", tag)
		}
	}
	if _, ok := ParseVersion("fleet-v1.2.3"); !ok {
		t.Fatal("fleet version refused")
	}
}

func TestFleetReleaseDiscovery(t *testing.T) {
	u := New(Options{Paths: config.Paths{Home: t.TempDir()}, GH: func() string { return "" }, APIBase: "https://fixture.invalid", HTTP: &http.Client{Transport: releaseVectorTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/o/r/releases" {
			t.Fatalf("forbidden release endpoint: %s", r.URL.Path)
		}
		b, _ := json.Marshal([]release{{TagName: "v99.0.0"}, {TagName: "latest"}, {TagName: "fleet-v1.2.3"}, {TagName: "fleet-v2.0.0", Draft: true}, {TagName: "fleet-v3.0.0", Prerelease: true}, {TagName: "fleet-v2.5.0-rc1"}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})}})
	for _, e := range []string{"TMX_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(e, "")
	}
	// fleet-v2.5.0-rc1 is not marked prerelease, but an rc tag never reaches the stable channel.
	r, err := u.latestRelease(context.Background(), "o/r", "stable")
	if err != nil || r.TagName != "fleet-v1.2.3" {
		t.Fatalf("release = %s, %v", r.TagName, err)
	}
	r, err = u.latestRelease(context.Background(), "o/r", "latest")
	if err != nil || r.TagName != "fleet-v3.0.0" {
		t.Fatalf("fleet prerelease = %s, %v", r.TagName, err)
	}
}

func TestFleetPublicationOnly(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/build.yml")
	if os.IsNotExist(err) && os.Getenv("TMX_PUBLIC_SNAPSHOT") == "1" {
		t.Skip("the release workflow is private; the public CI snapshot doesn't carry it")
	}
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "tags: ['fleet-v*']") || strings.Contains(s, "tags: ['v*']") || strings.Contains(s, "TAG=latest") {
		t.Fatal("workflow publishes outside fleet tag namespace")
	}
	// Option C: the fleet key lives only on Hosta; CI drafts, `tmx release sign` signs.
	if strings.Contains(s, "SIGNING_KEY") || strings.Contains(s, "release-sums.sh") || strings.Contains(s, "publish-release.sh") {
		t.Fatal("CI holds or uses a release signing key, or publishes a fleet release itself")
	}
	if !strings.Contains(s, "name: draft fleet release") || !strings.Contains(s, "fleet-ci-run: ${GITHUB_RUN_ID}") {
		t.Fatal("the draft job the signer's gate (d) checks is missing or renamed")
	}
	if !strings.Contains(s, "if: github.event_name == 'push' && github.ref_type == 'tag' && startsWith(github.ref_name, 'fleet-v')") {
		t.Fatal("publication not gated on fleet tag push")
	}
	b, err = os.ReadFile("../../scripts/publish-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "fleet-v*) edit+=(--latest=false)") {
		t.Fatal("fleet publication can replace upstream latest")
	}
}

func TestFleetAutoOffMakesNoBackgroundRequests(t *testing.T) {
	_, pub := newKey(t)
	dir := t.TempDir()
	u := New(Options{Paths: config.Paths{Home: dir}, Version: "fleet-v1.0.0", Executable: filepath.Join(dir, "tmx.exe"), GOOS: "windows", GOARCH: "amd64", Keys: []string{pub}, FirstCheck: time.Millisecond,
		HTTP: &http.Client{Transport: releaseVectorTransport(func(*http.Request) (*http.Response, error) {
			t.Error("automatic network request while auto is off")
			return nil, context.Canceled
		})}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	u.Run(ctx)
}

func TestFleetBinaryRefusesUpstreamKey(t *testing.T) {
	// Public upstream identity only; no private key is available or needed.
	upstream := "8aFwoLBhKp5XN9ovjPIsR3iDzsjSx0iyY/3o1kIQ0Dc="
	if _, err := publicKeys([]string{upstream}); err == nil {
		t.Fatal("configured upstream key accepted")
	}
	for _, key := range []string{"", "PLACEHOLDER", upstream} {
		u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Version: "fleet-v1.0.0", Executable: "tmx.exe", GOOS: "windows", GOARCH: "amd64", Settings: func() config.UpdateConfig { return config.UpdateConfig{FleetPublicKey: key} }})
		if s := u.Check(context.Background()); s.State != StateOff {
			t.Fatalf("unprovisioned key %q enabled updater", key)
		}
	}
}

func TestUpdateCheckCannotTriggerInstallWithoutOwnerProof(t *testing.T) {
	_, pub := newKey(t)
	on := true
	u := New(Options{Paths: config.Paths{Home: t.TempDir()}, Version: "fleet-v1.0.0", Executable: "tmx.exe", GOOS: "windows", GOARCH: "amd64", Keys: []string{pub}, GH: func() string { return "" },
		Settings: func() config.UpdateConfig { return config.UpdateConfig{Auto: &on} },
		HTTP: &http.Client{Transport: releaseVectorTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"tag_name":"fleet-v2.0.0"}]`))}, nil
		})}})
	for _, e := range []string{"TMX_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(e, "")
	}
	if s := u.Check(context.Background()); !s.Available {
		t.Fatal(s)
	}
	if len(u.installs) != 0 {
		t.Fatal("public check queued an automatic install")
	}
}

func TestFleetAutoDisabledDuringCheckDoesNotInstall(t *testing.T) {
	_, pub := newKey(t)
	on, off := true, false
	cfg := config.UpdateConfig{Auto: &on}
	var u *Updater
	u = New(Options{Paths: config.Paths{Home: t.TempDir()}, Version: "fleet-v1.0.0", Executable: "tmx.exe", GOOS: "windows", GOARCH: "amd64", Keys: []string{pub}, GH: func() string { return "" },
		Settings: func() config.UpdateConfig { return cfg },
		Save:     func(next config.UpdateConfig) error { cfg = next; return nil },
		HTTP: &http.Client{Transport: releaseVectorTransport(func(r *http.Request) (*http.Response, error) {
			// The owner disables auto after discovery starts but before it returns.
			if _, err := u.Configure(&off, nil); err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"tag_name":"fleet-v2.0.0"}]`))}, nil
		})}})
	for _, e := range []string{"TMX_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(e, "")
	}
	if s := u.checkRelease(context.Background(), true); !s.Available || s.Auto {
		t.Fatal(s)
	}
	if len(u.installs) != 0 {
		t.Fatal("in-flight check queued an install after auto was disabled")
	}
}
