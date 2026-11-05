package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/guardhook"
)

// cmdHook is Claude Code's PreToolUse hook for guarded agents (started by claude, not by people).
func cmdHook(e *env, args []string) error {
	if len(args) != 1 || args[0] != "pretooluse" {
		return usageErr("usage: tmx hook pretooluse (Claude Code runs it for tmx's agents)")
	}
	cx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return guardhook.Run(cx, os.Stdin, e.out, guardhook.FromEnv())
}

// ownerDo is c.Do for an owner action the owner gate may guard: on 428 it tells the owner to
// confirm on this machine, waits for the daemon's verdict and retries once with the proof.
func ownerDo(e *env, c *client.Client, cx context.Context, method, path string, body, out any) error {
	err := c.Do(cx, method, path, body, out)
	var ae *client.APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusPreconditionRequired {
		return err
	}
	var need struct {
		Confirmation gate.Confirmation `json:"confirmation"`
	}
	if json.Unmarshal(ae.Body, &need) != nil || need.Confirmation.ID == "" {
		return err
	}
	conf := need.Confirmation
	fmt.Fprintf(e.errw, "Confirm on this machine with %s: tmx is trying to %s.\n", conf.MethodLabel, conf.Reason)
	sig, stop := signal.NotifyContext(cx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	for conf.State == gate.StatePending {
		var next gate.Confirmation
		if err := c.Get(sig, "/api/gate/confirmations/"+client.Seg(conf.ID)+"?wait=25000", &next); err != nil {
			if sig.Err() != nil {
				_ = c.Do(context.Background(), http.MethodDelete, "/api/gate/confirmations/"+client.Seg(conf.ID), nil, nil)
				return exitError{code: 130, msg: "cancelled"}
			}
			return err
		}
		conf = next
	}
	if conf.State != gate.StateConfirmed {
		return fmt.Errorf("not confirmed (%s)", strings.TrimSpace(conf.Error))
	}
	retry := *c
	retry.Header = http.Header{api.ConfirmHeader: {conf.ID}}
	return retry.Do(cx, method, path, body, out)
}

func cmdGate(e *env, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	const usage = "usage: tmx gate [status|on|off] | tmx gate grace off|5m|15m|30m|1h|2h|4h|8h|24h | tmx gate lock"
	grace := -1
	switch {
	case sub == "grace" && len(args) == 2:
		n, err := parseGrace(args[1])
		if err != nil {
			return usageErr("%v\n%s", err, usage)
		}
		grace = n
	case sub == "grace" || len(args) > 1:
		return usageErr(usage)
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var st gate.Status
		switch sub {
		case "status":
			if err := c.Get(cx, "/api/gate", &st); err != nil {
				return err
			}
		case "on", "off":
			if err := ownerDo(e, c, cx, http.MethodPut, "/api/gate", map[string]bool{"enabled": sub == "on"}, &st); err != nil {
				return err
			}
		case "grace": // a longer window asks for a fresh proof; shorter or off does not
			if err := ownerDo(e, c, cx, http.MethodPut, "/api/gate/grace", map[string]int{"grace_minutes": grace}, &st); err != nil {
				return err
			}
		case "lock":
			if err := c.Do(cx, http.MethodPost, "/api/gate/lock", nil, &st); err != nil {
				return err
			}
		default:
			return usageErr(usage)
		}
		state := "off"
		if st.Enabled {
			state = "on"
		}
		fmt.Fprintf(e.out, "owner gate: %s (%s)\n", state, st.MethodLabel)
		if st.Unavailable != "" {
			fmt.Fprintf(e.out, "  not available: %s\n", st.Unavailable)
		}
		if st.Enabled {
			fmt.Fprintf(e.out, "  asks for: %s\n", strings.Join(st.Gated, "; "))
		}
		fmt.Fprintf(e.out, "  %s\n", graceLine(st))
		if st.Recovery != "" {
			fmt.Fprintf(e.out, "  recovery: %s\n", st.Recovery)
		}
		return nil
	})
}

// parseGrace reads the window's length: off, minutes (60) or a duration (90m, 1h, 24h).
func parseGrace(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	n := -1
	switch {
	case s == "off":
		n = 0
	case s != "" && strings.Trim(s, "0123456789") == "":
		if v, err := strconv.Atoi(s); err == nil {
			n = v
		}
	default:
		if d, err := time.ParseDuration(strings.TrimSuffix(s, "in")); err == nil && d%time.Minute == 0 { // 5min too
			n = int(d / time.Minute)
		}
	}
	if !gate.ValidGrace(n) {
		return 0, fmt.Errorf("%q: %v", s, gate.ErrGraceStep)
	}
	return n, nil
}

// graceLine says what the window does now: "no prompt for 1 hour after a confirmation (open
// until 15:42)".
func graceLine(st gate.Status) string {
	if st.GraceMinutes == 0 {
		return "asks every time (no-ask window off: tmx gate grace 1h)"
	}
	line := "after a confirmation, no prompt for " + gate.GraceLabel(st.GraceMinutes)
	if st.GraceUntil > 0 {
		return line + " (open until " + time.UnixMilli(st.GraceUntil).Format("15:04") + "; tmx gate lock ends it)"
	}
	return line + " (locked now; a restart locks it too)"
}

func cmdGuard(e *env, args []string) error {
	fs := newFlags("guard")
	forKind := fs.String("for", "", "only for this kind of agent (head, lead, worker)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	sub := "status"
	if len(pos) > 0 {
		sub = pos[0]
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var info api.SafetyInfo
		if err := c.Get(cx, "/api/safety", &info); err != nil {
			return err
		}
		s := info.Guard
		switch sub {
		case "status":
		case "on", "off":
			next := guard.Settings{Enabled: s.Enabled, OffFor: []string{}}
			for _, k := range s.OffFor {
				if k != *forKind {
					next.OffFor = append(next.OffFor, k)
				}
			}
			switch {
			case *forKind == "":
				next = guard.Settings{Enabled: sub == "on", OffFor: []string{}}
			case sub == "off":
				next.OffFor = append(next.OffFor, *forKind)
			}
			if err := ownerDo(e, c, cx, http.MethodPut, "/api/safety/guard", next, &s); err != nil {
				return err
			}
		default:
			return usageErr("usage: tmx guard [status|on|off] [--for head|lead|worker]")
		}
		state := "on"
		if !s.Enabled {
			state = "off"
		}
		fmt.Fprintf(e.out, "dangerous-command guard: %s", state)
		if s.Enabled && len(s.OffFor) > 0 {
			fmt.Fprintf(e.out, " (off for %s)", strings.Join(s.OffFor, ", "))
		}
		fmt.Fprintln(e.out, "\n  applies to chat-mode Claude agents started from now on (restart running agents)")
		return nil
	})
}
