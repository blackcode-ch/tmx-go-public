package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/accounts"
	"github.com/blackcode-ch/tmx-go/internal/client"
)

// cmdAccounts: `tmx accounts [--json]` lists the AI accounts the harnesses are signed in to and
// how much of each plan's limits is used; `tmx accounts refresh [HARNESS]` checks again and
// waits for the answer. Signing in is in the app (Accounts), in the harness's own terminal.
func cmdAccounts(e *env, args []string) error {
	fs := newFlags("accounts")
	asJSON := fs.Bool("json", false, "print the JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	sub := ""
	if len(pos) > 0 {
		sub = pos[0]
	}
	switch sub {
	case "", "ls", "list", "status":
		if len(pos) > 1 {
			return usageErr("usage: tmx accounts [refresh [HARNESS]] [--json]")
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var p accounts.Payload
			if err := c.Get(cx, "/api/accounts", &p); err != nil {
				return err
			}
			return e.printAccounts(p, *asJSON)
		})
	case "refresh":
		h := ""
		if len(pos) > 1 {
			h = pos[1]
		}
		if len(pos) > 2 || (h != "" && !accounts.Known(h)) {
			return usageErr("usage: tmx accounts refresh [claude|codex|antigravity|gemini]")
		}
		return withOwner(e, func(c *client.Client, cx context.Context) error {
			var p accounts.Payload
			if err := c.Post(cx, "/api/accounts/refresh", map[string]string{"harness": h}, &p); err != nil {
				return err
			}
			deadline := time.Now().Add(90 * time.Second)
			for refreshing(p, h) && time.Now().Before(deadline) {
				select {
				case <-cx.Done():
					return cx.Err()
				case <-time.After(300 * time.Millisecond):
				}
				if err := c.Get(cx, "/api/accounts", &p); err != nil {
					return err
				}
			}
			return e.printAccounts(p, *asJSON)
		})
	}
	return usageErr("tmx accounts: unknown subcommand %q (accounts, accounts refresh [HARNESS])", sub)
}

func refreshing(p accounts.Payload, h string) bool {
	for _, a := range p.Accounts {
		if a.Refreshing && (h == "" || a.Harness == h) {
			return true
		}
	}
	return false
}

func (e *env) printAccounts(p accounts.Payload, asJSON bool) error {
	if asJSON {
		e.printJSON(p)
		return nil
	}
	return writeAccounts(e.out, p, time.Now())
}

// writeAccounts prints the accounts as plain text: who is signed in, each limit's use and
// reset. Never an amount of money.
func writeAccounts(w io.Writer, p accounts.Payload, now time.Time) error {
	for i, a := range p.Accounts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%-12s %s\n", a.Label, accountLine(a))
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, l := range a.Limits {
			fmt.Fprintf(tw, "  %s\t%s used\t%s\n", l.Label, percent(l.UsedPercent), resets(l, now))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		var notes []string
		if a.LimitsNote != "" && a.Connected {
			notes = append(notes, a.LimitsNote)
		}
		if a.Error != "" {
			notes = append(notes, "error: "+a.Error)
		}
		if a.Login.State == accounts.LoginRunning || a.Login.State == accounts.LoginFailed {
			notes = append(notes, "sign-in "+a.Login.State+orEmpty(": ", a.Login.Message))
		}
		if a.Refreshing {
			notes = append(notes, "checking now…")
		} else if a.Installed && a.CheckedAt > 0 {
			notes = append(notes, "checked "+since(now, a.CheckedAt))
		}
		for _, n := range notes {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
	return nil
}

func accountLine(a accounts.Account) string {
	switch {
	case !a.Installed && a.CheckedAt == 0 && !a.Refreshing:
		return "not checked yet"
	case !a.Installed && a.Install != "":
		return "not installed (" + a.Install + ")"
	case !a.Installed:
		return "not installed"
	case !a.Connected:
		return "not signed in (sign in from the app: Accounts)"
	}
	var parts []string
	for _, s := range []string{a.Account.Email, a.Account.Plan, methodName(a.Account.Method), a.Account.Org} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "signed in"
	}
	return "signed in: " + strings.Join(parts, " · ")
}

func methodName(m string) string {
	switch m {
	case accounts.MethodSubscription:
		return "subscription"
	case accounts.MethodAPIKey:
		return "API key"
	}
	return ""
}

func percent(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d%%", int64(f))
	}
	return fmt.Sprintf("%.1f%%", f)
}

func resets(l accounts.Limit, now time.Time) string {
	switch {
	case l.ResetsText != "":
		return "resets " + l.ResetsText
	case l.ResetsAt > 0:
		t := time.UnixMilli(l.ResetsAt)
		if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
			return "resets at " + t.Format("15:04")
		}
		return "resets " + t.Format("Jan 2 at 15:04")
	}
	return ""
}

func since(now time.Time, ms int64) string {
	d := now.Sub(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	}
	return fmt.Sprintf("%d h ago", int(d/time.Hour))
}

func orEmpty(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}
