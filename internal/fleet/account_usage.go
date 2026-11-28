package fleet

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func accountProfileName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

func (f *Fleet) recordAccountTurnsLocked(a model.Agent, turns int) {
	h := AccountHarness(a.Harness)
	switch h {
	case model.HarnessClaude, model.HarnessCodex, model.HarnessGemini, "antigravity":
	default:
		return
	}
	err := f.st.RecordAccountTurns(string(h), accountProfileName(a.Account), store.AccountTurnUsage{
		AgentID: a.ID, Name: a.Name, Model: a.Model, Turns: turns,
	}, model.Now())
	if err != nil {
		f.log.Warn("record account turns", "agent", a.ID, "err", err)
	}
}

// ObserveAccountUsage uses the account service's own snapshot without making a new probe.
func (f *Fleet) ObserveAccountUsage(p []model.AccountUsageReading) {
	f.observeAccountUsage(p, time.Now())
}

func (f *Fleet) observeAccountUsage(p []model.AccountUsageReading, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head := f.headLocked()
	if f.closed || head == nil {
		return
	}
	queued := false
	queue := func(key, body string, observedAt, expires int64) bool {
		at := now.UnixMilli()
		m := model.Message{ID: model.NewID(), From: model.WatchParty, To: head.a.ID, State: model.MsgQueued,
			Receipt: "queued", CreatedAt: at, ReceiptAt: at, Body: body}
		created, err := f.st.QueueAccountUsageNotice(key, observedAt, expires, m)
		if err != nil {
			f.log.Warn("queue account usage notice", "err", err)
			return false
		}
		if created {
			f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: head.a.ID, Data: m})
			queued = true
		}
		return created
	}
	at := now.UnixMilli()
	connected, unknown, warning, critical := 0, 0, 0, 0
	checked, refreshing := false, false
	for _, a := range p {
		checked = checked || a.CheckedAt > 0
		refreshing = refreshing || a.Refreshing
		if !a.Connected {
			continue
		}
		connected++
		known := false
		// Normal account checks run every five minutes. Older readings can
		// remain on the account card but cannot advance a warning window.
		if a.QuotaKnown && a.CheckedAt > at-(15*time.Minute).Milliseconds() && a.CheckedAt <= at {
			for _, l := range a.Limits {
				modelKey, eligible := trackedAccountLimit(a.Harness, l.ID)
				if !eligible || math.IsNaN(l.UsedPercent) || l.UsedPercent < 0 || l.UsedPercent > 100 ||
					(l.ResetsAt > 0 && l.ResetsAt <= at) {
					continue
				}
				known = true
				scope := fmt.Sprintf("quota/%x/%q", a.IdentityKey, l.ID)
				if !a.Refreshing {
					fresh, err := f.st.ObserveAccountQuotaWindow(scope, a.CheckedAt, l.ResetsAt, l.UsedPercent)
					if err != nil {
						f.log.Warn("observe account quota window", "err", err)
					}
					if err != nil || !fresh {
						continue
					}
				}
				for _, level := range []int{80, 90} {
					key := fmt.Sprintf("%s/%d", scope, level)
					if l.UsedPercent < float64(level) {
						if !a.Refreshing && l.ResetsAt > at {
							if err := f.st.ObserveAccountUsageWindow(key, a.CheckedAt, l.ResetsAt); err != nil {
								f.log.Warn("extend account usage notice window", "err", err)
							}
						}
						continue
					}
					if level == 80 {
						warning++
					} else {
						critical++
					}
					if a.Refreshing {
						continue
					}
					queue(key, f.accountWarning(a, l, modelKey, level, now), a.CheckedAt, l.ResetsAt)
				}
			}
		}
		if !known {
			unknown++
		}
	}
	if checked && !refreshing {
		nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		digest := fmt.Sprintf("accounts today — %d connected; %d tracked limits at 80%% or higher (%d at 90%% or higher); %d connected accounts without a current weekly or model reading.",
			connected, warning, critical, unknown)
		if queue("daily-accounts", digest, at, nextDay.UnixMilli()) {
			if err := f.st.PruneAccountTurns(now.Add(-24 * time.Hour).UnixMilli()); err != nil {
				f.log.Warn("prune account turns", "err", err)
			}
		}
	}
	if queued {
		id := head.a.ID
		f.refreshQueuedLocked(head)
		f.goTracked(func() { f.tryDeliver(id) })
	}
}

// The native readers emit week for the whole account, week_<model> for Claude
// model windows, and a model/bucket prefix before a Codex window's suffix.
func trackedAccountLimit(harness, id string) (model string, eligible bool) {
	if id == "week" {
		return "", true
	}
	if harness == "claude" {
		return strings.CutPrefix(id, "week_")
	}
	if harness == "codex" {
		for _, suffix := range []string{"_week", "_day", "_5h", "_primary", "_secondary", "_month"} {
			if key, ok := strings.CutSuffix(id, suffix); ok {
				return key, true
			}
		}
	}
	if harness == "antigravity" {
		for _, prefix := range []string{"claude_", "gemini_", "gpt_"} {
			if strings.HasPrefix(id, prefix) {
				return id, true
			}
		}
	}
	return "", false
}

func (f *Fleet) accountWarning(a model.AccountUsageReading, l model.AccountUsageLimit, modelKey string, level int, now time.Time) string {
	label := l.Label
	if label == "" {
		label = l.ID
	}
	credits := "unknown"
	if l.FreeResets >= 0 {
		credits = fmt.Sprint(l.FreeResets)
	}
	reset := "unknown"
	if l.ResetsAt > 0 {
		reset = time.UnixMilli(l.ResetsAt).In(now.Location()).Format("2 Jan 2006 15:04 MST")
	}
	return fmt.Sprintf("%s account %q · %s reached %d%% (reported %.1f%%). Reset time: %s. Remaining free resets: %s. Managed agents by reported turns, last 24 h: %s. Provider quota attribution is unknown.",
		a.Harness, accountProfileName(a.Name), label, level, l.UsedPercent,
		reset,
		credits, f.accountHeavyUsers(a, modelKey, now))
}

func (f *Fleet) accountHeavyUsers(a model.AccountUsageReading, modelKey string, now time.Time) string {
	rows, err := f.st.AccountTurns(a.Harness, accountProfileName(a.Name), now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli())
	if err != nil {
		f.log.Warn("read account turns", "err", err)
		return "unknown"
	}
	byAgent := map[string]store.AccountTurnUsage{}
	for _, row := range rows {
		if modelKey != "" && !accountModelMatches(a.Harness, modelKey, row.Model) {
			continue
		}
		current := byAgent[row.AgentID]
		row.Turns += current.Turns
		byAgent[row.AgentID] = row
	}
	top := make([]store.AccountTurnUsage, 0, len(byAgent))
	for _, row := range byAgent {
		top = append(top, row)
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Turns != top[j].Turns {
			return top[i].Turns > top[j].Turns
		}
		return top[i].AgentID < top[j].AgentID
	})
	var labels []string
	for _, row := range top[:min(3, len(top))] {
		if r := f.agents[row.AgentID]; r != nil {
			row.Name = r.a.Name
		}
		labels = append(labels, fmt.Sprintf("%s (%d)", row.Name, row.Turns))
	}
	if len(labels) == 0 {
		return "unknown (no matching managed turn counts)"
	}
	return strings.Join(labels, ", ")
}

func accountModelMatches(harness, key, model string) bool {
	model = strings.ReplaceAll(strings.ToLower(model), "-", "_")
	model = strings.ReplaceAll(model, ".", "_")
	if harness == "claude" {
		return slices.Contains(strings.Split(model, "_"), key)
	}
	key = strings.TrimPrefix(key, "codex_")
	return model == key || strings.HasPrefix(model, key+"_")
}
