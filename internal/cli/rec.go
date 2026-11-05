package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

// tmx rec: the owner's screen and microphone recordings (DESIGN.md "Recorder"). There is
// deliberately no "start": a recording begins only with a click in the owner's window.
const recUsage = `usage: tmx rec ls [--limit N] | show ID | rm ID --yes
       tmx rec summary ID [--again [--wait]] [--json]   Claude's summary (--again writes it again)
       tmx rec process ID   (the report worker only) make the report here and print its state
  (recordings start only from the window: Recordings, or the record button)`

func cmdRec(e *env, args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	switch args[0] {
	case "ls":
		fs := newFlags("rec ls")
		limit := fs.Int("limit", 30, "how many, newest first (0: all)")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var list []model.Recording
			if err := c.Get(cx, "/api/recordings?limit="+strconv.Itoa(*limit), &list); err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(e.out, "no recordings (start one in the window: Recordings)")
				return nil
			}
			tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTARTED\tKIND\tSTATE\tLENGTH\tSIZE\tSEGMENTS\tTITLE")
			for _, r := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", r.ID, time.UnixMilli(r.StartedAt).Format("2006-01-02 15:04"),
					r.Kind, r.State, recLength(r.DurationMS), recBytes(r.Bytes), len(r.Segments), r.Title)
			}
			return tw.Flush()
		})
	case "show":
		if len(args) != 2 {
			return usageErr(recUsage)
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var r model.Recording
			if err := c.Get(cx, "/api/recordings/"+client.Seg(args[1]), &r); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s  %s\n", r.ID, r.Title)
			fmt.Fprintf(e.out, "  %s, %s, %s, %s (%s)\n", r.Kind, r.State, recLength(r.DurationMS), recBytes(r.Bytes), r.Mime)
			fmt.Fprintf(e.out, "  started %s", time.UnixMilli(r.StartedAt).Format("2006-01-02 15:04:05"))
			if r.EndedAt > 0 {
				fmt.Fprintf(e.out, ", ended %s", time.UnixMilli(r.EndedAt).Format("15:04:05"))
			}
			fmt.Fprintf(e.out, "\n  folder %s\n", r.Dir)
			if r.Error != "" {
				fmt.Fprintf(e.out, "  note: %s\n", r.Error)
			}
			for _, g := range r.Segments {
				fmt.Fprintf(e.out, "  #%d  %s  %s  %s\n", g.N, recLength(g.DurationMS), recBytes(g.Bytes), g.File)
				if g.WAV != "" {
					fmt.Fprintf(e.out, "       16 kHz WAV   %s\n", g.WAV)
				}
			}
			return nil
		})
	case "rm":
		fs := newFlags("rec rm")
		yes := fs.Bool("yes", false, "confirm")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usageErr(recUsage)
		}
		if !*yes {
			return usageErr("this deletes recording %s and its files; repeat with --yes", pos[0])
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			if err := c.Do(cx, http.MethodDelete, "/api/recordings/"+client.Seg(pos[0]), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "recording %s deleted\n", pos[0])
			return nil
		})
	case "summary":
		return recSummary(e, args[1:]) // summary.go
	case "process":
		return recProcess(e, args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(e.out, recUsage)
		return nil
	}
	return usageErr("unknown rec command %q\n%s", args[0], recUsage)
}

func recLength(ms int64) string {
	d := (time.Duration(ms) * time.Millisecond).Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func recBytes(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)>>10)
}

// recProcess is the report worker's one command (internal/recworker): it asks this daemon, as the agent it runs
// inside, to make the recording's report, then prints each new state until the report is made or failed. It
// prints states and the failure's reason only: never a transcript line or the summary (the worker is a cloud model).
func recProcess(e *env, args []string) error {
	if len(args) != 1 || !recorder.ValidID(args[0]) { // one recording id, nothing else (the worker's only command)
		return usageErr(recUsage)
	}
	url, tok := os.Getenv(fleet.EnvURL), os.Getenv(fleet.EnvToken)
	if url == "" || tok == "" {
		return fmt.Errorf("tmx rec process runs only inside the report worker the recorder launched")
	}
	c := client.New(url, tok)
	path := "/api/recordings/" + client.Seg(args[0]) + "/process"
	cx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	last := model.SummaryState("")
	for {
		var x api.ProcessResponse
		if err := c.Post(cx, path, nil, &x); err != nil {
			return err
		}
		if x.State != last {
			fmt.Fprintf(e.out, "%s %s\n", args[0], x.State)
			last = x.State
		}
		switch {
		case x.State == model.SummaryFailed:
			return fmt.Errorf("report of %s failed: %s", args[0], x.Error)
		case !x.State.Pending() && x.State != model.SummaryNone:
			return nil
		}
		select {
		case <-cx.Done():
			return fmt.Errorf("report of %s is still %s after 2 hours", args[0], x.State)
		case <-time.After(5 * time.Second):
		}
	}
}
