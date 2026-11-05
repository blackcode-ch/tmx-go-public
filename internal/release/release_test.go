package release

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

const (
	repo   = "blackcode-ch/tmx-go"
	tag    = "fleet-v1.2.0"
	commit = "1111111111111111111111111111111111111111"
	head   = "2222222222222222222222222222222222222222"
	uplURL = "https://uploads.github.com/repos/blackcode-ch/tmx-go/releases/7/assets{?name,label}"
)

var sumsBody = []byte(strings.Repeat("a", 64) + "  tmx_fleet-v1.2.0_windows_amd64.zip\n" + strings.Repeat("b", 64) + "  tmx_fleet-v1.2.0_linux_amd64\n")

// fakeGH is GitHub as the signer sees it: JSON answers by path, no network.
type fakeGH struct {
	t        *testing.T
	answers  map[string]any
	upload   []string // upload URLs
	body     [][]byte
	artifact []byte // the zip of run 900's fleet-release-sums
	patch    []string
}

func (f *fakeGH) Get(_ context.Context, path string) ([]byte, error) {
	a, ok := f.answers[path]
	if !ok {
		return nil, fmt.Errorf("fake gh: 404 %s", path)
	}
	return json.Marshal(a)
}

func (f *fakeGH) Download(_ context.Context, path string) ([]byte, error) {
	switch path {
	case "repos/" + repo + "/releases/assets/70":
		return sumsBody, nil
	case "repos/" + repo + "/actions/artifacts/80/zip":
		return f.artifact, nil
	}
	return nil, fmt.Errorf("fake gh: no asset %s", path)
}

func (f *fakeGH) Patch(_ context.Context, path string, body []byte) error {
	f.patch = append(f.patch, path+" "+string(body))
	return nil
}

// zipOf is an artifact zip holding the named files.
func zipOf(t *testing.T, files ...string) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for i := 0; i+1 < len(files); i += 2 {
		f, err := w.Create(files[i])
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(files[i+1]))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *fakeGH) Upload(_ context.Context, url string, data []byte) error {
	f.upload, f.body = append(f.upload, url), append(f.body, append([]byte(nil), data...))
	return nil
}

type obj = map[string]any

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func sumsAssetJSON() obj {
	return obj{"id": 70, "name": "SHA256SUMS", "state": "uploaded", "created_at": t0.Add(5 * time.Minute),
		"uploader": obj{"login": "github-actions[bot]", "type": "Bot"}}
}

// happyGH is a repository where every gate passes for tag.
func happyGH(t *testing.T) *fakeGH {
	r := "repos/" + repo
	return &fakeGH{t: t, answers: map[string]any{
		r + "/git/ref/tags/" + tag:                               obj{"object": obj{"type": "tag", "sha": "3333333333333333333333333333333333333333"}},
		r + "/git/tags/3333333333333333333333333333333333333333": obj{"object": obj{"type": "commit", "sha": commit}},
		r + "/branches/fleet%2Fmain": obj{"commit": obj{"sha": head}, "protection": obj{"enabled": true,
			"required_status_checks": obj{"contexts": []string{"test (ubuntu)", "lint"}, "checks": []obj{{"context": "test (ubuntu)", "app_id": 15368}, {"context": "lint", "app_id": nil}}}}},
		r + "/compare/" + commit + "..." + head: obj{"status": "ahead"},
		r + "/commits/" + commit + "/check-runs?per_page=100": obj{"total_count": 3, "check_runs": []obj{
			{"id": 1, "name": "test (ubuntu)", "status": "completed", "conclusion": "failure", "app": obj{"id": 15368}},
			{"id": 2, "name": "test (ubuntu)", "status": "completed", "conclusion": "success", "app": obj{"id": 15368}}, // the re-run
			{"id": 3, "name": "lint", "status": "completed", "conclusion": "skipped", "app": obj{"id": 15368}}}},
		r + "/commits/" + commit + "/status?per_page=100": obj{"statuses": []obj{{"context": "security-review", "state": "success"}}},
		r + "/actions/variables/FLEET_REVIEW_LOGIN":       obj{"name": "FLEET_REVIEW_LOGIN", "value": "go-reviewer"},
		r + "/commits/" + commit + "/statuses?per_page=100&page=1": []obj{
			{"id": 12, "context": "security-review", "state": "success", "creator": obj{"login": "go-reviewer"}},
			{"id": 11, "context": "ci", "state": "success", "creator": obj{"login": "someone"}},
			{"id": 10, "context": "security-review", "state": "failure", "creator": obj{"login": "go-reviewer"}}},
		r + "/releases?per_page=100&page=1": []obj{
			{"id": 7, "tag_name": tag, "draft": true, "target_commitish": commit, "upload_url": uplURL, "body": "Draft by CI.\nfleet-ci-run: 900\n",
				"assets": []obj{sumsAssetJSON(), {"id": 71, "name": "tmx_fleet-v1.2.0_linux_amd64", "state": "uploaded"}}},
			{"id": 6, "tag_name": "fleet-v1.1.0", "draft": false},
			{"id": 5, "tag_name": "fleet-v1.2.0-rc2", "draft": false},
			{"id": 4, "tag_name": "v9.9.9", "draft": false}, // not a fleet release
		},
		r + "/actions/runs/900": obj{"id": 900, "path": ".github/workflows/build.yml", "head_branch": tag, "head_sha": commit,
			"event": "push", "status": "completed", "conclusion": "success"},
		r + "/actions/runs/900/jobs?filter=all&per_page=100": obj{"total_count": 3, "jobs": []obj{
			{"run_id": 900, "name": "build release artifacts", "head_sha": commit, "status": "completed", "conclusion": "success",
				"started_at": t0, "completed_at": t0.Add(4 * time.Minute)},
			{"run_id": 900, "name": "draft fleet release", "head_sha": commit, "status": "completed", "conclusion": "failure", // attempt 1
				"started_at": t0.Add(4 * time.Minute), "completed_at": t0.Add(4*time.Minute + 30*time.Second)},
			{"run_id": 900, "name": "draft fleet release", "head_sha": commit, "status": "completed", "conclusion": "success",
				"started_at": t0.Add(4*time.Minute + 40*time.Second), "completed_at": t0.Add(6 * time.Minute)}}},
		r + "/actions/runs/900/artifacts?name=fleet-release-sums&per_page=100": obj{"total_count": 1, "artifacts": []obj{
			{"id": 80, "name": "fleet-release-sums", "expired": false, "size_in_bytes": 300, "workflow_run": obj{"id": 900, "head_sha": commit}}}},
	}, artifact: zipOf(t, "SHA256SUMS", string(sumsBody))}
}

// setup makes a release home with a key and returns the pinned key the daemons would have.
func setup(t *testing.T) (string, config.FleetKey) {
	t.Helper()
	home := t.TempDir()
	pub, err := Keygen(home, config.LegacyFleetKeyID)
	if err != nil {
		t.Fatal(err)
	}
	return home, config.FleetKey{ID: pub.ID, Type: config.KeyEd25519, Key: pub.Key}
}

func opts(home string, gh GitHub, out *bytes.Buffer) Options {
	return Options{GitHub: gh, Home: home, Repo: repo, Tag: tag, Out: out, Now: func() time.Time { return t0.Add(time.Hour) }}
}

func TestKeygenWritesOnceAndNeverPrintsThePrivateKey(t *testing.T) {
	home := t.TempDir()
	pub, err := Keygen(home, "fleet-release-v1")
	if err != nil {
		t.Fatal(err)
	}
	path := KeyFile(home, "fleet-release-v1")
	if filepath.Dir(path) != filepath.Join(home, "secrets") {
		t.Fatal(path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := LoadKey(home, "fleet-release-v1")
	if err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)) != pub.Key || len(pub.Fingerprint) != 12 {
		t.Fatalf("public key %+v does not match the file", pub)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("key file mode %v", fi.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(home, "secrets", ".*")); len(left) != 0 {
		t.Fatalf("staging files left: %v", left)
	}
	// Never overwritten.
	if _, err := Keygen(home, "fleet-release-v1"); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("second keygen: %v", err)
	}
	if b2, _ := os.ReadFile(path); !bytes.Equal(b, b2) {
		t.Fatal("key overwritten")
	}
	if _, err := Keygen(home, "Bad ID!"); err == nil {
		t.Fatal("bad id accepted")
	}
	// What keygen prints carries no private material in any encoding.
	out := pub.Snippet() + pub.Key + pub.Fingerprint
	seed := priv.Seed()
	for _, enc := range []string{strings.TrimSpace(string(b)), base64.StdEncoding.EncodeToString(priv),
		base64.RawURLEncoding.EncodeToString(seed), fmt.Sprintf("%x", seed)} {
		if strings.Contains(out, enc) {
			t.Fatal("keygen output contains the private key")
		}
	}
	var snip struct {
		Update config.UpdateConfig `json:"update"`
	}
	if err := json.Unmarshal([]byte(pub.Snippet()), &snip); err != nil {
		t.Fatal(err)
	}
	if keys, _, err := snip.Update.PinnedKeys(); err != nil || len(keys) != 1 || keys[0].Key != pub.Key {
		t.Fatalf("snippet does not pin the key: %v %v", keys, err)
	}
}

func TestHomeOverrideAndTestModeGuard(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	if h, err := Home(); err != nil || h != dir {
		t.Fatalf("home %q %v", h, err)
	}
	if h, _ := defaultHome(); h != dir {
		t.Fatal("the override is ignored in a test binary")
	}
	t.Setenv(EnvHome, "")
	t.Setenv("LOCALAPPDATA", `C:\definitely-not-temp`)
	t.Setenv("XDG_STATE_HOME", "/definitely-not-temp")
	t.Setenv("HOME", "/definitely-not-temp")
	if _, err := Home(); err == nil {
		t.Fatal("the real release home is reachable from a test")
	}
}

// The key test: what the signer uploads verifies with the daemons' verifier.
func TestSignRoundTripsThroughTheVerifier(t *testing.T) {
	home, pin := setup(t)
	gh := happyGH(t)
	var out bytes.Buffer
	res, err := Sign(context.Background(), opts(home, gh, &out))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(gh.upload) != 1 || gh.upload[0] != "https://uploads.github.com/repos/blackcode-ch/tmx-go/releases/7/assets?name=SHA256SUMS.sig" {
		t.Fatalf("uploads %v", gh.upload)
	}
	if len(gh.patch) != 1 || gh.patch[0] != `repos/`+repo+`/releases/7 {"draft":false,"make_latest":"false","prerelease":false}` || !res.Published {
		t.Fatalf("publish %v", gh.patch)
	}
	sig := gh.body[0]
	if !strings.HasPrefix(string(sig), "fleet-release-v1:ed25519:") || strings.Count(string(sig), "\n") != 1 || !res.Uploaded {
		t.Fatalf("sig %q", sig)
	}
	if err := update.VerifyRelease([]config.FleetKey{pin}, nil, repo, tag, sumsBody, sig); err != nil {
		t.Fatalf("the verifier refuses the signer's signature: %v", err)
	}
	// Bound to repo, tag and sums.
	if update.VerifyRelease([]config.FleetKey{pin}, nil, repo, "fleet-v1.2.1", sumsBody, sig) == nil ||
		update.VerifyRelease([]config.FleetKey{pin}, nil, "other/repo", tag, sumsBody, sig) == nil ||
		update.VerifyRelease([]config.FleetKey{pin}, nil, repo, tag, append(sumsBody, '\n'), sig) == nil {
		t.Fatal("signature not bound to repo/tag/sums")
	}
	log, err := ReadLog(home)
	if err != nil || len(log) != 1 || log[0].Tag != tag || log[0].Commit != commit || log[0].SumsSHA256 != res.SumsSHA256 {
		t.Fatalf("log %+v %v", log, err)
	}
	// Signing it again is refused (no re-signing, no downgrade).
	gh2 := happyGH(t)
	if _, err := Sign(context.Background(), opts(home, gh2, &out)); !isGate(err, "e") || len(gh2.upload) != 0 {
		t.Fatalf("re-sign: %v", err)
	}
	// The private key never reaches the output.
	priv, _ := LoadKey(home, pin.ID)
	if strings.Contains(out.String(), base64.StdEncoding.EncodeToString(priv.Seed())) {
		t.Fatal("output contains the private key")
	}
}

func TestSignDryRunUploadsNothing(t *testing.T) {
	home, pin := setup(t)
	gh := happyGH(t)
	var out bytes.Buffer
	o := opts(home, gh, &out)
	o.DryRun = true
	res, err := Sign(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.upload) != 0 || len(gh.patch) != 0 || res.Uploaded || res.Published {
		t.Fatal("dry run uploaded or published")
	}
	if log, _ := ReadLog(home); len(log) != 0 {
		t.Fatal("dry run logged")
	}
	if !strings.Contains(out.String(), "dry run") || update.VerifyRelease([]config.FleetKey{pin}, nil, repo, tag, sumsBody, []byte(res.SigLine)) != nil {
		t.Fatalf("dry run: %s", out.String())
	}
}

func isGate(err error, gate string) bool {
	var g *GateError
	return errors.As(err, &g) && g.Gate == gate
}

func TestSignRefusesEachGate(t *testing.T) {
	r := "repos/" + repo
	rels := func(gh *fakeGH) []obj { return gh.answers[r+"/releases?per_page=100&page=1"].([]obj) }
	draft := func(gh *fakeGH) obj { return rels(gh)[0] }
	for _, tc := range []struct {
		name, gate, want string
		edit             func(gh *fakeGH)
	}{
		{"not on fleet/main", "a", "not on fleet/main", func(gh *fakeGH) {
			gh.answers[r+"/compare/"+commit+"..."+head] = obj{"status": "diverged"}
		}},
		{"tag is a tree", "a", "does not name a commit", func(gh *fakeGH) {
			gh.answers[r+"/git/tags/3333333333333333333333333333333333333333"] = obj{"object": obj{"type": "tree", "sha": commit}}
		}},
		{"required check failed", "b", `"test (ubuntu)"`, func(gh *fakeGH) {
			runs := gh.answers[r+"/commits/"+commit+"/check-runs?per_page=100"].(obj)
			runs["check_runs"] = append(runs["check_runs"].([]obj), obj{"id": 9, "name": "test (ubuntu)", "status": "completed", "conclusion": "failure", "app": obj{"id": 15368}})
			runs["total_count"] = 4
		}},
		{"required check running", "b", "in_progress", func(gh *fakeGH) {
			gh.answers[r+"/commits/"+commit+"/check-runs?per_page=100"] = obj{"total_count": 2, "check_runs": []obj{
				{"id": 2, "name": "test (ubuntu)", "status": "in_progress", "app": obj{"id": 15368}}, {"id": 3, "name": "lint", "status": "completed", "conclusion": "success", "app": obj{"id": 15368}}}}
		}},
		{"required check missing", "b", `"lint" on 111111111111 is missing`, func(gh *fakeGH) {
			gh.answers[r+"/commits/"+commit+"/check-runs?per_page=100"] = obj{"total_count": 1, "check_runs": []obj{
				{"id": 2, "name": "test (ubuntu)", "status": "completed", "conclusion": "success", "app": obj{"id": 15368}}}}
		}},
		{"same name by another app", "b", `"lint" on 111111111111 is missing`, func(gh *fakeGH) {
			gh.answers[r+"/commits/"+commit+"/check-runs?per_page=100"] = obj{"total_count": 2, "check_runs": []obj{
				{"id": 2, "name": "test (ubuntu)", "status": "completed", "conclusion": "success", "app": obj{"id": 15368}},
				{"id": 3, "name": "lint", "status": "completed", "conclusion": "success", "app": obj{"id": 999}}}}
		}},
		{"pinned app overrides actions", "b", `"test (ubuntu)"`, func(gh *fakeGH) {
			gh.answers[r+"/branches/fleet%2Fmain"].(obj)["protection"].(obj)["required_status_checks"].(obj)["checks"] =
				[]obj{{"context": "test (ubuntu)", "app_id": 4242}, {"context": "lint"}}
		}},
		{"no required checks", "b", "no required checks", func(gh *fakeGH) {
			gh.answers[r+"/branches/fleet%2Fmain"] = obj{"commit": obj{"sha": head}, "protection": obj{"enabled": false}}
		}},
		{"no security review", "c", "is missing", func(gh *fakeGH) {
			gh.answers[r+"/commits/"+commit+"/statuses?per_page=100&page=1"] = []obj{}
		}},
		{"review by another creator", "c", `set by "mallory"`, func(gh *fakeGH) {
			gh.answers[r+"/commits/"+commit+"/statuses?per_page=100&page=1"] = []obj{
				{"id": 20, "context": "security-review", "state": "success", "creator": obj{"login": "mallory"}}}
		}},
		{"latest review failed after a success", "c", "is failure", func(gh *fakeGH) {
			gh.answers[r+"/commits/"+commit+"/statuses?per_page=100&page=1"] = []obj{
				{"id": 30, "context": "security-review", "state": "failure", "creator": obj{"login": "go-reviewer"}},
				{"id": 12, "context": "security-review", "state": "success", "creator": obj{"login": "go-reviewer"}}}
		}},
		{"latest review on page 2", "c", "is failure", func(gh *fakeGH) {
			var page1 []obj
			for i := 0; i < 100; i++ {
				page1 = append(page1, obj{"id": 100 + i, "context": "ci", "state": "success", "creator": obj{"login": "x"}})
			}
			page1[50] = obj{"id": 40, "context": "security-review", "state": "success", "creator": obj{"login": "go-reviewer"}}
			gh.answers[r+"/commits/"+commit+"/statuses?per_page=100&page=1"] = page1
			gh.answers[r+"/commits/"+commit+"/statuses?per_page=100&page=2"] = []obj{
				{"id": 41, "context": "security-review", "state": "failure", "creator": obj{"login": "go-reviewer"}}}
		}},
		{"no reviewer configured", "c", "pass --reviewer", func(gh *fakeGH) {
			delete(gh.answers, r+"/actions/variables/FLEET_REVIEW_LOGIN")
		}},
		{"empty reviewer variable", "c", "is empty", func(gh *fakeGH) {
			gh.answers[r+"/actions/variables/FLEET_REVIEW_LOGIN"] = obj{"value": ""}
		}},
		{"no release", "d", "no release", func(gh *fakeGH) { gh.answers[r+"/releases?per_page=100&page=1"] = rels(gh)[1:] }},
		{"published higher on page 2", "e", "published fleet-v2.0.0", func(gh *fakeGH) {
			page1 := rels(gh)
			for len(page1) < 100 {
				page1 = append(page1, obj{"id": 1000 + len(page1), "tag_name": "fleet-v0.0.1", "draft": false})
			}
			gh.answers[r+"/releases?per_page=100&page=1"] = page1
			gh.answers[r+"/releases?per_page=100&page=2"] = []obj{{"id": 1, "tag_name": "fleet-v2.0.0", "draft": false}}
		}},
		{"already published", "d", "already published", func(gh *fakeGH) { draft(gh)["draft"] = false }},
		{"other target", "d", "targets", func(gh *fakeGH) { draft(gh)["target_commitish"] = "feature" }},
		{"already signed", "d", "already has SHA256SUMS.sig", func(gh *fakeGH) {
			draft(gh)["assets"] = append(draft(gh)["assets"].([]obj), obj{"id": 72, "name": "SHA256SUMS.sig", "state": "uploaded"})
		}},
		{"no sums", "d", "no uploaded SHA256SUMS", func(gh *fakeGH) { draft(gh)["assets"] = []obj{} }},
		{"sums by a person", "d", `uploaded by "mallory"`, func(gh *fakeGH) {
			a := sumsAssetJSON()
			a["uploader"] = obj{"login": "mallory", "type": "User"}
			draft(gh)["assets"] = []obj{a}
		}},
		{"sums outside the draft job", "d", `not uploaded during a successful "draft fleet release" job`, func(gh *fakeGH) {
			a := sumsAssetJSON()
			a["created_at"] = t0.Add(2 * time.Minute) // during the build job: another step or workflow
			draft(gh)["assets"] = []obj{a}
		}},
		{"sums during the failed draft attempt", "d", "not uploaded during", func(gh *fakeGH) {
			a := sumsAssetJSON()
			a["created_at"] = t0.Add(4*time.Minute + 10*time.Second)
			draft(gh)["assets"] = []obj{a}
		}},
		{"draft job renamed", "d", "not uploaded during", func(gh *fakeGH) {
			jobs := gh.answers[r+"/actions/runs/900/jobs?filter=all&per_page=100"].(obj)["jobs"].([]obj)
			jobs[2]["name"] = "draft fleet release (copy)"
		}},
		{"too many jobs", "d", "more than one page", func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900/jobs?filter=all&per_page=100"].(obj)["total_count"] = 101
		}},
		{"no run named", "d", "names 0 CI runs", func(gh *fakeGH) { draft(gh)["body"] = "notes" }},
		{"two runs named", "d", "names 2 CI runs", func(gh *fakeGH) { draft(gh)["body"] = "fleet-ci-run: 900\nfleet-ci-run: 901\n" }},
		{"run of another workflow", "d", `of ".github/workflows/evil.yml"`, func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900"].(obj)["path"] = ".github/workflows/evil.yml"
		}},
		{"run of another ref", "d", "not the push of", func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900"].(obj)["head_branch"] = "fleet/main"
		}},
		{"run on another commit", "d", "not the push of", func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900"].(obj)["head_sha"] = head
		}},
		{"run by dispatch", "d", "not the push of", func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900"].(obj)["event"] = "workflow_dispatch"
		}},
		{"run failed", "d", "not completed/success", func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900"].(obj)["conclusion"] = "failure"
		}},
		{"unknown run", "", "404", func(gh *fakeGH) { draft(gh)["body"] = "fleet-ci-run: 901" }},
		{"sums differ from the run's artifact", "d", "differs from the one run 900 computed", func(gh *fakeGH) {
			gh.artifact = zipOf(gh.t, "SHA256SUMS", strings.Repeat("c", 64)+"  tmx_fleet-v1.2.0_linux_amd64\n")
		}},
		{"artifact holds more", "d", "does not hold just SHA256SUMS", func(gh *fakeGH) {
			gh.artifact = zipOf(gh.t, "SHA256SUMS", string(sumsBody), "x", "y")
		}},
		{"artifact not a zip", "d", "not a zip", func(gh *fakeGH) { gh.artifact = sumsBody }},
		{"no artifact", "d", "0 artifacts named", func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900/artifacts?name=fleet-release-sums&per_page=100"] = obj{"total_count": 0, "artifacts": []obj{}}
		}},
		{"artifact expired", "d", "expired", func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900/artifacts?name=fleet-release-sums&per_page=100"].(obj)["artifacts"].([]obj)[0]["expired"] = true
		}},
		{"artifact of another run", "d", "not of that run", func(gh *fakeGH) {
			gh.answers[r+"/actions/runs/900/artifacts?name=fleet-release-sums&per_page=100"].(obj)["artifacts"].([]obj)[0]["workflow_run"] = obj{"id": 901, "head_sha": commit}
		}},
		{"published is higher", "e", "published fleet-v1.3.0-rc2", func(gh *fakeGH) {
			rels(gh)[2]["tag_name"] = "fleet-v1.3.0-rc2"
		}},
		{"two releases for the tag", "d", "more than one release", func(gh *fakeGH) { rels(gh)[1]["tag_name"] = tag }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := setup(t)
			gh := happyGH(t)
			tc.edit(gh)
			var out bytes.Buffer
			_, err := Sign(context.Background(), opts(home, gh, &out))
			if err == nil || (tc.gate != "" && !isGate(err, tc.gate)) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want gate (%s) %q, got %v", tc.gate, tc.want, err)
			}
			if len(gh.upload) != 0 || len(gh.patch) != 0 {
				t.Fatal("uploaded or published despite a refusal")
			}
			if log, _ := ReadLog(home); len(log) != 0 {
				t.Fatal("logged despite a refusal")
			}
		})
	}
}

func TestSignRefusesADowngradeFromTheLog(t *testing.T) {
	home, _ := setup(t)
	if err := appendLog(home, LogEntry{Tag: "fleet-v1.3.0-rc1", Commit: head, Time: t0}); err != nil {
		t.Fatal(err)
	}
	gh := happyGH(t)
	_, err := Sign(context.Background(), opts(home, gh, &bytes.Buffer{}))
	if !isGate(err, "e") || !strings.Contains(err.Error(), "fleet-v1.3.0-rc1") || len(gh.upload) != 0 {
		t.Fatalf("downgrade: %v", err)
	}
	// A corrupt log fails closed.
	os.WriteFile(LogFile(home), []byte("{not json\n"), 0o600)
	if _, err := Sign(context.Background(), opts(home, happyGH(t), &bytes.Buffer{})); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt log: %v", err)
	}
}

func TestSignArgumentsAndMissingKey(t *testing.T) {
	home := t.TempDir()
	for _, o := range []Options{
		{Tag: "v1.2.0"}, {Tag: "fleet-v1.2"}, {Tag: "fleet-v1.2.0-rc0"}, {Tag: "fleet-v1.2.0-rc01"}, {Tag: tag, Repo: "no-slash"}, {Tag: tag, Repo: "../.."}, {Tag: tag, Repo: "o/.."}, {Tag: tag, Repo: "./r"}, {Tag: tag, Repo: "o/r/x"}, {Tag: tag + "\nx"},
	} {
		o.GitHub, o.Home = happyGH(t), home
		if _, err := Sign(context.Background(), o); !isGate(err, "") {
			t.Errorf("%+v: %v", o, err)
		}
	}
	if _, err := Sign(context.Background(), Options{GitHub: happyGH(t), Home: home, Tag: tag}); err == nil || !strings.Contains(err.Error(), "keygen") {
		t.Fatalf("missing key: %v", err)
	}
}

// An rc is published as a prerelease, so only the "latest" channel installs it.
func TestSignPublishesAnRCAsPrerelease(t *testing.T) {
	home, _ := setup(t)
	gh := happyGH(t)
	const rc = "fleet-v1.3.0-rc1"
	r := "repos/" + repo
	gh.answers[r+"/git/ref/tags/"+rc] = gh.answers[r+"/git/ref/tags/"+tag]
	gh.answers[r+"/actions/runs/900"].(obj)["head_branch"] = rc
	gh.answers[r+"/releases?per_page=100&page=1"].([]obj)[0]["tag_name"] = rc
	o := opts(home, gh, &bytes.Buffer{})
	o.Tag = rc
	if _, err := Sign(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(gh.patch) != 1 || !strings.Contains(gh.patch[0], `"prerelease":true`) || !strings.Contains(gh.patch[0], `"draft":false`) {
		t.Fatalf("publish %v", gh.patch)
	}
}

func TestVersionOrder(t *testing.T) {
	order := []string{"fleet-v0.9.9", "fleet-v1.0.0-rc1", "fleet-v1.0.0-rc2", "fleet-v1.0.0-rc10", "fleet-v1.0.0", "fleet-v1.0.1", "fleet-v1.10.0"}
	for i := 0; i+1 < len(order); i++ {
		a, ok1 := ParseTag(order[i])
		b, ok2 := ParseTag(order[i+1])
		if !ok1 || !ok2 || !a.Less(b) || b.Less(a) {
			t.Errorf("%s < %s", order[i], order[i+1])
		}
	}
}

// --reviewer stands in for the repo variable (e.g. a token that may not read variables), and the
// review must still be by that login.
func TestSignReviewerFlag(t *testing.T) {
	home, _ := setup(t)
	gh := happyGH(t)
	delete(gh.answers, "repos/"+repo+"/actions/variables/FLEET_REVIEW_LOGIN")
	o := opts(home, gh, &bytes.Buffer{})
	o.DryRun, o.Reviewer = true, "go-reviewer"
	if _, err := Sign(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.Reviewer = "someone-else"
	if _, err := Sign(context.Background(), o); !isGate(err, "c") {
		t.Fatalf("other reviewer: %v", err)
	}
}
