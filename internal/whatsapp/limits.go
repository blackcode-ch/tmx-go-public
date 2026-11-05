package whatsapp

import (
	"sync"
	"time"
)

// Sending limits. WhatsApp bans numbers that send like a bot (many messages, many chats, the
// same text again and again), so tmx sends at a person's pace whoever asks: the owner, or an
// agent the owner allowed.
const (
	// ChatGap: at least this long between two messages to one chat. A message with several
	// files is several WhatsApp messages; they wait their turn instead of being refused.
	ChatGap = 2 * time.Second
	// WindowMax messages in Window over all chats; beyond that a send is refused (429).
	WindowMax = 30
	Window    = 10 * time.Minute
	// MaxAgentPending: an agent may have this many messages waiting for the owner's Allow.
	MaxAgentPending = 5
	// MaxTextBytes is the longest text tmx sends (WhatsApp takes more; people do not write more).
	MaxTextBytes = 16 << 10
	// MaxAgentTextBytes: an agent's message is short.
	MaxAgentTextBytes = 4 << 10
	// MaxUploadBytes per file and MaxUploads per request (the composer's limits).
	MaxUploadBytes = 16 << 20
	MaxUploads     = 5
)

// window counts messages sent through tmx over the last Window (all chats).
type window struct {
	mu    sync.Mutex
	times []time.Time
}

// take reserves n sends at now, or reports how long until there is room.
func (w *window) take(now time.Time, n int) (bool, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cut := now.Add(-Window)
	i := 0
	for i < len(w.times) && !w.times[i].After(cut) {
		i++
	}
	w.times = w.times[i:]
	if len(w.times)+n > WindowMax {
		if n > WindowMax {
			return false, Window
		}
		return false, w.times[len(w.times)+n-WindowMax-1].Sub(cut)
	}
	for range n {
		w.times = append(w.times, now)
	}
	return true, 0
}

// seed loads the sends of the last Window (after a restart).
func (w *window) seed(ms []int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range ms {
		w.times = append(w.times, time.UnixMilli(t))
	}
}
