package transcribe

// Recording transcripts keep only what was said (tmx-lead, 3 Nov, ported from the Python fleet's review_clean):
// whisper invents filler on silent stretches of a long recording (a 2-min TTS clip came back with ~30x
// "Thank you." on its trailing silence), which then pollutes the summary. A short sentence (<= RepeatWords words)
// repeated RepeatRun+ times in a row - within a segment or over consecutive segments - is kept once; a 2-4 word
// phrase looping LoopRun+ times inside a segment is kept once (one word only at LoopRun+2: "no, no, no" is
// speech); subtitle boilerplate is dropped. Speech said twice and long repeated sentences stay as heard; the
// WAV stays, so a transcript can always be made again.

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const (
	RepeatWords = 5
	RepeatRun   = 3
	LoopRun     = 4
)

// HallucinationPhrases is the boilerplate whisper writes on near-silence (lower case substrings).
var HallucinationPhrases = []string{
	"closed captioning provided by", "thanks for watching", "thank you for watching", "subtitles by", "amara.org",
	"untertitel im auftrag des zdf", "untertitel der amara", "sous-titrage", "sous-titres réalisés",
	"please subscribe", "www.patreon.com",
}

var sentenceEnd = regexp.MustCompile(`([.!?])\s+`)

func sentences(text string) []string {
	text = strings.TrimSpace(sentenceEnd.ReplaceAllString(text, "$1\x00"))
	var out []string
	for _, s := range strings.Split(text, "\x00") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// norm is a sentence's words, lower case, without punctuation.
func norm(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

func boilerplate(s string) bool {
	low := strings.ToLower(s)
	for _, p := range HallucinationPhrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

func shortRun(n int, s string) bool {
	return n >= RepeatRun && len(strings.Fields(norm(s))) <= RepeatWords
}

// collapseRuns keeps one of each run of the same short item (items compared by key).
func collapseRuns[T any](items []T, key func(T) string) []T {
	var out []T
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && key(items[j]) == key(items[i]) {
			j++
		}
		if shortRun(j-i, key(items[i])) {
			out = append(out, items[i])
		} else {
			out = append(out, items[i:j]...)
		}
		i = j
	}
	return out
}

// unloop keeps once a 2-4 word phrase repeated LoopRun+ times in a row (one word: LoopRun+2+ times).
func unloop(text string) string {
	words := strings.Fields(text)
	key := func(w string) string { return norm(w) }
	for n := 4; n >= 1; n-- {
		run := LoopRun
		if n == 1 {
			run = LoopRun + 2
		}
		var out []string
		for i := 0; i < len(words); {
			reps := 1
			for i+(reps+1)*n <= len(words) && same(words[i:i+n], words[i+reps*n:i+(reps+1)*n], key) {
				reps++
			}
			if reps >= run {
				out = append(out, words[i:i+n]...)
				i += reps * n
			} else {
				out = append(out, words[i])
				i++
			}
		}
		words = out
	}
	return strings.Join(words, " ")
}

func same(a, b []string, key func(string) string) bool {
	for i := range a {
		if key(a[i]) != key(b[i]) || key(a[i]) == "" {
			return false
		}
	}
	return true
}

// CleanText cleans one stretch of transcript text.
func CleanText(text string) string {
	var kept []string
	for _, s := range sentences(text) {
		if !boilerplate(s) {
			kept = append(kept, s)
		}
	}
	kept = collapseRuns(kept, norm)
	return unloop(strings.Join(kept, " "))
}

// CleanSegments cleans a recording's segments: each one's text, then runs over consecutive segments; empty
// segments are dropped.
func CleanSegments(segs []model.TranscriptSegment) []model.TranscriptSegment {
	out := make([]model.TranscriptSegment, 0, len(segs))
	for _, s := range segs {
		if s.Text = CleanText(s.Text); s.Text != "" {
			out = append(out, s)
		}
	}
	return collapseRuns(out, func(s model.TranscriptSegment) string { return norm(s.Text) })
}
