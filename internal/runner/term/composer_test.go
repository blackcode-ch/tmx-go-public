package term

import (
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestMailHeldForDraftBusyAndUnknownScreen(t *testing.T) {
	const empty = "────────\r\n❯\r\n────────\r\n? for shortcuts"
	for _, tc := range []struct {
		name, text string
		state      model.AgentState
	}{
		{"busy", "Working (esc to interrupt)\r\n" + empty, model.StateWorking},
		{"draft", "────────\r\n❯ human draft\r\n────────\r\n? for shortcuts", model.StateIdle},
		{"wrapped draft", "────────\r\n❯\r\nhuman continuation\r\n────────\r\n? for shortcuts", model.StateIdle},
		{"unknown", "unrecognized quiet screen", model.StateIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, bp := newGuardedTestProcess(t)
			p.done = make(chan struct{})
			close(bp.release)
			p.state = tc.state
			p.w.scr.Write([]byte(tc.text))
			if err := p.w.send("queued mail"); err != nil {
				t.Fatal(err)
			}
			p.w.idleSince = time.Now().Add(-time.Hour)
			p.w.evaluate()
			if len(p.w.queued) != 1 {
				t.Fatal("mail left queue on unsafe screen")
			}
			select {
			case b := <-bp.writes:
				t.Fatalf("typed over screen: %q", b)
			default:
			}
			// Clearing the draft / finishing the turn releases exactly one paste.
			p.w.scr.Write([]byte("\x1b[2J\x1b[H" + empty))
			p.w.idleSince = time.Now().Add(-time.Hour)
			p.w.evaluate()
			if len(p.w.queued) != 0 {
				t.Fatal("mail still held with empty input")
			}
			select {
			case b := <-bp.writes:
				if string(b) != "queued mail" {
					t.Fatalf("paste %q", b)
				}
			default:
				t.Fatal("no paste")
			}
			// Stop the delayed submit callback before the fixture closes.
			close(p.done)
		})
	}
}

func TestEmptyComposerEvidence(t *testing.T) {
	for _, tc := range []struct {
		h     model.Harness
		text  string
		empty bool
	}{
		{model.HarnessCodex, "›\n  95% context left", true},
		{model.HarnessCodex, "› draft\n  95% context left", false},
		{model.HarnessCodex, "›\n  continued draft\n  95% context left", false},
		{model.HarnessCodex, "›\n  unrecognized footer", false},
		{model.HarnessClaude, "? for shortcuts", false},
		{model.HarnessPi, "────\n\n────\nctrl+c twice to exit", true},
		{model.HarnessPi, "────\ndraft\n────\nctrl+c twice to exit", false},
		{model.HarnessOpenCode, "┃\n┃  Build  model\ntab agents", true},
		{model.HarnessOpenCode, "┃ draft\n┃  Build  model\ntab agents", false},
	} {
		if got := emptyComposer(tc.text, tc.h); got != tc.empty {
			t.Errorf("%s %q: %v", tc.h, tc.text, got)
		}
	}
}
