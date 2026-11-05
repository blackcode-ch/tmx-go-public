package fleet

import (
	"fmt"
	"testing"
)

// bc #244 (tmx-lead, 4 Nov): announce_set MERGES links by default (dedupe by URL; a newer title
// wins); replacing the list needs replace_links. Two agents adding their links no longer wipe
// each other's.
func TestProjectSectionLinksMerge(t *testing.T) {
	_, f, p := sectionFixture(t)
	set := func(links []string, replace bool) []string {
		t.Helper()
		sec, err := f.SetProjectSection(OwnerCaller, p.ID, ProjectSectionPatch{Links: &links, ReplaceLinks: replace})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range sec.Links {
			out = append(out, l.Title+"|"+l.URL)
		}
		return out
	}
	set([]string{"Guide | https://x.test/a", "https://x.test/b"}, false)
	got := set([]string{"https://x.test/c", "Better | https://x.test/b"}, false)
	want := []string{"Guide|https://x.test/a", "Better|https://x.test/b", "c|https://x.test/c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("merge: %v, want %v", got, want)
	}
	// An empty list without the flag changes nothing.
	if got := set([]string{}, false); len(got) != 3 {
		t.Fatalf("empty merge changed the links: %v", got)
	}
	// Replace: only with the flag; an empty list then clears.
	if got := set([]string{"https://x.test/d"}, true); fmt.Sprint(got) != fmt.Sprint([]string{"d|https://x.test/d"}) {
		t.Fatalf("replace: %v", got)
	}
	if got := set([]string{}, true); len(got) != 0 {
		t.Fatalf("clear: %v", got)
	}
	// At most 20 links in all: a merge past that is refused, not silently cut.
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, fmt.Sprintf("https://x.test/%d", i))
	}
	set(many, true)
	more := []string{"https://x.test/extra"}
	if _, err := f.SetProjectSection(OwnerCaller, p.ID, ProjectSectionPatch{Links: &more}); KindOf(err) != KindInvalid {
		t.Fatalf("21st link: %v", err)
	}
}
