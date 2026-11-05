package claude

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A start waiting behind a held sign-in hears why, and "" the moment it has the turn.
func TestWaitReasonFollowsTheHolder(t *testing.T) {
	q, dir := testQueue(t, 10*time.Millisecond, time.Hour)
	login, err := q.BeginHeld(context.Background(), HolderSignIn, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Holder() != HolderSignIn {
		t.Fatalf("holder %q", q.Holder())
	}
	var mu sync.Mutex
	var heard []string
	got := make(chan *StartTicket, 1)
	go func() {
		tk, err := BeginStart(context.Background(), []string{EnvConfigDir + "=" + dir}, HolderAgent, func(r string) {
			mu.Lock()
			heard = append(heard, r)
			mu.Unlock()
		})
		if err != nil {
			t.Error(err)
		}
		got <- tk
	}()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if len(heard) != 1 || heard[0] != WaitSignIn {
		t.Errorf("while the sign-in holds: %q", heard)
	}
	mu.Unlock()
	login.Release() // the sign-in exited
	tk := <-got
	if tk == nil {
		t.Fatal("no turn")
	}
	mu.Lock()
	if len(heard) != 2 || heard[1] != "" {
		t.Errorf("after the turn: %q", heard)
	}
	mu.Unlock()
	if q.Holder() != HolderAgent {
		t.Fatalf("holder %q", q.Holder())
	}
	tk.Release()
	if q.Holder() != "" || WaitReason(HolderAgent) != WaitOther {
		t.Fatal("holder or reason")
	}
}
