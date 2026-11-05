package update

import (
	"math"
	"regexp"
	"strconv"
)

// Version is a release version as the Makefile writes it (git describe on fleet-v* tags):
// fleet-vMAJOR.MINOR.PATCH[-rcN], or that followed by -N-gSHA for a build N commits after the tag (the
// "latest" channel). A release candidate is lower than its release (fleet-v1.2.3-rc2 < fleet-v1.2.3),
// the order `tmx release sign` uses (internal/release.Version). Anything else (a dev build, -dirty, a bare commit) is not a release:
// such builds never update themselves.
type Version struct {
	Major, Minor, Patch, Ahead int
	RC                         int // the N of -rcN; 0 = a final release
	raw                        string
}

var versionRx = regexp.MustCompile(`^fleet-v(\d+)\.(\d+)\.(\d+)(?:-rc([1-9]\d*))?(?:-(\d+)-g[0-9a-f]{4,40})?$`)

// ParseVersion parses a release version.
func ParseVersion(s string) (Version, bool) {
	m := versionRx.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	n := func(i int) int {
		v, _ := strconv.Atoi(m[i])
		return v
	}
	v := Version{Major: n(1), Minor: n(2), Patch: n(3), raw: s}
	if m[4] != "" {
		v.RC = n(4)
	}
	if m[5] != "" {
		v.Ahead = n(5)
	}
	return v, true
}

func (v Version) String() string { return v.raw }

// Prerelease reports a release candidate; only the "latest" channel installs one.
func (v Version) Prerelease() bool { return v.RC > 0 }

// Less orders versions: by the tag (-rcN before the final release), then by the commits after it.
func (v Version) Less(o Version) bool {
	rc := func(x int) int {
		if x == 0 {
			return math.MaxInt
		}
		return x
	}
	a := [5]int{v.Major, v.Minor, v.Patch, rc(v.RC), v.Ahead}
	b := [5]int{o.Major, o.Minor, o.Patch, rc(o.RC), o.Ahead}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
