package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"github.com/blackcode-ch/tmx-go/internal/summarize"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

// Summaries of recordings (DESIGN.md "Summaries"): the daemon's service, and tmx rec summary.

// newSummaries makes the daemon's summary service: Claude (config claude_command) writes a
// summary of every finished recording from its transcripts (voice) and frames; settings live in
// config.json "summaries"; it hears transcripts and deletions on the fleet's bus.
func newSummaries(paths config.Paths, cfg config.Config, st *store.Store, rec *recorder.Service, voice *transcribe.Service,
	f *fleet.Fleet, log *slog.Logger) *summarize.Service {
	return summarize.New(summarize.Options{Store: st, Recordings: rec, Voice: voice, Bus: f, Claude: cfg.ClaudeCommand,
		ClaudeConfigDir: paths.ClaudeConfigDir(),
		Publish:         f.Publish, Logger: log,
		Settings: func() config.SummaryConfig {
			if c, err := config.Load(paths); err == nil && c.Summaries != nil {
				return *c.Summaries
			}
			return config.SummaryConfig{}
		},
		Save: func(v config.SummaryConfig) error {
			c, err := config.Load(paths)
			if err != nil {
				return err
			}
			c.Summaries = &v
			return config.Save(paths, c)
		},
	})
}

// recSummary is tmx rec summary ID [--again] [--wait] [--json].
func recSummary(e *env, args []string) error {
	fs := newFlags("rec summary")
	again := fs.Bool("again", false, "write it again (transcribing first what is missing)")
	wait := fs.Bool("wait", false, "with --again: wait until it is written (up to 15 minutes)")
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(recUsage)
	}
	id := client.Seg(pos[0])
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var x model.RecordingSummary
		if *again {
			if err := c.Post(cx, "/api/recordings/"+id+"/summarize", nil, &x); err != nil {
				return err
			}
			if !*wait {
				fmt.Fprintf(e.out, "summary of %s queued (%s); tmx rec summary %s shows it\n", pos[0], x.State, pos[0])
				return nil
			}
			wcx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			for x.State.Pending() {
				select {
				case <-wcx.Done():
					return fmt.Errorf("the summary is still %s; tmx rec summary %s shows it later", x.State, pos[0])
				case <-time.After(2 * time.Second):
				}
				if err := c.Get(wcx, "/api/recordings/"+id+"/summary", &x); err != nil {
					return err
				}
			}
		} else if err := c.Get(cx, "/api/recordings/"+id+"/summary", &x); err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.MarshalIndent(x, "", "  ")
			fmt.Fprintln(e.out, string(b))
			return nil
		}
		fmt.Fprint(e.out, formatSummary(x))
		return nil
	})
}

// formatSummary prints a summary for a terminal.
func formatSummary(x model.RecordingSummary) string {
	var b strings.Builder
	switch x.State {
	case model.SummaryNone:
		b.WriteString("no summary yet (tmx rec summary ID --again writes one)\n")
		return b.String()
	case model.SummaryTranscribing:
		b.WriteString("transcribing first, then Claude writes the summary\n")
	case model.SummaryQueued:
		b.WriteString("queued: Claude writes it next\n")
	case model.SummaryRunning:
		b.WriteString("Claude is writing it now\n")
	case model.SummaryFailed:
		fmt.Fprintf(&b, "failed: %s\n", x.Error)
	case model.SummarySkipped:
		fmt.Fprintf(&b, "no summary: %s\n", x.Error)
	}
	if x.Note != "" {
		fmt.Fprintf(&b, "note: %s\n", x.Note)
	}
	if x.Summary == "" {
		return b.String()
	}
	if x.State != model.SummaryDone {
		b.WriteString("(the previous summary:)\n")
	}
	b.WriteString("\n" + x.Summary + "\n")
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s\n", title)
		for _, it := range items {
			fmt.Fprintf(&b, "  - %s\n", it)
		}
	}
	list("Key points", x.KeyPoints)
	list("Decisions", x.Decisions)
	if len(x.ActionItems) > 0 {
		b.WriteString("\nAction items\n")
		for _, it := range x.ActionItems {
			if it.Who != "" {
				fmt.Fprintf(&b, "  - %s: %s\n", it.Who, it.What)
			} else {
				fmt.Fprintf(&b, "  - %s\n", it.What)
			}
		}
	}
	if len(x.Chapters) > 0 {
		b.WriteString("\nChapters\n")
		for _, c := range x.Chapters {
			fmt.Fprintf(&b, "  %8s  %s\n", recLength(c.TMS), c.Title)
		}
	}
	fmt.Fprintf(&b, "\nby %s from %s and %s", x.Model, fmt.Sprintf(plural(x.Transcripts, "%d transcript", "%d transcripts"), x.Transcripts),
		fmt.Sprintf(plural(x.Frames, "%d frame", "%d frames"), x.Frames))
	if x.Parts > 1 {
		fmt.Fprintf(&b, ", in %d parts", x.Parts)
	}
	if x.FinishedAt > 0 {
		fmt.Fprintf(&b, ", %s", time.UnixMilli(x.FinishedAt).Format("2006-01-02 15:04"))
	}
	b.WriteString("\n")
	return b.String()
}
