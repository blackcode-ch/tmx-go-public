package cli

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/rollback"
)

const probationPoll = 5 * time.Second

// watchProbation is the post-swap health watchdog (DESIGN.md "Updates"). While the running binary
// is on probation it asks its own /api/health until the answer is ok with the governor not
// degraded (the database migrated before the API opened), then confirms the update. At the
// deadline without that, it calls fail: the daemon stops and starts this binary again, whose
// start restores the previous pair (a running daemon cannot replace its own database).
func watchProbation(ctx context.Context, log *slog.Logger, dir, healthURL string, now func() time.Time, poll time.Duration, fail func()) {
	p, ok, err := rollback.OnProbation(dir)
	if err != nil {
		log.Error("update probation unreadable", "err", err)
		return
	}
	if !ok {
		return
	}
	log.Info("update on probation", "version", p.To, "deadline", p.Deadline)
	client := &http.Client{Timeout: 3 * time.Second}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if probationHealthy(ctx, client, healthURL) {
			if p.Drill {
				log.Warn("update rollback DRILL: the update is healthy, failing it on purpose", "version", p.To)
				failProbation(log, dir, "rollback drill armed by the owner", fail)
				return
			}
			if err := rollback.ConfirmProbation(dir, now()); err != nil {
				log.Error("update probation: cannot confirm", "err", err)
				return
			}
			log.Info("update confirmed healthy", "version", p.To)
			return
		}
		if now().After(p.Deadline) {
			log.Error("update NOT healthy by its deadline; restarting to roll back", "version", p.To, "deadline", p.Deadline)
			failProbation(log, dir, "not confirmed healthy by "+p.Deadline.Format(time.RFC3339), fail)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func probationHealthy(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var h struct {
		OK       bool `json:"ok"`
		Degraded bool `json:"governor_degraded"`
	}
	return res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&h) == nil && h.OK && !h.Degraded
}

// failProbation marks the update failed and restarts; the next start restores the previous pair.
// If the mark cannot be written, the restart still happens: that start is past the deadline (or,
// for a drill, counts towards the start limit) and restores all the same.
func failProbation(log *slog.Logger, dir, reason string, fail func()) {
	if err := rollback.MarkFailed(dir, reason); err != nil {
		log.Error("update probation: cannot record the failure", "err", err)
	}
	fail()
}
