package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/blackcode-ch/tmx-go/internal/client"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/resources"
	"github.com/blackcode-ch/tmx-go/internal/slots"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// newResourceControl builds the daemon's slots and resource manager from config.json.
func newResourceControl(e *env, log *slog.Logger) (*slots.Manager, *resources.Manager) {
	paths := e.paths
	sl := slots.New(slots.Options{Alive: sysinfo.Alive})
	rm := resources.New(resources.Options{
		Paths: paths, Config: e.cfg.EffectiveResources(), Workspace: e.cfg.Workspace, Slots: sl, Logger: log,
		Save: func(r config.Resources) error {
			c, err := config.Load(paths) // re-read so hand edits to config.json survive
			if err != nil {
				return err
			}
			c.Resources = &r
			return config.Save(paths, c)
		},
	})
	return sl, rm
}

// attachResourceControl connects resource control and slots to the fleet's event bus.
func attachResourceControl(f *fleet.Fleet, sl *slots.Manager, rm *resources.Manager) {
	rm.Attach(f)
	sl.Notify(func(v slots.View) { f.Publish(model.BusEvent{Type: model.EvSlotsUpdated, Data: v}) })
}

func cmdStart(e *env, args []string) error {
	fs := newFlags("start")
	force := fs.Bool("force", false, "start now even over the machine's limits")
	noWait := fs.Bool("no-wait", false, "refuse instead of queueing")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx start NAME [--force] [--no-wait]")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var a model.Agent
		body := fleet.StartOptions{Force: *force, NoWait: *noWait}
		if err := c.Post(cx, "/api/agents/"+client.Seg(pos[0])+"/start", body, &a); err != nil {
			return err
		}
		fmt.Fprintln(e.out, agentLine(a))
		return nil
	})
}

// agentLine is "name: state", with the queue position or the pause reason.
func agentLine(a model.Agent) string {
	switch a.State {
	case model.StateQueued:
		return fmt.Sprintf("%s: queued, %s in line (%s)", a.Name, ordinal(a.QueuePos), a.QueueReason)
	case model.StatePaused:
		return fmt.Sprintf("%s: paused (%s)", a.Name, a.PausedDetail)
	}
	return fmt.Sprintf("%s: %s", a.Name, a.State)
}

func cmdResume(e *env, args []string) error {
	pos, err := parse(newFlags("resume"), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx resume NAME")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var a model.Agent
		if err := c.Post(cx, "/api/agents/"+client.Seg(pos[0])+"/resume", nil, &a); err != nil {
			return err
		}
		fmt.Fprintln(e.out, agentLine(a))
		return nil
	})
}

func cmdExempt(e *env, args []string) error {
	fs := newFlags("exempt")
	hours := fs.Float64("hours", 24, "how long; 0 lifts the exemption")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: tmx exempt NAME [--hours H]")
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		if err := c.Post(cx, "/api/resources/exempt", map[string]any{"agent": pos[0], "hours": *hours}, nil); err != nil {
			return err
		}
		if *hours == 0 {
			fmt.Fprintf(e.out, "%s: brakes apply again\n", pos[0])
		} else {
			fmt.Fprintf(e.out, "%s: no brakes for %g hours\n", pos[0], *hours)
		}
		return nil
	})
}

// cmdResources prints the machine strip and every agent's usage.
func cmdResources(e *env, args []string) error {
	if _, err := parse(newFlags("resources"), args); err != nil {
		return err
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var v resources.View
		if err := c.Get(cx, "/api/resources", &v); err != nil {
			return err
		}
		var snap model.Snapshot
		if err := c.Get(cx, "/api/snapshot", &snap); err != nil {
			return err
		}
		m := v.Sample.Machine
		var sl []string
		for _, k := range v.Slots.Kinds {
			sl = append(sl, fmt.Sprintf("%s %d/%d", k.Kind, k.Held, k.Size))
		}
		fmt.Fprintf(e.out, "machine %s: CPU %.0f%% · RAM %s free of %s · disk %s free · %d running, %d queued, %d paused · slots %s\n",
			snap.Machine, m.CPU, resources.Bytes(int64(m.MemFree)), resources.Bytes(int64(m.MemTotal)),
			resources.Bytes(int64(m.DiskFree)), m.Running, m.Queued, m.Paused, strings.Join(sl, ", "))
		if m.Saturated {
			fmt.Fprintln(e.out, "the disk is saturated")
		}
		fmt.Fprintf(e.out, "sampler: %.2f ms per %d s sample (%.4f%% of one core)\n\n", v.Cost.AvgMs, v.Sample.Interval, v.Cost.CPUPct)
		use := map[string]resources.AgentUsage{}
		for _, u := range v.Sample.Agents {
			use[u.AgentID] = u
		}
		tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSTATE\tCPU\tMEMORY\tWRITTEN TODAY\tBUDGET")
		for _, a := range snap.Agents {
			u := use[a.ID]
			st := string(a.State)
			if a.State == model.StateQueued {
				st = fmt.Sprintf("queued #%d", a.QueuePos)
			} else if a.State == model.StatePaused {
				st = "paused: " + a.PausedReason
			}
			budget := "-"
			if u.Budget > 0 {
				budget = resources.Bytes(int64(u.Budget))
			}
			if u.ExemptUntil > 0 {
				budget = "exempt"
			}
			fmt.Fprintf(tw, "%s\t%s\t%.0f%%\t%s\t%s\t%s\n", a.Name, st, u.CPU, resources.Bytes(int64(u.Mem)),
				resources.Bytes(int64(u.WrittenToday)), budget)
		}
		return tw.Flush()
	})
}
