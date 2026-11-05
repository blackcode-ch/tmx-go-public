package transcribe

import (
	"os"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// The 3 Nov eval: whisper on a 2.3-min TTS clip added ~30x "Thank you." on its trailing silence.
func TestCleanTheTTSSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/review_tts_transcript.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "Thank you.") < 20 {
		t.Fatal("sample changed")
	}
	got := CleanText(string(raw))
	if !strings.HasSuffix(got, "Next review is in two weeks, same time. Thank you.") || strings.Count(got, "Thank you") != 1 {
		t.Fatalf("end: %q", got[max(0, len(got)-120):])
	}
	if !strings.Contains(got, "Peter, can you ask the vendor for the education discount before Friday?") || len(got) < 850 {
		t.Fatalf("speech lost: %q", got)
	}
}

func TestCleanKeepsRealSpeech(t *testing.T) {
	for _, c := range [][2]string{
		{"Yes. Yes. I agree.", "Yes. Yes. I agree."},
		{"We ship the release on Monday morning. We ship the release on Monday morning. We ship the release on Monday morning.",
			"We ship the release on Monday morning. We ship the release on Monday morning. We ship the release on Monday morning."},
		{"No, no, no, that is wrong.", "No, no, no, that is wrong."},
		{"so you know you know you know you know we ship", "so you know we ship"},
		{"Good. Thanks for watching! Please subscribe.", "Good."},
		{"Okay. Okay. Okay. Okay. Next item.", "Okay. Next item."},
	} {
		if got := CleanText(c[0]); got != c[1] {
			t.Errorf("CleanText(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// whisper writes the invented run as separate segments on the silence: kept once, with the first one's time.
func TestCleanSegmentsCollapsesRunsOverSegments(t *testing.T) {
	segs := []model.TranscriptSegment{{FromMS: 0, ToMS: 4000, Text: "That's it."}}
	for i := range 12 {
		segs = append(segs, model.TranscriptSegment{FromMS: int64(5000 + i*2000), ToMS: int64(7000 + i*2000), Text: " Thank you."})
	}
	segs = append(segs, model.TranscriptSegment{FromMS: 40000, ToMS: 41000, Text: "Thanks for watching!"})
	got := CleanSegments(segs)
	if len(got) != 2 || got[1].FromMS != 5000 || joinText(got) != "That's it. Thank you." {
		t.Fatalf("%+v", got)
	}
}
