package term

import (
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/runner"
)

type receiptPty struct {
	*blockingPty
	mu      sync.Mutex
	calls   int
	shortAt int
}

func (p *receiptPty) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == p.shortAt {
		return 0, nil
	}
	return len(b), nil
}

func TestMailReceiptFollowsActualTerminalWrites(t *testing.T) {
	for _, fail := range []int{0, 1, 2} {
		t.Run(string(rune('0'+fail)), func(t *testing.T) {
			pt := &receiptPty{blockingPty: newBlockingPty(), shortAt: fail}
			p := newTestProcess(t, pt)
			p.done = make(chan struct{})
			p.w.info.SubmitDelay = time.Millisecond
			p.w.scr.Write([]byte("────────\r\n❯ human draft\r\n────────\r\n? for shortcuts"))
			if err := p.SendMail("message", []string{"mail-one"}); err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-p.events:
				t.Fatalf("receipt before write: %+v", ev)
			default:
			}
			p.w.scr.Write([]byte("\x1b[2J\x1b[H────────\r\n❯\r\n────────\r\n? for shortcuts"))
			p.w.idleSince = time.Now().Add(-time.Hour)
			p.w.evaluate()
			timeout := time.After(time.Second)
			for {
				select {
				case ev := <-p.events:
					if ev.Kind != runner.EvMailDelivery {
						continue
					}
					want := "submitted"
					if fail > 0 {
						want = "unconfirmed"
					}
					if ev.Delivery != want || len(ev.MessageIDs) != 1 || ev.MessageIDs[0] != "mail-one" {
						t.Fatalf("receipt %+v", ev)
					}
					pt.mu.Lock()
					n := pt.calls
					pt.mu.Unlock()
					if fail == 1 && n != 1 {
						t.Fatal("submitted Enter after failed paste")
					}
					if fail != 1 && n != 2 {
						t.Fatalf("writes %d", n)
					}
					close(p.done)
					return
				case <-timeout:
					t.Fatal("no terminal receipt")
				}
			}
		})
	}
}
