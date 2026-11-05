package release

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// Extra signing (`tmx release sign-extra TAG`): the signature of an on-demand extra, today the
// voice engine, which public CI builds and publishes unsigned on the public mirror. The daemon
// accepts such a release only with SHA256SUMS.sig over update.FleetExtraSignatureContext, the
// repository and the tag (update.VerifySums), so a signature is bound to that one repository.
// SignExtra refuses unless every gate holds, checked through the GitHub API:
//
//	(p) the tag is whisper-engine-<tag> of internal/transcribe/engine.pin on DefaultRepo's
//	    fleet/main (the reviewed pin), and the repository is PublicRepo; nothing else is signed;
//	(r) the release of the tag is published as a prerelease, has no SHA256SUMS.sig yet, and every
//	    one of its files was uploaded by github-actions[bot]; its files are exactly the engines,
//	    the two license files and SHA256SUMS;
//	(o) the release's target commit is PublicRepo's fleet/main head or an ancestor of it (only
//	    the mirror may push there: a scrubbed, gated snapshot);
//	(w) exactly one successful run of EngineWorkflow on that commit (a push to fleet/main or a
//	    dispatch on it) has a successful job named "publish <tag> (unsigned)", and every file of
//	    the release was uploaded while that job ran;
//	(s) SHA256SUMS lists exactly those files, and each file's bytes have the listed sha256.
//
// Not proven, as for releases: that the binaries are what the source builds. The run is public,
// on GitHub's fresh hosted runners, with no secret in it. Extras are logged in their own file
// (ExtraLogFile), never in the release log: gate (e) of Sign compares every logged tag.

const (
	// PublicRepo is the public mirror, the only repository extras are signed for.
	PublicRepo = "blackcode-ch/tmx-go-public"
	// EnginePin is the engine pin's path in DefaultRepo; EngineWorkflow builds the engine there.
	EnginePin      = "internal/transcribe/engine.pin"
	EngineWorkflow = ".github/workflows/whisper-engine.yml"
	maxEngineFile  = 64 << 20
)

// EngineFiles are the release's files besides SHA256SUMS (keep in step with engineAsset in
// internal/transcribe/catalog.go and ASSETS in the public whisper-engine.yml).
var EngineFiles = []string{
	"whisper-cli_darwin_amd64",
	"whisper-cli_darwin_arm64",
	"whisper-cli_linux_amd64",
	"whisper-cli_linux_arm64",
	"whisper-cli_windows_amd64.exe",
	"LICENSE-whisper.cpp.txt",
	"LICENSE-whisper-models.txt",
}

var (
	extraTagRx = regexp.MustCompile(`^whisper-engine-v\d{1,6}\.\d{1,6}\.\d{1,6}$`)
	pinTagRx   = regexp.MustCompile(`^v\d{1,6}\.\d{1,6}\.\d{1,6}$`)
)

// ExtraLogFile is the log of signed extras in home.
func ExtraLogFile(home string) string { return filepath.Join(home, "signed-extras.log") }

// ExtraLogEntry is one line of the extras log.
type ExtraLogEntry struct {
	Repo       string    `json:"repo"`
	Tag        string    `json:"tag"`
	Commit     string    `json:"commit"`
	SumsSHA256 string    `json:"sums_sha256"`
	KeyID      string    `json:"key_id"`
	Time       time.Time `json:"time"`
}

// ReadExtraLog returns the extras log's entries; a line that does not parse fails closed.
func ReadExtraLog(home string) ([]ExtraLogEntry, error) {
	b, err := os.ReadFile(ExtraLogFile(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []ExtraLogEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e ExtraLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("extras log %s is corrupt: %w", ExtraLogFile(home), err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func appendExtraLog(home string, e ExtraLogEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(ExtraLogFile(home), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ExtraOptions is one extra signing.
type ExtraOptions struct {
	GitHub GitHub
	Home   string // the release home (Home())
	Repo   string // default PublicRepo; nothing else is accepted
	Tag    string
	KeyID  string // default config.LegacyFleetKeyID
	DryRun bool   // check every gate and sign locally; upload and log nothing
	Out    io.Writer
	Now    func() time.Time
}

type extraRelease struct {
	ID              int64     `json:"id"`
	TagName         string    `json:"tag_name"`
	Draft           bool      `json:"draft"`
	Prerelease      bool      `json:"prerelease"`
	TargetCommitish string    `json:"target_commitish"`
	UploadURL       string    `json:"upload_url"`
	Assets          []ghAsset `json:"assets"`
}

// SignExtra checks the gates for o.Tag and signs it (see the comment at the top of this file).
func SignExtra(ctx context.Context, o ExtraOptions) (Result, error) {
	if o.Repo == "" {
		o.Repo = PublicRepo
	}
	if o.KeyID == "" {
		o.KeyID = config.LegacyFleetKeyID
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.GitHub == nil || o.Home == "" {
		return Result{}, errors.New("release: no GitHub or release home")
	}
	if o.Repo != PublicRepo {
		return Result{}, refuse("p", "extras are signed only for %s, not %q", PublicRepo, o.Repo)
	}
	if !extraTagRx.MatchString(o.Tag) {
		return Result{}, refuse("p", "tag %q is not whisper-engine-vX.Y.Z", o.Tag)
	}
	key, err := LoadKey(o.Home, o.KeyID)
	if err != nil {
		return Result{}, err
	}
	logged, err := ReadExtraLog(o.Home)
	if err != nil {
		return Result{}, err
	}
	for _, e := range logged {
		if e.Repo == o.Repo && e.Tag == o.Tag {
			return Result{}, refuse("r", "%s %s was signed already on %s (extras log)", o.Repo, o.Tag, e.Time.Format(time.DateOnly))
		}
	}
	say := func(format string, args ...any) { fmt.Fprintf(o.Out, format+"\n", args...) }
	repo := "repos/" + o.Repo

	// (p) the reviewed pin names this tag.
	var pin struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := getJSON(ctx, o.GitHub, "repos/"+DefaultRepo+"/contents/"+EnginePin+"?ref="+url.QueryEscape(Branch), &pin); err != nil {
		return Result{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(pin.Content, "\n", ""))
	if pin.Encoding != "base64" || err != nil {
		return Result{}, refuse("p", "%s on %s of %s could not be read", EnginePin, Branch, DefaultRepo)
	}
	pinTag := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "tag="); ok {
			pinTag = strings.TrimSpace(v)
		}
	}
	if !pinTagRx.MatchString(pinTag) {
		return Result{}, refuse("p", "%s on %s has no valid tag= line", EnginePin, Branch)
	}
	if o.Tag != "whisper-engine-"+pinTag {
		return Result{}, refuse("p", "%s is not the pinned engine (%s on %s says whisper-engine-%s)", o.Tag, EnginePin, Branch, pinTag)
	}
	say("ok (p) %s is %s's pin on %s of %s", o.Tag, EnginePin, Branch, DefaultRepo)

	// (r) the published prerelease and its files.
	var rel extraRelease
	if err := getJSON(ctx, o.GitHub, repo+"/releases/tags/"+url.PathEscape(o.Tag), &rel); err != nil {
		return Result{}, err
	}
	if rel.TagName != o.Tag || rel.Draft || !rel.Prerelease {
		return Result{}, refuse("r", "the release of %s is not a published prerelease (draft=%v prerelease=%v)", o.Tag, rel.Draft, rel.Prerelease)
	}
	if !shaRx.MatchString(rel.TargetCommitish) {
		return Result{}, refuse("r", "the release targets %q, not a commit", rel.TargetCommitish)
	}
	commit := rel.TargetCommitish
	want := map[string]bool{sumsAsset: true}
	for _, f := range EngineFiles {
		want[f] = true
	}
	files := map[string]ghAsset{}
	for _, a := range rel.Assets {
		if a.Name == sigAsset {
			return Result{}, refuse("r", "the release of %s already has %s", o.Tag, sigAsset)
		}
		if !want[a.Name] {
			return Result{}, refuse("r", "the release has an unexpected file %q", a.Name)
		}
		if a.State != "uploaded" || a.Uploader.Login != CIBot || a.Uploader.Type != "Bot" {
			return Result{}, refuse("r", "%s was not uploaded by CI (%s, state %q)", a.Name, a.Uploader.Login, a.State)
		}
		files[a.Name] = a
	}
	if len(files) != len(want) {
		return Result{}, refuse("r", "the release has %d of its %d files", len(files), len(want))
	}
	say("ok (r) release %d: a published prerelease, %d files, all uploaded by %s, no %s yet", rel.ID, len(files), CIBot, sigAsset)

	// (o) the commit is on the mirror's fleet/main.
	var branch struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := getJSON(ctx, o.GitHub, repo+"/branches/"+url.PathEscape(Branch), &branch); err != nil {
		return Result{}, err
	}
	if !shaRx.MatchString(branch.Commit.SHA) {
		return Result{}, refuse("o", "%s has no head commit", Branch)
	}
	if branch.Commit.SHA != commit {
		var cmp struct {
			Status string `json:"status"`
		}
		if err := getJSON(ctx, o.GitHub, repo+"/compare/"+commit+"..."+branch.Commit.SHA, &cmp); err != nil {
			return Result{}, err
		}
		if cmp.Status != "ahead" && cmp.Status != "identical" {
			return Result{}, refuse("o", "commit %s is not on %s of %s (compare: %q)", commit[:12], Branch, o.Repo, cmp.Status)
		}
	}
	say("ok (o) %s is on %s of %s (head %s)", commit[:12], Branch, o.Repo, branch.Commit.SHA[:12])

	// (w) the one engine run that published the files.
	var runs struct {
		TotalCount   int `json:"total_count"`
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			Path       string `json:"path"`
			Event      string `json:"event"`
			HeadBranch string `json:"head_branch"`
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := getJSON(ctx, o.GitHub, repo+"/actions/runs?head_sha="+commit+"&per_page=100", &runs); err != nil {
		return Result{}, err
	}
	if runs.TotalCount > len(runs.WorkflowRuns) {
		return Result{}, refuse("w", "%d runs on %s, more than one page", runs.TotalCount, commit[:12])
	}
	job := "publish " + o.Tag + " (unsigned)"
	var runID int64
	var window [2]time.Time
	for _, r := range runs.WorkflowRuns {
		if r.Path != EngineWorkflow || r.HeadSHA != commit || r.HeadBranch != Branch || (r.Event != "push" && r.Event != "workflow_dispatch") {
			continue
		}
		if r.Status != "completed" || r.Conclusion != "success" {
			continue
		}
		var jobs struct {
			TotalCount int `json:"total_count"`
			Jobs       []struct {
				RunID       int64     `json:"run_id"`
				Name        string    `json:"name"`
				HeadSHA     string    `json:"head_sha"`
				Status      string    `json:"status"`
				Conclusion  string    `json:"conclusion"`
				StartedAt   time.Time `json:"started_at"`
				CompletedAt time.Time `json:"completed_at"`
			} `json:"jobs"`
		}
		if err := getJSON(ctx, o.GitHub, fmt.Sprintf("%s/actions/runs/%d/jobs?filter=all&per_page=100", repo, r.ID), &jobs); err != nil {
			return Result{}, err
		}
		for _, j := range jobs.Jobs {
			if j.RunID != r.ID || j.Name != job || j.HeadSHA != commit || j.Status != "completed" || j.Conclusion != "success" {
				continue
			}
			if runID != 0 {
				return Result{}, refuse("w", "more than one successful %q on %s (runs %d and %d)", job, commit[:12], runID, r.ID)
			}
			runID, window = r.ID, [2]time.Time{j.StartedAt, j.CompletedAt}
		}
	}
	if runID == 0 {
		return Result{}, refuse("w", "no successful run of %s on %s with a successful job %q", EngineWorkflow, commit[:12], job)
	}
	for _, a := range files {
		if a.CreatedAt.Before(window[0]) || a.CreatedAt.After(window[1]) {
			return Result{}, refuse("w", "%s was uploaded at %s, outside job %q of run %d (%s to %s)", a.Name,
				a.CreatedAt.Format(time.RFC3339), job, runID, window[0].Format(time.RFC3339), window[1].Format(time.RFC3339))
		}
	}
	say("ok (w) every file was uploaded during job %q of run %d (%s)", job, runID, EngineWorkflow)

	// (s) SHA256SUMS lists exactly these files, with their bytes' sha256.
	sums, err := o.GitHub.Download(ctx, fmt.Sprintf("%s/releases/assets/%d", repo, files[sumsAsset].ID))
	if err != nil {
		return Result{}, err
	}
	if len(sums) == 0 || len(sums) > maxSums || !looksLikeSums(sums) {
		return Result{}, refuse("s", "%s is not a sha256sum file", sumsAsset)
	}
	listed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") { // as looksLikeSums reads it
		f := strings.Fields(line)
		name := strings.TrimPrefix(f[1], "*")
		if _, dup := listed[name]; dup || !want[name] || name == sumsAsset {
			return Result{}, refuse("s", "%s lists %q unexpectedly", sumsAsset, name)
		}
		listed[name] = strings.ToLower(f[0])
	}
	if len(listed) != len(EngineFiles) {
		return Result{}, refuse("s", "%s lists %d of the %d files", sumsAsset, len(listed), len(EngineFiles))
	}
	names := append([]string(nil), EngineFiles...)
	sort.Strings(names)
	for _, name := range names {
		b, err := o.GitHub.Download(ctx, fmt.Sprintf("%s/releases/assets/%d", repo, files[name].ID))
		if err != nil {
			return Result{}, err
		}
		if len(b) == 0 || len(b) > maxEngineFile {
			return Result{}, refuse("s", "%s has an unexpected size (%d bytes)", name, len(b))
		}
		if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != listed[name] {
			return Result{}, refuse("s", "%s does not have the sha256 %s lists", name, sumsAsset)
		}
	}
	say("ok (s) %s lists exactly the %d files, and each one's bytes match", sumsAsset, len(EngineFiles))

	h := sha256.Sum256(sums)
	res := Result{Commit: commit, SumsSHA256: hex.EncodeToString(h[:])}
	res.SigLine = update.SignExtraLine(o.KeyID, key, o.Repo, o.Tag, sums)
	pub := key.Public().(ed25519.PublicKey)
	pinned := []config.FleetKey{{ID: o.KeyID, Type: config.KeyEd25519, Key: base64.StdEncoding.EncodeToString(pub)}}
	if err := update.VerifyExtra(pinned, []string{config.KeyEd25519}, o.Repo, o.Tag, sums, []byte(res.SigLine+"\n")); err != nil {
		return Result{}, fmt.Errorf("the signature does not verify: %w", err)
	}
	say("signing extra %s %s (SHA256SUMS sha256 %s) with %s (fingerprint %s)", o.Repo, o.Tag, res.SumsSHA256, o.KeyID, Fingerprint(pub))
	if o.DryRun {
		say("dry run: would upload %s: %s", sigAsset, res.SigLine)
		return res, nil
	}
	up := rel.UploadURL
	if i := strings.Index(up, "{"); i >= 0 {
		up = up[:i]
	}
	if err := o.GitHub.Upload(ctx, up+"?name="+sigAsset, []byte(res.SigLine+"\n")); err != nil {
		return Result{}, err
	}
	res.Uploaded, res.Published = true, true // the prerelease was published by CI; signing adds the .sig
	if err := appendExtraLog(o.Home, ExtraLogEntry{Repo: o.Repo, Tag: o.Tag, Commit: commit, SumsSHA256: res.SumsSHA256, KeyID: o.KeyID, Time: o.Now().UTC()}); err != nil {
		return res, fmt.Errorf("uploaded, but the extras log was not written (add %s by hand): %w", o.Tag, err)
	}
	say("uploaded %s to %s %s", sigAsset, o.Repo, o.Tag)
	return res, nil
}
