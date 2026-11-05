package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp"
	"github.com/blackcode-ch/tmx-go/internal/whatsapp/watest"
)

// lockedOutput is the link command's stdout, read while the command still writes it. It
// rechecks the whole text so far on every write, so a marker split across writes still counts.
type lockedOutput struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	marker string
	seen   chan struct{} // closed once marker is in the output
}

func (o *lockedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buf.Write(p)
	select {
	case <-o.seen:
	default:
		if strings.Contains(o.buf.String(), o.marker) {
			close(o.seen)
		}
	}
	return n, err
}

func (o *lockedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// untilDeadline fires shortly before the test binary's own deadline (-timeout), so a hung
// wait fails with a message instead of a panic dump; it never fires without a deadline.
func untilDeadline(t *testing.T) <-chan time.Time {
	d, ok := t.Deadline()
	if !ok {
		return nil
	}
	return time.After(time.Until(d) - 5*time.Second)
}

func TestWaCommands(t *testing.T) {
	d := cliDaemon(t)
	d.EnableWhatsApp(t)
	if out, _, code := tmx(t, "wa"); code != 0 || !strings.Contains(out, "WhatsApp: unlinked") || !strings.Contains(out, "tmx wa link") {
		t.Fatalf("status: %d %s", code, out)
	}
	// link: prints the QR as half blocks, then returns once the phone linked tmx.
	done := make(chan [3]any, 1)
	linkOut := &lockedOutput{marker: "Scan with WhatsApp", seen: make(chan struct{})}
	go func() {
		var errs bytes.Buffer
		code := run([]string{"wa", "link"}, linkOut, &errs)
		done <- [3]any{linkOut.String(), errs.String(), code}
	}()
	// The phone can only scan after the CLI has rendered the QR. The events socket subscribes
	// before its handshake completes, so the QR event that follows the link request reaches it.
	select {
	case <-linkOut.seen:
	case res := <-done:
		t.Fatalf("link returned before showing the QR: %v\n%s\n%s", res[2], res[0], res[1])
	case <-untilDeadline(t):
		t.Fatalf("link did not show the QR; fake linking %q; output so far:\n%s", d.WaFake.Linking(), linkOut.String())
	}
	d.WaFake.Scan(watest.Self)
	var res [3]any
	select {
	case res = <-done:
	case <-untilDeadline(t):
		t.Fatalf("link did not return; output so far:\n%s", linkOut.String())
	}
	out, errs := res[0].(string), res[1].(string)
	if res[2].(int) != 0 || !strings.Contains(out, "Scan with WhatsApp") || !strings.Contains(out, "██") ||
		!strings.Contains(out, "linked: +41790000000") || !strings.Contains(errs, "can ban numbers") {
		t.Fatalf("link: %v\n%s\n%s", res[2], out, errs)
	}
	anna := "41791111111@s.whatsapp.net"
	d.WaFake.Emit(whatsapp.EvHistory{Contacts: []whatsapp.Contact{{JID: anna, Name: "Anna Muster"}},
		Messages: []whatsapp.Incoming{{Msg: model.WaMessage{ID: "A1", ChatJID: anna, SenderJID: anna, At: time.Now().UnixMilli(),
			Type: model.WaText, Text: "hello"}}}, Progress: 100})

	if out, _, code := tmx(t, "wa", "chats"); code != 0 || !strings.Contains(out, "Anna Muster") || !strings.Contains(out, anna) {
		t.Fatalf("chats: %d %s", code, out)
	}
	if out, _, code := tmx(t, "wa", "read", "anna"); code != 0 || !strings.Contains(out, "[A1]") || !strings.Contains(out, "Anna Muster: hello") {
		t.Fatalf("read: %d %s", code, out)
	}
	file := filepath.Join(t.TempDir(), "note.txt")
	os.WriteFile(file, []byte("hi"), 0o600)
	if out, errs, code := tmx(t, "wa", "send", "Anna Muster", "-f", file, "see", "attached"); code != 0 || !strings.Contains(out, "to Anna Muster: pending") {
		t.Fatalf("send: %d %s %s", code, out, errs)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(d.WaFake.SentNow()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s := d.WaFake.SentNow(); len(s) != 1 || s[0].Text != "see attached" || s[0].File == nil || s[0].File.Mime != "text/plain; charset=utf-8" {
		t.Fatalf("sent %+v", s)
	}
	if _, errs, code := tmx(t, "wa", "read", "zed"); code == 0 || !strings.Contains(errs, "no WhatsApp chat") {
		t.Fatalf("read unknown: %d %s", code, errs)
	}
	if _, errs, code := tmx(t, "wa", "unlink"); code == 0 || !strings.Contains(errs, "--yes") {
		t.Fatalf("unlink without --yes: %d %s", code, errs)
	}
	if out, _, code := tmx(t, "wa", "unlink", "--yes"); code != 0 || !strings.Contains(out, "unlinked") {
		t.Fatalf("unlink: %d %s", code, out)
	}
	if out, _, _ := tmx(t, "wa", "status"); !strings.Contains(out, "unlinked") {
		t.Fatalf("after unlink: %s", out)
	}
}
