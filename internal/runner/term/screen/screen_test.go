package screen

import (
	"strings"
	"testing"
	"time"
)

func TestScreenRendersCursorMovesAndWideCharacters(t *testing.T) {
	s := New(20, 4, nil)
	defer s.Close()
	s.Write([]byte("hello\r\nworld"))
	s.Write([]byte("\x1b[1;1Hj"))    // overwrite h
	s.Write([]byte("\x1b[3;1H界❯ x")) // a wide character takes two cells
	lines := s.Lines()
	if len(lines) != 4 || lines[0] != "jello" || lines[1] != "world" || lines[2] != "界❯ x" || lines[3] != "" {
		t.Fatalf("%q", lines)
	}
	s.Write([]byte("\x1b[2J\x1b[H"))
	if strings.TrimSpace(s.Text()) != "" {
		t.Fatalf("not cleared: %q", s.Text())
	}
}

func TestScreenAnswersQueriesAndTracksBracketedPaste(t *testing.T) {
	replies := make(chan []byte, 4)
	s := New(40, 10, func(b []byte) { replies <- b })
	defer s.Close()
	if got := string(s.Paste("hi")); got != "hi" {
		t.Fatalf("paste before mode: %q", got)
	}
	s.Write([]byte("\x1b[?2004h\x1b[3;5H\x1b[6n"))
	select {
	case b := <-replies:
		if string(b) != "\x1b[3;5R" {
			t.Fatalf("cursor report %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no cursor position report")
	}
	if got := string(s.Paste("hi")); got != "\x1b[200~hi\x1b[201~" {
		t.Fatalf("bracketed paste %q", got)
	}
	s.Write([]byte("\x1b[?2004l"))
	if got := string(s.Paste("hi")); got != "hi" {
		t.Fatalf("paste after reset: %q", got)
	}
}

func TestAltScreenAndResize(t *testing.T) {
	s := New(10, 3, nil)
	defer s.Close()
	s.Write([]byte("main"))
	s.Write([]byte("\x1b[?1049h\x1b[Halt"))
	if s.Lines()[0] != "alt" {
		t.Fatalf("%q", s.Lines())
	}
	s.Write([]byte("\x1b[?1049l"))
	if s.Lines()[0] != "main" {
		t.Fatalf("%q", s.Lines())
	}
	s.Resize(30, 6)
	if len(s.Lines()) != 6 {
		t.Fatal(len(s.Lines()))
	}
}

func TestKeys(t *testing.T) {
	ks, err := Keys("down down enter ctrl+c 2 y esc shift+tab")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"\x1b[B", "\x1b[B", "\r", "\x03", "2", "y", "\x1b", "\x1b[Z"}
	if len(ks) != len(want) {
		t.Fatalf("%q", ks)
	}
	for i := range want {
		if string(ks[i]) != want[i] {
			t.Fatalf("%d: %q", i, ks[i])
		}
	}
	if _, err := Keys("hyper+x"); err == nil {
		t.Fatal("want error")
	}
}

func TestPlanAnswerWithoutHighlightTypesTheNumber(t *testing.T) {
	d := Detection{State: Question, Choices: []Choice{{1, "Red"}, {2, "Blue"}}}
	if k, err := PlanAnswer(d, "blue", nil); err != nil || k != "2" {
		t.Fatal(k, err)
	}
	d.Selected = 2
	if k, _ := PlanAnswer(d, "1", nil); k != "up enter" {
		t.Fatal(k)
	}
	if _, err := PlanAnswer(d, "7", nil); err == nil {
		t.Fatal("option 7 is not on the screen")
	}
	if _, err := PlanAnswer(Detection{State: Idle}, "1", nil); err == nil {
		t.Fatal("no menu")
	}
}

func TestCursorListNeedsSiblingsAndShortLabels(t *testing.T) {
	if c, _, _ := ParseMenu([]string{"❯ only one line"}, ""); c != nil {
		t.Fatalf("%v", c)
	}
	long := "❯ " + strings.Repeat("word ", 30)
	if c, _, _ := ParseMenu([]string{long, "  " + strings.Repeat("more ", 30)}, ""); c != nil {
		t.Fatal("prose is not a menu")
	}
	c, sel, first := ParseMenu([]string{"Pick:", "  ❯ Alpha", "    Beta"}, "❯")
	if len(c) != 2 || sel != 1 || first != 1 || c[1].Label != "Beta" {
		t.Fatal(c, sel, first)
	}
}
