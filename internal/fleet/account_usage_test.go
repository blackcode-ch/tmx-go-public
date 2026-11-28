package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func TestAccountUsageWarningsRearmBothThresholdsAfterResetAndRestart(t *testing.T) {
	for _, scenario := range []string{"early", "same end", "end 30s later", "end 30s earlier", "natural", "unknown first", "unknown after expiry"} {
		t.Run(scenario, func(t *testing.T) {
			e := newEnv(t)
			f := e.started()
			now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
			firstEnd := now.Add(3 * time.Hour)
			observe := func(at, checked, end time.Time, used float64) {
				var reset int64
				if !end.IsZero() {
					reset = end.UnixMilli()
				}
				f.observeAccountUsage([]model.AccountUsageReading{{
					Harness: "claude", Name: "default", Connected: true, QuotaKnown: true, CheckedAt: checked.UnixMilli(),
					Limits: []model.AccountUsageLimit{{ID: "week", Label: "Weekly", UsedPercent: used, ResetsAt: reset, FreeResets: -1}},
				}}, at)
			}
			assertWarnings := func(want int) {
				t.Helper()
				rows, err := e.st.ListMessages(store.MessageFilter{From: model.WatchParty, To: f.HeadID()})
				if err != nil {
					t.Fatal(err)
				}
				var eighty, ninety int
				for _, m := range rows {
					if strings.Contains(m.Body, "reached 80%") {
						eighty++
					}
					if strings.Contains(m.Body, "reached 90%") {
						ninety++
					}
				}
				if eighty != want || ninety != want {
					t.Fatalf("80%%=%d and 90%%=%d; want %d each", eighty, ninety, want)
				}
			}
			initialEnd := firstEnd
			if scenario == "unknown first" {
				initialEnd = time.Time{}
			}
			initialUse := 93.
			initialWarnings := 1
			if scenario == "unknown after expiry" {
				initialUse, initialWarnings = 30, 0
			}
			observe(now, now, initialEnd, initialUse)
			assertWarnings(initialWarnings)
			if scenario == "unknown first" {
				bind := now.Add(time.Minute)
				observe(bind, bind, firstEnd, 93)
				assertWarnings(1)
			}
			if scenario == "early" || scenario == "end 30s later" || scenario == "end 30s earlier" {
				jitter := now.Add(2 * time.Minute)
				observe(jitter, jitter, firstEnd.Add(30*time.Second), 75)
				observe(jitter.Add(time.Second), jitter.Add(time.Second), firstEnd.Add(30*time.Second), 93)
				assertWarnings(1)
			}
			if scenario == "same end" {
				observe(now, now, firstEnd, 3)
				observe(now.Add(time.Minute), now.Add(time.Minute), firstEnd, 75)
				observe(now.Add(2*time.Minute), now.Add(2*time.Minute), time.Time{}, 3)
				observe(now.Add(3*time.Minute), now.Add(3*time.Minute), firstEnd, 93)
				assertWarnings(1)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := f.Close(ctx); err != nil {
				t.Fatal(err)
			}
			f = e.started()
			resetAt := firstEnd.Add(time.Minute)
			if scenario == "early" || scenario == "same end" || scenario == "end 30s later" || scenario == "end 30s earlier" {
				resetAt = now.Add(10 * time.Minute)
			}
			nextEnd := resetAt.Add(7 * 24 * time.Hour)
			switch scenario {
			case "same end":
				nextEnd = firstEnd
			case "end 30s later":
				nextEnd = firstEnd.Add(30 * time.Second)
			case "end 30s earlier":
				nextEnd = firstEnd.Add(-30 * time.Second)
			}
			if scenario == "unknown after expiry" {
				observe(resetAt, resetAt, time.Time{}, 93)
				assertWarnings(1)
				known := resetAt.Add(time.Minute)
				observe(known, known, nextEnd, 93)
				assertWarnings(1)
				return
			}
			observe(resetAt, resetAt, nextEnd, 3)
			assertWarnings(1)

			// A delayed older high snapshot cannot recreate the old window.
			observe(resetAt.Add(time.Second), resetAt.Add(-time.Second), firstEnd, 93)
			assertWarnings(1)
			high := resetAt.Add(2 * time.Minute)
			observe(high, high.Add(-16*time.Minute), nextEnd, 93)
			assertWarnings(1)
			observe(high, high, nextEnd, 93)
			observe(high.Add(time.Second), high, nextEnd, 93)
			assertWarnings(2)
		})
	}
}

func TestAccountUsageWarningsCrossingsAttributionAndDailyDigest(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	sample := []model.AccountUsageReading{
		{Harness: "claude", Name: "default", Connected: true, QuotaKnown: true, CheckedAt: now.UnixMilli(),
			Limits: []model.AccountUsageLimit{
				{ID: "week", Label: "Weekly", UsedPercent: 79, ResetsAt: reset.UnixMilli(), FreeResets: 2},
				{ID: "week_fable", Label: "Fable weekly", UsedPercent: 78, ResetsAt: reset.UnixMilli(), FreeResets: 0},
				{ID: "session", UsedPercent: 99, ResetsAt: reset.UnixMilli()},
				{ID: "week_sonnet", UsedPercent: 79, FreeResets: -1},
			}},
		{Harness: "claude", Name: "disconnected", CheckedAt: now.UnixMilli(), Limits: []model.AccountUsageLimit{{ID: "week", UsedPercent: 99, ResetsAt: reset.UnixMilli()}}},
	}
	for _, row := range []struct {
		name, account, model string
		age                  time.Duration
		turns                int
	}{
		{"reporter", "default", "claude-fable-5", time.Hour, 8},
		{"builder", "default", "claude-sonnet-5", 2 * time.Hour, 12},
		{"other-account", "other", "claude-fable-5", time.Hour, 40},
		{"outdated", "default", "claude-fable-5", 25 * time.Hour, 100},
	} {
		if err := e.st.RecordAccountTurns("claude", row.account, store.AccountTurnUsage{
			AgentID: row.name, Name: row.name, Model: row.model, Turns: row.turns,
		}, now.Add(-row.age).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	mail := func() []model.Message {
		t.Helper()
		rows, err := e.st.ListMessages(store.MessageFilter{From: model.WatchParty, To: f.HeadID()})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	f.observeAccountUsage(sample, now)
	if got := mail(); len(got) != 1 || !strings.HasPrefix(got[0].Body, "accounts today") {
		t.Fatalf("below threshold: %+v", got)
	}
	limits := sample[0].Limits
	limits[0].UsedPercent = 80
	limits[1].UsedPercent = 91
	limits[3].UsedPercent = 80
	f.observeAccountUsage(sample, now)
	f.observeAccountUsage(sample, now.Add(time.Second))
	got := mail()
	if len(got) != 5 {
		t.Fatalf("threshold/window duplicates: %+v", got)
	}
	for _, m := range got {
		switch {
		case strings.Contains(m.Body, "· Weekly"):
			for _, want := range []string{"reached 80%", "Remaining free resets: 2", "builder (12), reporter (8)"} {
				if !strings.Contains(m.Body, want) {
					t.Errorf("weekly mail misses %q: %s", want, m.Body)
				}
			}
		case strings.Contains(m.Body, "· Fable weekly"):
			if strings.Contains(m.Body, "builder") || !strings.Contains(m.Body, "reporter (8)") ||
				!strings.Contains(m.Body, "Remaining free resets: 0") {
				t.Errorf("model attribution: %s", m.Body)
			}
		case strings.Contains(m.Body, "· week_sonnet"):
			if !strings.Contains(m.Body, "Reset time: unknown") || !strings.Contains(m.Body, "Remaining free resets: unknown") {
				t.Errorf("unavailable fields were fabricated: %s", m.Body)
			}
		}
		if strings.Contains(m.Body, "outdated") || strings.Contains(m.Body, "other-account") {
			t.Fatalf("unsupported attribution: %s", m.Body)
		}
	}
	later := now.Add(24 * time.Hour)
	sample[0].CheckedAt = later.UnixMilli()
	limits[0].ResetsAt = later.Add(time.Hour).UnixMilli()
	f.observeAccountUsage(sample, later)
	f.observeAccountUsage(sample, later.Add(time.Second))
	if got := mail(); len(got) != 7 {
		t.Fatalf("new window/day did not rearm correctly: %+v", got)
	}
}

func TestManagedTurnUsageCapturesAccountAtResultTime(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	a, proc := talkingAgent(t, e, f, "usage-reporter")
	proc.Emit(runner.Event{Kind: runner.EvUsage, Turns: 3})
	eventually(t, "per-account turn usage", func() bool {
		now := time.Now()
		rows, err := e.st.AccountTurns("claude", accountProfileName(a.Account), now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
		return err == nil && len(rows) == 1 && rows[0].AgentID == a.ID && rows[0].Turns >= 3
	})
}
