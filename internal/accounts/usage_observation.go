package accounts

import (
	"crypto/sha256"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func (s *Service) observeUsage() {
	if s.o.UsageObserved != nil {
		s.o.UsageObserved(s.usageObservation(s.Payload()))
	}
}

func (s *Service) usageObservation(p Payload) []model.AccountUsageReading {
	now := s.now()
	out := make([]model.AccountUsageReading, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		name := a.Name
		if name == "" {
			name = DefaultName
		}
		r := model.AccountUsageReading{Harness: a.Harness, Name: name, CheckedAt: a.CheckedAt,
			Connected: a.Connected && a.Login.State != LoginRunning, Refreshing: a.Refreshing,
			QuotaKnown:  a.Error == "" && a.LimitsNote == "",
			IdentityKey: sha256.Sum256([]byte(a.Harness + "\x00" + name + "\x00" + strings.ToLower(a.Account.Email))),
		}
		for _, l := range a.Limits {
			credits := -1
			if a.Harness == string(Claude) {
				switch l.ID {
				case "week":
					credits = resetRemaining(a, "weekly", now, false)
				case "week_fable":
					credits = resetRemaining(a, "fable", now, false)
				}
			}
			r.Limits = append(r.Limits, model.AccountUsageLimit{ID: l.ID, Label: l.Label, UsedPercent: l.UsedPercent, ResetsAt: l.ResetsAt, FreeResets: credits})
		}
		out = append(out, r)
	}
	return out
}
