package release

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/update"
)

// The release workflow treats the pushed tag as data (security-review #118): every job that reads
// github.ref_name checks it against one strict pattern first, and jq only ever gets it through --arg.
func TestReleaseWorkflowTreatsTheTagAsData(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/build.yml")
	if os.IsNotExist(err) && os.Getenv("TMX_PUBLIC_SNAPSHOT") == "1" {
		t.Skip("the release workflow is private; the public CI snapshot doesn't carry it")
	}
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	const check = `[[ "$TAG" =~ ^fleet-v[0-9]+\.[0-9]+\.[0-9]+(-rc[1-9][0-9]*)?$ ]] || {`
	if n := strings.Count(wf, check); n < 2 {
		t.Errorf("tag pattern check found %d times, want it in release-policy and the draft job", n)
	}
	splice := regexp.MustCompile(`\$\{?TAG`)
	for i, line := range strings.Split(wf, "\n") {
		// Only what follows the jq call is jq's program; $TAG before it is a gh argument.
		j := strings.LastIndex(line, "jq ")
		if j < 0 {
			continue
		}
		if prog := line[j:]; splice.MatchString(prog) && !strings.Contains(prog, `--arg tag "$TAG"`) {
			t.Errorf("build.yml:%d puts $TAG into a jq program: %s", i+1, strings.TrimSpace(line))
		}
	}

	// The same pattern (bash ERE == Go RE2 here) refuses the reviewer's forged-row tag and agrees with
	// the signer and the daemon on what a release tag is.
	rx := regexp.MustCompile(`^fleet-v[0-9]+\.[0-9]+\.[0-9]+(-rc[1-9][0-9]*)?$`)
	evil := `fleet-v1"or(false))|empty,("MTIzCXRydWUJZmFsc2U="|@base64d),(.|select(""==`
	for _, tag := range []string{evil, "fleet-v1.2.0-rc0", "fleet-v1.2.0-rc01", "fleet-v1.2", "v1.2.0", "fleet-v1.2.0\n"} {
		if rx.MatchString(tag) || tagRx.MatchString(tag) {
			t.Errorf("%q accepted", tag)
		}
	}
	for _, tag := range []string{"fleet-v1.2.0", "fleet-v1.2.0-rc1", "fleet-v10.0.3-rc12"} {
		_, ok := update.ParseVersion(tag)
		if !rx.MatchString(tag) || !tagRx.MatchString(tag) || !ok {
			t.Errorf("%q: workflow %v, signer %v, daemon %v", tag, rx.MatchString(tag), tagRx.MatchString(tag), ok)
		}
	}
}
