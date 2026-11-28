package accounts

import (
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestUsageObservationReportsOnlyKnownUnexpiredResetCredits(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := &Service{o: Options{Now: func() time.Time { return now }}}
	a := Account{Harness: "claude", Connected: true, CheckedAt: now.UnixMilli(),
		FreeResets: &FreeResets{Known: true, CheckedAt: now.UnixMilli(), Grants: []FreeReset{
			{Type: "weekly", Remaining: 2}, {Type: "fable", Remaining: 8, ExpiresAt: now.Add(-time.Hour).UnixMilli()},
		}},
		Limits: []Limit{{ID: "week"}, {ID: "week_fable"}, {ID: "week_sonnet"}},
	}
	p := Payload{Accounts: []Account{a}}
	got := s.usageObservation(p)
	if len(got) != 1 || !got[0].Connected || !got[0].QuotaKnown || got[0].Name != DefaultName ||
		got[0].Limits[0].FreeResets != 2 || got[0].Limits[1].FreeResets != 0 || got[0].Limits[2].FreeResets != -1 {
		t.Fatalf("observed credits: %+v", got)
	}
	now = now.Add(3 * time.Minute)
	got = s.usageObservation(p)
	if got[0].Limits[0].FreeResets != -1 {
		t.Fatal("a stale credit count was treated as known")
	}
}

func TestUsageObservationRunsOnCompletedAccountCheck(t *testing.T) {
	seen := make(chan []model.AccountUsageReading, 2)
	fx := newFixture(t, func(o *Options) {
		o.UsageObserved = func(p []model.AccountUsageReading) { seen <- p }
	}, Claude)
	fx.refresh(t, string(Claude))
	select {
	case p := <-seen:
		for _, a := range p {
			if a.Harness == string(Claude) && a.Connected && a.QuotaKnown && !a.Refreshing && len(a.Limits) > 0 {
				return
			}
		}
		t.Fatalf("no completed Claude usage reading: %+v", p)
	case <-time.After(time.Second):
		t.Fatal("no completed check observation")
	}
}
