package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

const (
	xTag    = "whisper-engine-v1.9.5"
	xCommit = "4444444444444444444444444444444444444444"
	xHead   = "5555555555555555555555555555555555555555"
	xUpload = "https://uploads.github.com/repos/blackcode-ch/tmx-go-public/releases/9/assets{?name,label}"
)

// extraGH is the public mirror as SignExtra sees it: JSON answers by path, file bytes by asset id.
type extraGH struct {
	answers map[string]any
	files   map[int64][]byte
	upload  []string
	body    [][]byte
}

func (f *extraGH) Get(_ context.Context, path string) ([]byte, error) {
	a, ok := f.answers[path]
	if !ok {
		return nil, fmt.Errorf("fake gh: 404 %s", path)
	}
	return json.Marshal(a)
}

func (f *extraGH) Download(_ context.Context, path string) ([]byte, error) {
	var id int64
	if _, err := fmt.Sscanf(path, "repos/"+PublicRepo+"/releases/assets/%d", &id); err == nil {
		if b, ok := f.files[id]; ok {
			return b, nil
		}
	}
	return nil, fmt.Errorf("fake gh: no asset %s", path)
}

func (f *extraGH) Upload(_ context.Context, url string, data []byte) error {
	f.upload, f.body = append(f.upload, url), append(f.body, append([]byte(nil), data...))
	return nil
}

func (f *extraGH) Patch(context.Context, string, []byte) error {
	return fmt.Errorf("fake gh: no patch expected")
}

var xT0 = time.Date(2026, 11, 3, 16, 50, 0, 0, time.UTC)

// happyExtra is a mirror where every gate passes for xTag. Asset ids: 100.. the EngineFiles, 99 SHA256SUMS.
func happyExtra() *extraGH {
	f := &extraGH{files: map[int64][]byte{}}
	var sums strings.Builder
	assets := []obj{}
	at := xT0.Add(2 * time.Minute)
	for i, name := range EngineFiles {
		b := []byte("bytes of " + name)
		id := int64(100 + i)
		f.files[id] = b
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
		assets = append(assets, obj{"id": id, "name": name, "state": "uploaded", "created_at": at, "uploader": obj{"login": CIBot, "type": "Bot"}})
	}
	f.files[99] = []byte(sums.String())
	assets = append(assets, obj{"id": 99, "name": "SHA256SUMS", "state": "uploaded", "created_at": at, "uploader": obj{"login": CIBot, "type": "Bot"}})
	pin := base64.StdEncoding.EncodeToString([]byte("# the pin\ntag=v1.9.5\ncommit=d1be6fde11ac6e0407606b4e42fe72d34add8037\n"))
	r := "repos/" + PublicRepo
	f.answers = map[string]any{
		"repos/" + DefaultRepo + "/contents/" + EnginePin + "?ref=fleet%2Fmain": obj{"content": pin, "encoding": "base64"},
		r + "/releases/tags/" + xTag: obj{"id": 9, "tag_name": xTag, "draft": false, "prerelease": true,
			"target_commitish": xCommit, "upload_url": xUpload, "assets": assets},
		r + "/branches/fleet%2Fmain":              obj{"commit": obj{"sha": xHead}},
		r + "/compare/" + xCommit + "..." + xHead: obj{"status": "ahead"},
		r + "/actions/runs?head_sha=" + xCommit + "&per_page=100": obj{"total_count": 2, "workflow_runs": []obj{
			{"id": 500, "path": ".github/workflows/ci.yml", "event": "push", "head_branch": "fleet/main", "head_sha": xCommit, "status": "completed", "conclusion": "success"},
			{"id": 501, "path": EngineWorkflow, "event": "push", "head_branch": "fleet/main", "head_sha": xCommit, "status": "completed", "conclusion": "success"}}},
		r + "/actions/runs/501/jobs?filter=all&per_page=100": obj{"total_count": 2, "jobs": []obj{
			{"run_id": 501, "name": "build (whisper-cli_linux_amd64)", "head_sha": xCommit, "status": "completed", "conclusion": "success", "started_at": xT0, "completed_at": xT0.Add(time.Minute)},
			{"run_id": 501, "name": "publish " + xTag + " (unsigned)", "head_sha": xCommit, "status": "completed", "conclusion": "success",
				"started_at": xT0.Add(90 * time.Second), "completed_at": xT0.Add(3 * time.Minute)}}},
	}
	return f
}

func xOpts(home string, gh GitHub, out *bytes.Buffer) ExtraOptions {
	return ExtraOptions{GitHub: gh, Home: home, Tag: xTag, Out: out, Now: func() time.Time { return xT0.Add(time.Hour) }}
}

// The key test: what sign-extra uploads verifies with the daemon's own extra verifier, for the
// mirror only, and not as a daemon release.
func TestSignExtraRoundTripsThroughTheVerifier(t *testing.T) {
	home, pin := setup(t)
	gh := happyExtra()
	var out bytes.Buffer
	res, err := SignExtra(context.Background(), xOpts(home, gh, &out))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(gh.upload) != 1 || gh.upload[0] != "https://uploads.github.com/repos/blackcode-ch/tmx-go-public/releases/9/assets?name=SHA256SUMS.sig" {
		t.Fatalf("uploads %v", gh.upload)
	}
	sums := gh.files[99]
	keys := []config.FleetKey{pin}
	req := []string{config.KeyEd25519}
	if err := update.VerifyExtra(keys, req, PublicRepo, xTag, sums, gh.body[0]); err != nil {
		t.Fatalf("the daemon's extra verifier refuses it: %v", err)
	}
	if update.VerifyExtra(keys, req, DefaultRepo, xTag, sums, gh.body[0]) == nil {
		t.Fatal("the signature must be bound to the mirror, not valid for the private repository")
	}
	if update.VerifyRelease(keys, req, PublicRepo, xTag, sums, gh.body[0]) == nil {
		t.Fatal("an extra's signature must never pass as a daemon release")
	}
	if !res.Uploaded || res.Commit != xCommit || strings.Contains(out.String(), "PRIVATE") {
		t.Fatalf("%+v", res)
	}
	logged, err := ReadExtraLog(home)
	if err != nil || len(logged) != 1 || logged[0].Repo != PublicRepo || logged[0].Tag != xTag {
		t.Fatalf("extras log %+v %v", logged, err)
	}
	if _, err := os.Stat(LogFile(home)); !os.IsNotExist(err) {
		t.Fatal("an extra must never go into the release log (gate (e) of Sign compares every entry)")
	}
	// Signing the same again is refused by the log, even if the .sig were removed from the release.
	if _, err := SignExtra(context.Background(), xOpts(home, happyExtra(), &out)); !isGate(err, "r") {
		t.Fatalf("re-sign: %v", err)
	}
}

func TestSignExtraDryRunUploadsNothing(t *testing.T) {
	home, _ := setup(t)
	gh := happyExtra()
	o := xOpts(home, gh, &bytes.Buffer{})
	o.DryRun = true
	if _, err := SignExtra(context.Background(), o); err != nil || len(gh.upload) != 0 {
		t.Fatalf("%v %v", err, gh.upload)
	}
	if l, _ := ReadExtraLog(home); len(l) != 0 {
		t.Fatal("a dry run logs nothing")
	}
}

func TestSignExtraRefusesEachGate(t *testing.T) {
	r := "repos/" + PublicRepo
	rel := func(gh *extraGH) obj { return gh.answers[r+"/releases/tags/"+xTag].(obj) }
	assets := func(gh *extraGH) []obj { return rel(gh)["assets"].([]obj) }
	jobs := func(gh *extraGH) []obj {
		return gh.answers[r+"/actions/runs/501/jobs?filter=all&per_page=100"].(obj)["jobs"].([]obj)
	}
	for _, tc := range []struct {
		name, gate string
		opt        func(*ExtraOptions)
		mod        func(*extraGH)
	}{
		{name: "another repository", gate: "p", opt: func(o *ExtraOptions) { o.Repo = DefaultRepo }},
		{name: "not an engine tag", gate: "p", opt: func(o *ExtraOptions) { o.Tag = "fleet-v1.2.0" }},
		{name: "not the pinned engine", gate: "p", opt: func(o *ExtraOptions) { o.Tag = "whisper-engine-v1.9.4" }},
		{name: "a bad pin", gate: "p", mod: func(gh *extraGH) {
			gh.answers["repos/"+DefaultRepo+"/contents/"+EnginePin+"?ref=fleet%2Fmain"] = obj{"content": base64.StdEncoding.EncodeToString([]byte("tag=latest\n")), "encoding": "base64"}
		}},
		{name: "a draft", gate: "r", mod: func(gh *extraGH) { rel(gh)["draft"] = true }},
		{name: "not a prerelease", gate: "r", mod: func(gh *extraGH) { rel(gh)["prerelease"] = false }},
		{name: "already signed", gate: "r", mod: func(gh *extraGH) {
			rel(gh)["assets"] = append(assets(gh), obj{"id": 98, "name": "SHA256SUMS.sig", "state": "uploaded", "uploader": obj{"login": CIBot, "type": "Bot"}})
		}},
		{name: "a file by a person", gate: "r", mod: func(gh *extraGH) { assets(gh)[0]["uploader"] = obj{"login": "someone", "type": "User"} }},
		{name: "an unexpected file", gate: "r", mod: func(gh *extraGH) {
			rel(gh)["assets"] = append(assets(gh), obj{"id": 97, "name": "evil.sh", "state": "uploaded", "uploader": obj{"login": CIBot, "type": "Bot"}})
		}},
		{name: "a missing file", gate: "r", mod: func(gh *extraGH) { rel(gh)["assets"] = assets(gh)[1:] }},
		{name: "a branch target", gate: "r", mod: func(gh *extraGH) { rel(gh)["target_commitish"] = "fleet/main" }},
		{name: "not on the mirror's fleet/main", gate: "o", mod: func(gh *extraGH) { gh.answers[r+"/compare/"+xCommit+"..."+xHead] = obj{"status": "diverged"} }},
		{name: "no engine run", gate: "w", mod: func(gh *extraGH) {
			gh.answers[r+"/actions/runs?head_sha="+xCommit+"&per_page=100"].(obj)["workflow_runs"].([]obj)[1]["path"] = ".github/workflows/other.yml"
		}},
		{name: "a failed run", gate: "w", mod: func(gh *extraGH) {
			gh.answers[r+"/actions/runs?head_sha="+xCommit+"&per_page=100"].(obj)["workflow_runs"].([]obj)[1]["conclusion"] = "failure"
		}},
		{name: "a run of another branch", gate: "w", mod: func(gh *extraGH) {
			gh.answers[r+"/actions/runs?head_sha="+xCommit+"&per_page=100"].(obj)["workflow_runs"].([]obj)[1]["head_branch"] = "pr/12"
		}},
		{name: "no publish job", gate: "w", mod: func(gh *extraGH) { jobs(gh)[1]["name"] = "publish something else" }},
		{name: "a file uploaded outside the job", gate: "w", mod: func(gh *extraGH) { assets(gh)[2]["created_at"] = xT0.Add(time.Hour) }},
		{name: "a file whose bytes differ", gate: "s", mod: func(gh *extraGH) { gh.files[101] = []byte("other bytes") }},
		{name: "SHA256SUMS lists an extra name", gate: "s", mod: func(gh *extraGH) {
			gh.files[99] = append(gh.files[99], []byte(strings.Repeat("c", 64)+"  evil.sh\n")...)
		}},
		{name: "SHA256SUMS lists a name twice", gate: "s", mod: func(gh *extraGH) {
			first := strings.SplitN(string(gh.files[99]), "\n", 2)[0]
			gh.files[99] = append(gh.files[99], []byte(first+"\n")...)
		}},
		{name: "SHA256SUMS is not a sums file", gate: "s", mod: func(gh *extraGH) { gh.files[99] = []byte("hello\n") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := setup(t)
			gh := happyExtra()
			if tc.mod != nil {
				tc.mod(gh)
			}
			o := xOpts(home, gh, &bytes.Buffer{})
			if tc.opt != nil {
				tc.opt(&o)
			}
			_, err := SignExtra(context.Background(), o)
			if !isGate(err, tc.gate) {
				t.Fatalf("want gate (%s), got %v", tc.gate, err)
			}
			if len(gh.upload) != 0 {
				t.Fatal("a refusal uploads nothing")
			}
		})
	}
}
