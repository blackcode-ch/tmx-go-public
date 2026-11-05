package fleet

import (
	"sync"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func TestRateLimitHook(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	var mu sync.Mutex
	var got []runner.RateLimit
	var harnesses []model.Harness
	f.SetRateLimitHook(func(h model.Harness, _ string, rl runner.RateLimit) {
		mu.Lock()
		defer mu.Unlock()
		got, harnesses = append(got, rl), append(harnesses, h)
	})
	head := mustAgent(t, f, "head")
	e.chat.Last(head.ID).Emit(runner.Event{Kind: runner.EvRateLimit,
		RateLimit: &runner.RateLimit{Status: "rejected", Type: "five_hour", ResetsAt: 1800000000}})
	eventually(t, "the hook", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if got[0] != (runner.RateLimit{Status: "rejected", Type: "five_hour", ResetsAt: 1800000000}) || harnesses[0] != head.Harness {
		t.Fatalf("%+v %q", got, harnesses)
	}
}
