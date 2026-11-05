package release

import (
	"archive/zip"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/update"
)

// Release signing (DESIGN.md "Signing a release"). Sign refuses unless every gate holds, checked
// here through the GitHub API, not by whoever runs it:
//
//	(a) the tag's commit is fleet/main's head or an ancestor of it;
//	(b) every required check of fleet/main's branch protection passed on that commit: the latest
//	    check run of that name by the required app (protection's app_id, else GitHub Actions)
//	    concluded success, skipped or neutral, as GitHub counts them; a required context with no
//	    app pinned may instead be a commit status in state success;
//	(c) the latest "security-review" commit status on that commit is success and was set by the
//	    configured reviewer (--reviewer, else the repo variable FLEET_REVIEW_LOGIN, as CI's
//	    release-policy job checks it; none = refused);
//	(d) the draft release of the tag exists, has no SHA256SUMS.sig yet, targets the commit (or
//	    fleet/main) and names its CI run ("fleet-ci-run: <id>" in its body, written by the draft
//	    job). That run is of .github/workflows/build.yml, the push of the tag on the commit, and
//	    concluded success, with a successful job named exactly "draft fleet release"; SHA256SUMS
//	    was uploaded by github-actions[bot] while that job ran, and its bytes equal the SHA256SUMS
//	    in that run's artifact fleet-release-sums. Only that run's own jobs can add artifacts to
//	    it (the run-scoped runtime token, not GITHUB_TOKEN), and a push run uses the workflow file
//	    of the pushed commit, which (a)-(c) proved reviewed: so the signed sums are the ones the
//	    reviewed release workflow computed. Not proven: that the binaries behind them are what the
//	    source builds (a compromised runner, action or dependency could build others; no
//	    reproducible builds), nor who wrote the release body (any contents:write token can; it
//	    only points at the run, and every check is then made on the run itself);
//	(e) the tag's version is higher than every release in the local release log and every
//	    published fleet-v release (no downgrade, no re-signing).
//
// Then it signs SHA256SUMS (update.SignReleaseLine, the verifier's own message), checks the
// signature with the verifier (update.VerifyRelease), uploads SHA256SUMS.sig and logs it.

const (
	// DefaultRepo is the fleet repository.
	DefaultRepo = "blackcode-ch/tmx-go"
	// Branch is the branch a release must come from.
	Branch = "fleet/main"
	// SecurityReviewContext is the commit status the security review sets.
	SecurityReviewContext = "security-review"
	// CIBot is who uploads CI's release assets (GITHUB_TOKEN).
	CIBot = "github-actions[bot]"
	// ActionsAppID is the GitHub Actions app: a required check without an app_id must come from it.
	ActionsAppID = 15368
	// ReviewerVariable is the repo variable naming the reviewer (CI's release-policy reads it too).
	ReviewerVariable = "FLEET_REVIEW_LOGIN"
	maxPages         = 20
	// Workflow, DraftJob and SumsArtifact are what CI's release run must be (gate d): the workflow
	// file (as of the tagged, reviewed commit), the job that drafts the release, and the artifact
	// it keeps SHA256SUMS in. Renaming any of them in build.yml needs the same change here.
	Workflow     = ".github/workflows/build.yml"
	DraftJob     = "draft fleet release"
	SumsArtifact = "fleet-release-sums"
	maxArtifact  = 4 << 20

	sumsAsset = "SHA256SUMS"
	sigAsset  = "SHA256SUMS.sig"
	maxSums   = 1 << 20
)

var (
	repoRx = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	tagRx  = regexp.MustCompile(`^fleet-v(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:-rc([1-9]\d{0,5}))?$`) // rc as the daemon's ParseVersion: no rc0/rc01
	shaRx  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Version is a release tag's version: fleet-vX.Y.Z or fleet-vX.Y.Z-rcN (an rc is lower than
// its release).
type Version struct {
	n   [4]int // n[3]: the rc number, 0 = none
	Tag string
}

// ParseTag parses a fleet release tag.
func ParseTag(tag string) (Version, bool) {
	m := tagRx.FindStringSubmatch(tag)
	if m == nil {
		return Version{}, false
	}
	v := Version{Tag: tag}
	for i := 0; i < 4; i++ {
		if m[i+1] != "" {
			v.n[i], _ = strconv.Atoi(m[i+1])
		}
	}
	if m[4] != "" && v.n[3] == 0 {
		return Version{}, false // rc0 would compare as the final release
	}
	return v, true
}

// Less orders versions; X.Y.Z-rcN < X.Y.Z.
func (v Version) Less(o Version) bool {
	for i := 0; i < 3; i++ {
		if v.n[i] != o.n[i] {
			return v.n[i] < o.n[i]
		}
	}
	rc := func(x int) int {
		if x == 0 {
			return 1 << 30
		}
		return x
	}
	return rc(v.n[3]) < rc(o.n[3])
}

// LogEntry is one line of the release log.
type LogEntry struct {
	Tag        string    `json:"tag"`
	Commit     string    `json:"commit"`
	SumsSHA256 string    `json:"sums_sha256"`
	KeyID      string    `json:"key_id"`
	Time       time.Time `json:"time"`
}

// ReadLog returns the release log's entries (none when it does not exist yet). A line that does
// not parse fails closed.
func ReadLog(home string) ([]LogEntry, error) {
	b, err := os.ReadFile(LogFile(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []LogEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("release log %s is corrupt: %w", LogFile(home), err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func appendLog(home string, e LogEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(LogFile(home), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
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

// Options is one signing.
type Options struct {
	GitHub GitHub
	Home   string // the release home (Home())
	Repo   string // default DefaultRepo
	Tag    string
	KeyID  string // default config.LegacyFleetKeyID
	DryRun bool   // check every gate and sign locally; upload and log nothing
	// Reviewer is the login whose security-review status counts; "" = the repo variable
	// FLEET_REVIEW_LOGIN.
	Reviewer string
	Out      io.Writer
	Now      func() time.Time
}

// Result is what was (or, in a dry run, would be) uploaded.
type Result struct {
	Commit     string
	SumsSHA256 string
	SigLine    string
	Uploaded   bool
	Published  bool
}

// GateError is a refusal.
type GateError struct {
	Gate   string // "a".."e", or "" for a bad argument
	Reason string
}

func (e *GateError) Error() string {
	if e.Gate == "" {
		return "refusing to sign: " + e.Reason
	}
	return "refusing to sign: gate (" + e.Gate + ") " + e.Reason
}

func refuse(gate, format string, args ...any) error {
	return &GateError{Gate: gate, Reason: fmt.Sprintf(format, args...)}
}

type ghRelease struct {
	ID              int64     `json:"id"`
	TagName         string    `json:"tag_name"`
	Draft           bool      `json:"draft"`
	TargetCommitish string    `json:"target_commitish"`
	Body            string    `json:"body"`
	UploadURL       string    `json:"upload_url"`
	Assets          []ghAsset `json:"assets"`
}

type ghAsset struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	Uploader  struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"uploader"`
}

func getJSON(ctx context.Context, gh GitHub, path string, v any) error {
	b, err := gh.Get(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("GitHub %s: unexpected answer: %w", path, err)
	}
	return nil
}

// Sign checks the gates for o.Tag and signs it (see the comment at the top of this file).
func Sign(ctx context.Context, o Options) (Result, error) {
	if o.Repo == "" {
		o.Repo = DefaultRepo
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
	if owner, name, _ := strings.Cut(o.Repo, "/"); !repoRx.MatchString(o.Repo) || owner == "." || owner == ".." || name == "." || name == ".." {
		return Result{}, refuse("", "repository %q is not owner/name", o.Repo)
	}
	ver, ok := ParseTag(o.Tag)
	if !ok {
		return Result{}, refuse("", "tag %q is not fleet-vX.Y.Z or fleet-vX.Y.Z-rcN", o.Tag)
	}
	key, err := LoadKey(o.Home, o.KeyID)
	if err != nil {
		return Result{}, err
	}
	logged, err := ReadLog(o.Home)
	if err != nil {
		return Result{}, err
	}
	for _, e := range logged {
		if v, ok := ParseTag(e.Tag); !ok || !v.Less(ver) {
			return Result{}, refuse("e", "%s is not higher than %s, signed on %s (release log)", o.Tag, e.Tag, e.Time.Format(time.DateOnly))
		}
	}
	say := func(format string, args ...any) { fmt.Fprintf(o.Out, format+"\n", args...) }
	repo := "repos/" + o.Repo

	// (a) the tag's commit is on fleet/main.
	commit, err := tagCommit(ctx, o.GitHub, repo, o.Tag)
	if err != nil {
		return Result{}, err
	}
	var branch struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
		Protection struct {
			Enabled              bool `json:"enabled"`
			RequiredStatusChecks struct {
				Contexts []string `json:"contexts"`
				Checks   []struct {
					Context string `json:"context"`
					AppID   *int64 `json:"app_id"`
				} `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	if err := getJSON(ctx, o.GitHub, repo+"/branches/"+url.PathEscape(Branch), &branch); err != nil {
		return Result{}, err
	}
	if !shaRx.MatchString(branch.Commit.SHA) {
		return Result{}, refuse("a", "%s has no head commit", Branch)
	}
	if branch.Commit.SHA != commit {
		var cmp struct {
			Status string `json:"status"`
		}
		// base...head: "ahead" = head (the branch) contains base (the tag's commit).
		if err := getJSON(ctx, o.GitHub, repo+"/compare/"+commit+"..."+branch.Commit.SHA, &cmp); err != nil {
			return Result{}, err
		}
		if cmp.Status != "ahead" && cmp.Status != "identical" {
			return Result{}, refuse("a", "commit %s of %s is not on %s (compare: %q)", commit[:12], o.Tag, Branch, cmp.Status)
		}
	}
	say("ok (a) %s = %s, on %s (head %s)", o.Tag, commit[:12], Branch, branch.Commit.SHA[:12])

	// (b) every required check passed; (c) the security review passed.
	required := map[string]int64{} // context -> the app its check run must come from (0 = not pinned)
	if branch.Protection.Enabled {
		for _, c := range branch.Protection.RequiredStatusChecks.Contexts {
			required[c] = 0
		}
		for _, c := range branch.Protection.RequiredStatusChecks.Checks {
			if c.AppID != nil && *c.AppID > 0 {
				required[c.Context] = *c.AppID
			} else if _, ok := required[c.Context]; !ok {
				required[c.Context] = 0
			}
		}
	}
	delete(required, "")
	if len(required) == 0 {
		return Result{}, refuse("b", "%s's branch protection lists no required checks", Branch)
	}
	var runs struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			App        struct {
				ID int64 `json:"id"`
			} `json:"app"`
		} `json:"check_runs"`
	}
	if err := getJSON(ctx, o.GitHub, repo+"/commits/"+commit+"/check-runs?per_page=100", &runs); err != nil {
		return Result{}, err
	}
	if runs.TotalCount > len(runs.CheckRuns) {
		return Result{}, refuse("b", "%d check runs on %s, more than one page", runs.TotalCount, commit[:12])
	}
	var statuses struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := getJSON(ctx, o.GitHub, repo+"/commits/"+commit+"/status?per_page=100", &statuses); err != nil {
		return Result{}, err
	}
	state := map[string]string{} // the combined status holds the latest status per context
	for _, s := range statuses.Statuses {
		state[s.Context] = s.State
	}
	latest := map[string]int64{}
	verdict := map[string]string{} // the latest check run per name by its app (a re-run has a higher id)
	for _, r := range runs.CheckRuns {
		app, isRequired := required[r.Name]
		if app == 0 {
			app = ActionsAppID
		}
		if !isRequired || r.App.ID != app {
			continue // a run of the same name by another app counts for nothing
		}
		if id, seen := latest[r.Name]; seen && r.ID < id {
			continue
		}
		latest[r.Name] = r.ID
		if r.Status != "completed" {
			verdict[r.Name] = r.Status
		} else {
			verdict[r.Name] = r.Conclusion
		}
	}
	var names []string
	for c := range required {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		v, run := verdict[c]
		switch {
		case run && (v == "success" || v == "skipped" || v == "neutral"):
		case !run && required[c] == 0 && state[c] == "success":
		default:
			if !run {
				v = state[c]
			}
			if v == "" {
				v = "missing"
			}
			return Result{}, refuse("b", "required check %q on %s is %s", c, commit[:12], v)
		}
	}
	say("ok (b) %d required checks passed: %s", len(names), strings.Join(names, ", "))
	reviewer, err := reviewerLogin(ctx, o.GitHub, repo, o.Reviewer)
	if err != nil {
		return Result{}, err
	}
	if err := securityReview(ctx, o.GitHub, repo, commit, reviewer); err != nil {
		return Result{}, err
	}
	say("ok (c) %s: success by %s", SecurityReviewContext, reviewer)

	// (d) the draft release and its SHA256SUMS come from CI's run for the tag.
	var releases []ghRelease
	for page := 1; ; page++ {
		if page > maxPages {
			return Result{}, refuse("e", "more than %d releases; cannot see them all", 100*maxPages)
		}
		var batch []ghRelease
		if err := getJSON(ctx, o.GitHub, fmt.Sprintf("%s/releases?per_page=100&page=%d", repo, page), &batch); err != nil {
			return Result{}, err
		}
		releases = append(releases, batch...)
		if len(batch) < 100 {
			break
		}
	}
	var rel *ghRelease
	for i := range releases {
		if releases[i].TagName == o.Tag {
			if rel != nil {
				return Result{}, refuse("d", "more than one release for %s", o.Tag)
			}
			rel = &releases[i]
		}
	}
	if rel == nil {
		return Result{}, refuse("d", "no release for %s", o.Tag)
	}
	if !rel.Draft {
		return Result{}, refuse("d", "the release of %s is already published (only a draft is signed)", o.Tag)
	}
	if t := rel.TargetCommitish; t != commit && t != Branch {
		return Result{}, refuse("d", "the release targets %q, not %s or %s", t, commit[:12], Branch)
	}
	var sums *ghAsset
	for i := range rel.Assets {
		switch a := &rel.Assets[i]; a.Name {
		case sigAsset:
			return Result{}, refuse("d", "the release of %s already has %s", o.Tag, sigAsset)
		case sumsAsset:
			sums = a
		}
	}
	if sums == nil || sums.State != "uploaded" {
		return Result{}, refuse("d", "the release of %s has no uploaded %s", o.Tag, sumsAsset)
	}
	if sums.Uploader.Login != CIBot || sums.Uploader.Type != "Bot" {
		return Result{}, refuse("d", "%s was uploaded by %q, not by CI (%s)", sumsAsset, sums.Uploader.Login, CIBot)
	}
	runID, err := ciRun(ctx, o.GitHub, repo, o.Tag, commit, rel, sums)
	if err != nil {
		return Result{}, err
	}
	say("ok (d) draft release %d: %s uploaded by %s during job %q of run %d (%s)", rel.ID, sumsAsset, CIBot, DraftJob, runID, Workflow)

	// (e) higher than every published release too.
	for _, r := range releases {
		if r.Draft || r.TagName == o.Tag {
			continue
		}
		if v, ok := ParseTag(r.TagName); ok && !v.Less(ver) {
			return Result{}, refuse("e", "%s is not higher than the published %s", o.Tag, r.TagName)
		}
	}
	say("ok (e) %s is higher than every signed and published release", o.Tag)

	body, err := o.GitHub.Download(ctx, fmt.Sprintf("%s/releases/assets/%d", repo, sums.ID))
	if err != nil {
		return Result{}, err
	}
	if len(body) == 0 || len(body) > maxSums || !looksLikeSums(body) {
		return Result{}, refuse("d", "%s is not a sha256sum file", sumsAsset)
	}
	if err := sameAsArtifact(ctx, o.GitHub, repo, runID, commit, body); err != nil {
		return Result{}, err
	}
	say("ok (d) %s equals run %d's artifact %s", sumsAsset, runID, SumsArtifact)
	h := sha256.Sum256(body)
	res := Result{Commit: commit, SumsSHA256: hex.EncodeToString(h[:])}
	res.SigLine = update.SignReleaseLine(o.KeyID, key, o.Repo, o.Tag, body)
	pub := key.Public().(ed25519.PublicKey)
	pin := []config.FleetKey{{ID: o.KeyID, Type: config.KeyEd25519, Key: base64.StdEncoding.EncodeToString(pub)}}
	if err := update.VerifyRelease(pin, []string{config.KeyEd25519}, o.Repo, o.Tag, body, []byte(res.SigLine+"\n")); err != nil {
		return Result{}, fmt.Errorf("the signature does not verify: %w", err)
	}
	say("signing %s %s (SHA256SUMS sha256 %s) with %s (fingerprint %s)", o.Repo, o.Tag, res.SumsSHA256, o.KeyID, Fingerprint(pub))
	if o.DryRun {
		say("dry run: would upload %s: %s", sigAsset, res.SigLine)
		return res, nil
	}
	up := rel.UploadURL
	if i := strings.Index(up, "{"); i >= 0 {
		up = up[:i] // the URL template's {?name,label}
	}
	if err := o.GitHub.Upload(ctx, up+"?name="+sigAsset, []byte(res.SigLine+"\n")); err != nil {
		return Result{}, err
	}
	res.Uploaded = true
	if err := appendLog(o.Home, LogEntry{Tag: o.Tag, Commit: commit, SumsSHA256: res.SumsSHA256, KeyID: o.KeyID, Time: o.Now().UTC()}); err != nil {
		return res, fmt.Errorf("uploaded, but the release log was not written (add %s by hand): %w", o.Tag, err)
	}
	say("uploaded %s to the draft release of %s", sigAsset, o.Tag)
	// Publishing is not a trust step (every daemon verifies SHA256SUMS.sig with its pinned keys
	// and never looks at drafts), so the signer does it: no CI run, no key in CI. An rc stays a
	// prerelease (only the "latest" channel installs one); a fleet release never becomes the
	// repository's "Latest release".
	patch, _ := json.Marshal(map[string]any{"draft": false, "prerelease": ver.n[3] != 0, "make_latest": "false"})
	if err := o.GitHub.Patch(ctx, fmt.Sprintf("%s/releases/%d", repo, rel.ID), patch); err != nil {
		return res, fmt.Errorf("signed and uploaded, but not published (publish the draft by hand: gh release edit %s --draft=false --latest=false): %w", o.Tag, err)
	}
	res.Published = true
	say("published %s", o.Tag)
	return res, nil
}

// reviewerLogin is the login whose security-review counts: the flag, else the repo variable.
func reviewerLogin(ctx context.Context, gh GitHub, repo, flag string) (string, error) {
	if flag = strings.TrimSpace(flag); flag != "" {
		return flag, nil
	}
	var v struct {
		Value string `json:"value"`
	}
	if err := getJSON(ctx, gh, repo+"/actions/variables/"+ReviewerVariable, &v); err != nil {
		return "", refuse("c", "no reviewer configured: cannot read the repo variable %s (%v); pass --reviewer LOGIN", ReviewerVariable, err)
	}
	if v.Value = strings.TrimSpace(v.Value); v.Value == "" {
		return "", refuse("c", "no reviewer configured: the repo variable %s is empty; pass --reviewer LOGIN", ReviewerVariable)
	}
	return v.Value, nil
}

// securityReview: the latest security-review status on commit (the highest id over every page)
// is success and was set by reviewer. The combined status is not enough: it does not say who.
func securityReview(ctx context.Context, gh GitHub, repo, commit, reviewer string) error {
	type status struct {
		ID      int64  `json:"id"`
		Context string `json:"context"`
		State   string `json:"state"`
		Creator struct {
			Login string `json:"login"`
		} `json:"creator"`
	}
	var last *status
	for page := 1; ; page++ {
		if page > maxPages {
			return refuse("c", "too many commit statuses on %s", commit[:12])
		}
		var batch []status
		if err := getJSON(ctx, gh, fmt.Sprintf("%s/commits/%s/statuses?per_page=100&page=%d", repo, commit, page), &batch); err != nil {
			return err
		}
		for i := range batch {
			if s := batch[i]; s.Context == SecurityReviewContext && (last == nil || s.ID > last.ID) {
				last = &s
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	switch {
	case last == nil:
		return refuse("c", "commit status %q on %s is missing", SecurityReviewContext, commit[:12])
	case last.State != "success":
		return refuse("c", "the latest %q on %s is %s", SecurityReviewContext, commit[:12], last.State)
	case !strings.EqualFold(last.Creator.Login, reviewer):
		return refuse("c", "the latest %q on %s was set by %q, not the reviewer %q", SecurityReviewContext, commit[:12], last.Creator.Login, reviewer)
	}
	return nil
}

// tagCommit resolves a tag (lightweight or annotated) to its commit.
func tagCommit(ctx context.Context, gh GitHub, repo, tag string) (string, error) {
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := getJSON(ctx, gh, repo+"/git/ref/tags/"+tag, &ref); err != nil {
		return "", err
	}
	for i := 0; i < 4 && ref.Object.Type == "tag" && shaRx.MatchString(ref.Object.SHA); i++ {
		if err := getJSON(ctx, gh, repo+"/git/tags/"+ref.Object.SHA, &ref); err != nil {
			return "", err
		}
	}
	if ref.Object.Type != "commit" || !shaRx.MatchString(ref.Object.SHA) {
		return "", refuse("a", "tag %s does not name a commit", tag)
	}
	return ref.Object.SHA, nil
}

var runRx = regexp.MustCompile(`(?m)^fleet-ci-run: ([0-9]{1,19})\r?$`)

// ciRun is gate (d)'s run check: the run the draft names is the tag's release run of Workflow,
// and SHA256SUMS was uploaded while its DraftJob ran. It returns the run id.
func ciRun(ctx context.Context, gh GitHub, repo, tag, commit string, rel *ghRelease, sums *ghAsset) (int64, error) {
	m := runRx.FindAllStringSubmatch(rel.Body, -1)
	if len(m) != 1 {
		return 0, refuse("d", "the release of %s names %d CI runs (want one \"fleet-ci-run: <id>\" line from the %q job)", tag, len(m), DraftJob)
	}
	id, err := strconv.ParseInt(m[0][1], 10, 64)
	if err != nil || id <= 0 {
		return 0, refuse("d", "bad run id %q in the release of %s", m[0][1], tag)
	}
	var run struct {
		ID         int64  `json:"id"`
		Path       string `json:"path"`
		Event      string `json:"event"`
		HeadBranch string `json:"head_branch"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	if err := getJSON(ctx, gh, fmt.Sprintf("%s/actions/runs/%d", repo, id), &run); err != nil {
		return 0, err
	}
	switch {
	case run.ID != id:
		return 0, refuse("d", "asked for run %d, GitHub answered run %d", id, run.ID)
	case run.Path != Workflow:
		return 0, refuse("d", "run %d is of %q, not %s", id, run.Path, Workflow)
	case run.Event != "push" || run.HeadBranch != tag || run.HeadSHA != commit:
		return 0, refuse("d", "run %d is a %s of %q on %.12s, not the push of %s on %.12s", id, run.Event, run.HeadBranch, run.HeadSHA, tag, commit)
	case run.Status != "completed" || run.Conclusion != "success":
		return 0, refuse("d", "run %d is %s/%s, not completed/success", id, run.Status, run.Conclusion)
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
	// filter=all: the jobs of every attempt; the upload must fall inside one successful draft job.
	if err := getJSON(ctx, gh, fmt.Sprintf("%s/actions/runs/%d/jobs?filter=all&per_page=100", repo, id), &jobs); err != nil {
		return 0, err
	}
	if jobs.TotalCount > len(jobs.Jobs) {
		return 0, refuse("d", "run %d has %d jobs, more than one page", id, jobs.TotalCount)
	}
	for _, j := range jobs.Jobs {
		if j.RunID == id && j.Name == DraftJob && j.HeadSHA == commit && j.Status == "completed" && j.Conclusion == "success" &&
			!j.StartedAt.IsZero() && !sums.CreatedAt.Before(j.StartedAt) && !sums.CreatedAt.After(j.CompletedAt) {
			return id, nil
		}
	}
	return 0, refuse("d", "%s (uploaded %s) was not uploaded during a successful %q job of run %d",
		sumsAsset, sums.CreatedAt.Format(time.RFC3339), DraftJob, id)
}

// sameAsArtifact is the rest of gate (d): sums equals the SHA256SUMS kept in run id's artifact
// SumsArtifact, which only that run's own jobs could have made.
func sameAsArtifact(ctx context.Context, gh GitHub, repo string, id int64, commit string, sums []byte) error {
	var list struct {
		TotalCount int `json:"total_count"`
		Artifacts  []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Expired     bool   `json:"expired"`
			SizeInBytes int64  `json:"size_in_bytes"`
			WorkflowRun struct {
				ID      int64  `json:"id"`
				HeadSHA string `json:"head_sha"`
			} `json:"workflow_run"`
		} `json:"artifacts"`
	}
	if err := getJSON(ctx, gh, fmt.Sprintf("%s/actions/runs/%d/artifacts?name=%s&per_page=100", repo, id, SumsArtifact), &list); err != nil {
		return err
	}
	var found []int64
	for _, a := range list.Artifacts {
		if a.Name != SumsArtifact {
			continue
		}
		if a.Expired || a.WorkflowRun.ID != id || a.WorkflowRun.HeadSHA != commit || a.SizeInBytes > maxArtifact {
			return refuse("d", "run %d's artifact %s is expired, too large or not of that run (sign within its retention)", id, SumsArtifact)
		}
		found = append(found, a.ID)
	}
	if len(found) != 1 || list.TotalCount != len(list.Artifacts) {
		return refuse("d", "run %d has %d artifacts named %s, want one", id, len(found), SumsArtifact)
	}
	z, err := gh.Download(ctx, fmt.Sprintf("%s/actions/artifacts/%d/zip", repo, found[0]))
	if err != nil {
		return err
	}
	if len(z) > maxArtifact {
		return refuse("d", "run %d's artifact %s is too large", id, SumsArtifact)
	}
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		return refuse("d", "run %d's artifact %s is not a zip: %v", id, SumsArtifact, err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != sumsAsset || zr.File[0].UncompressedSize64 > maxSums {
		return refuse("d", "run %d's artifact %s does not hold just %s", id, SumsArtifact, sumsAsset)
	}
	f, err := zr.File[0].Open()
	if err != nil {
		return refuse("d", "run %d's artifact %s: %v", id, SumsArtifact, err)
	}
	kept, err := io.ReadAll(io.LimitReader(f, maxSums+1))
	f.Close()
	if err != nil {
		return refuse("d", "run %d's artifact %s: %v", id, SumsArtifact, err)
	}
	if !bytes.Equal(kept, sums) {
		return refuse("d", "the release's %s differs from the one run %d computed (artifact %s)", sumsAsset, id, SumsArtifact)
	}
	return nil
}

// looksLikeSums: every line is "<64 hex>  <name>" (sha256sum's format).
func looksLikeSums(b []byte) bool {
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 {
			return false
		}
		if _, err := hex.DecodeString(f[0]); err != nil {
			return false
		}
		n++
	}
	return n > 0
}
