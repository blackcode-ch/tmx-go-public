package store

import (
	"errors"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func putT(t *testing.T, s *Store, id, source, text string, at int64) model.Transcript {
	t.Helper()
	tr := model.Transcript{ID: id, Source: source, State: model.TranscriptDone, Model: "base-q5_1", Language: "de", Text: text,
		Segments: []model.TranscriptSegment{{FromMS: 0, ToMS: 1500, Text: text}}, CreatedAt: at, UpdatedAt: at}
	if err := s.PutTranscript(tr, "/rec/"+id+".wav"); err != nil {
		t.Fatal(err)
	}
	return tr
}

func ids(list []model.Transcript) string {
	out := ""
	for _, t := range list {
		out += t.ID + " "
	}
	return out
}

// The search index is FTS5 (modernc's SQLite includes it): this test fails if it ever does not.
func TestTranscriptSearchFTS5(t *testing.T) {
	s := openTest(t)
	var fts int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'transcripts_fts'`).Scan(&fts); err != nil || fts != 1 {
		t.Fatalf("no FTS5 index: %d %v", fts, err)
	}
	putT(t, s, "a", "recording:r1/1", "Grüezi mitenand, das Budget für Zürich", 1)
	putT(t, s, "b", "recording:r1/2", "The budget review is on Friday", 2)
	putT(t, s, "c", "recording:r2/1", "Nothing about money here", 3)

	cases := []struct{ q, prefix, want string }{
		{"budget", "", "b a "},              // newest first, any case
		{"zurich", "", "a "},                // accents do not matter
		{"budg rev", "", "b "},              // every word, each as a prefix
		{"BUDGET", "recording:r1/", "b a "}, // source prefix
		{"budget", "recording:r2/", ""},
		{"", "recording:r2/", "c "},
		{"", "", "c b a "},
		{`"; DROP TABLE x --`, "", ""}, // FTS syntax never gets through
		{"!!!", "", ""},                // no words: substring match, none
	}
	for _, c := range cases {
		got, err := s.ListTranscripts(TranscriptQuery{Q: c.q, SourcePrefix: c.prefix})
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if ids(got) != c.want {
			t.Errorf("q=%q prefix=%q: got %q, want %q", c.q, c.prefix, ids(got), c.want)
		}
	}

	// An update re-indexes the text; a delete drops it from the index.
	b := putT(t, s, "b", "recording:r1/2", "Changed words entirely", 2)
	if got, _ := s.ListTranscripts(TranscriptQuery{Q: "review"}); len(got) != 0 {
		t.Fatalf("old text still found: %s", ids(got))
	}
	if got, _ := s.ListTranscripts(TranscriptQuery{Q: "entirely"}); ids(got) != "b " {
		t.Fatalf("new text not found: %s", ids(got))
	}
	if got, err := s.GetTranscript("b"); err != nil || got.Text != b.Text || len(got.Segments) != 1 || got.Segments[0].ToMS != 1500 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if err := s.DeleteTranscript("b"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTranscript("b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if got, _ := s.ListTranscripts(TranscriptQuery{Q: "entirely"}); len(got) != 0 {
		t.Fatalf("deleted text still found: %s", ids(got))
	}
	var rows int
	s.db.QueryRow(`SELECT count(*) FROM transcripts_fts WHERE transcripts_fts MATCH 'entirely'`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("index keeps %d rows of a deleted transcript", rows)
	}
}

func TestTranscriptSourceAndUnfinished(t *testing.T) {
	s := openTest(t)
	putT(t, s, "a", "recording:r1/1", "done one", 1)
	q := model.Transcript{ID: "q", Source: "recording:r1/2", State: model.TranscriptQueued, CreatedAt: 2, UpdatedAt: 2}
	if err := s.PutTranscript(q, "/rec/q.wav"); err != nil {
		t.Fatal(err)
	}
	// One transcript per source.
	dup := q
	dup.ID = "other"
	if err := s.PutTranscript(dup, ""); err == nil {
		t.Fatal("a second transcript of the same source was stored")
	}
	got, err := s.TranscriptBySource("recording:r1/2")
	if err != nil || got.ID != "q" || got.Segments == nil {
		t.Fatalf("by source: %+v %v", got, err)
	}
	if _, err := s.TranscriptBySource("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
	un, err := s.UnfinishedTranscripts()
	if err != nil || len(un) != 1 || un[0].Transcript.ID != "q" || un[0].Audio != "/rec/q.wav" {
		t.Fatalf("unfinished: %+v %v", un, err)
	}
}

func TestFTSQuery(t *testing.T) {
	for in, want := range map[string]string{
		"hello world":    `"hello"* "world"*`,
		`a"b`:            `"a"* "b"*`,
		"  ":             "",
		"Grüezi, Zürich": `"Grüezi"* "Zürich"*`,
		"NEAR(a b)":      `"NEAR"* "a"* "b"*`,
	} {
		if got := FTSQuery(in); got != want {
			t.Errorf("FTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
