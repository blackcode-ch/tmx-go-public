package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
	"github.com/blackcode-ch/tmx-go/internal/runner/fake"
)

// An agent whose claude start waits behind a Claude sign-in says so (QueueReason while
// starting), and the reason is cleared once it may start.
func TestAgentShowsWaitBehindClaudeSignIn(t *testing.T) {
	e := newEnv(t)
	dir, err := config.Paths{Home: e.home}.EnsureClaudeConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	// Like the real runners: the start waits in the queue inside Start.
	e.chat.Script(func(p *fake.Process) {
		tk, err := claude.BeginStart(context.Background(), p.Spec.Env, claude.HolderAgent, p.Spec.OnStartWait)
		if err == nil {
			tk.Release()
		}
	}, nil)
	f := e.started()
	project := workerProject(t, f)
	login, err := claude.QueueFor(dir).BeginHeld(context.Background(), claude.HolderSignIn, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(login.Release)
	go f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: project, Name: "w-wait"}) // returns once it started
	eventually(t, "the agent shows the sign-in wait", func() bool {
		a, err := f.Agent("w-wait")
		return err == nil && a.State == model.StateStarting && a.QueueReason == claude.WaitSignIn
	})
	login.Release() // the sign-in exited
	eventually(t, "started, reason cleared", func() bool {
		a := mustAgent(t, f, "w-wait")
		return e.chat.Last(a.ID) != nil && a.QueueReason == "" // its process started
	})
}
