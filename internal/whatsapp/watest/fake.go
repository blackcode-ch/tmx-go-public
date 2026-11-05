// Package watest is a fake WhatsApp connection for tests: it links when the test says the
// phone scanned, records what is sent, and serves attachments the test put in.
package watest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/whatsapp"
)

// Self is the account the fake links by default.
var Self = whatsapp.Account{JID: "41790000000@s.whatsapp.net", Phone: "+41790000000", Name: "Owner"}

// Fake implements whatsapp.Client.
type Fake struct {
	mu      sync.Mutex
	h       func(whatsapp.Event)
	linked  bool
	acct    whatsapp.Account
	linking string
	n       int

	Sent      []whatsapp.Outgoing
	Uploaded  []whatsapp.OutFile
	Reads     [][]string // chat, sender, ids...
	Downloads int
	Media     map[string][]byte // direct path → the attachment
	Names     map[string]string // jid → name (ChatName)
	SendErr   error
	LoggedOut bool
}

// New is a fake with nothing linked; Linked is one that was linked before (a restart).
func New() *Fake { return &Fake{Media: map[string][]byte{}, Names: map[string]string{}} }

func Linked() *Fake {
	f := New()
	f.linked, f.acct = true, Self
	return f
}

func (f *Fake) emit(ev whatsapp.Event) {
	f.mu.Lock()
	h := f.h
	f.mu.Unlock()
	if h != nil {
		h(ev)
	}
}

// Emit delivers an event as if it came from WhatsApp.
func (f *Fake) Emit(ev whatsapp.Event) { f.emit(ev) }

// Scan is the phone linking the device (after LinkQR or LinkPhone), then connecting.
func (f *Fake) Scan(a whatsapp.Account) {
	f.mu.Lock()
	f.linked, f.acct, f.linking = true, a, ""
	f.mu.Unlock()
	f.emit(whatsapp.EvLinked{Account: a})
	f.emit(whatsapp.EvConnected{Account: a})
}

func (f *Fake) Open(_ context.Context, h func(whatsapp.Event)) (bool, error) {
	f.mu.Lock()
	f.h = h
	linked := f.linked
	f.mu.Unlock()
	if linked {
		go f.emit(whatsapp.EvConnected{Account: f.Account()})
	}
	return linked, nil
}

func (f *Fake) Account() whatsapp.Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acct
}

func (f *Fake) LinkQR(context.Context) error {
	f.mu.Lock()
	f.linking = "qr"
	f.mu.Unlock()
	go f.emit(whatsapp.EvQR{Code: "2@fake-qr-code,abc,def", Expires: time.Now().Add(20 * time.Second)})
	return nil
}

func (f *Fake) LinkPhone(_ context.Context, phone string) error {
	f.mu.Lock()
	f.linking = "phone:" + phone
	f.mu.Unlock()
	go f.emit(whatsapp.EvPairingCode{Code: "ABCD-EFGH"})
	return nil
}

// Linking is how the fake was asked to link ("qr", "phone:+41…", or "").
func (f *Fake) Linking() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.linking
}

func (f *Fake) CancelLink() {
	f.mu.Lock()
	was := f.linking != ""
	f.linking = ""
	f.mu.Unlock()
	if was {
		go f.emit(whatsapp.EvLinkEnded{})
	}
}

func (f *Fake) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linked, f.acct, f.LoggedOut = false, whatsapp.Account{}, true
	return nil
}

func (f *Fake) NewMessageID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return fmt.Sprintf("3EB0FAKE%04d", f.n)
}

func (f *Fake) Upload(_ context.Context, file whatsapp.OutFile) (whatsapp.MediaRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	ref := whatsapp.MediaRef{Kind: file.Kind, DirectPath: fmt.Sprintf("/fake/up/%d", f.n), MediaKey: []byte("k"), Size: int64(len(file.Data))}
	f.Uploaded = append(f.Uploaded, file)
	f.Media[ref.DirectPath] = file.Data
	return ref, nil
}

func (f *Fake) Send(_ context.Context, m whatsapp.Outgoing) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SendErr != nil {
		return time.Time{}, f.SendErr
	}
	f.Sent = append(f.Sent, m)
	return time.Now(), nil
}

// SentNow returns a copy of what was sent so far.
func (f *Fake) SentNow() []whatsapp.Outgoing {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]whatsapp.Outgoing(nil), f.Sent...)
}

func (f *Fake) Download(_ context.Context, ref whatsapp.MediaRef, file *os.File) error {
	f.mu.Lock()
	data, ok := f.Media[ref.DirectPath]
	f.Downloads++
	f.mu.Unlock()
	if !ok {
		return errors.New("media download failed with status 404")
	}
	_, err := file.Write(data)
	return err
}

func (f *Fake) MarkRead(_ context.Context, chat, sender string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reads = append(f.Reads, append([]string{chat, sender}, ids...))
	return nil
}

func (f *Fake) ChatName(_ context.Context, jid string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Names[jid]
}

func (f *Fake) Close() {}

// Snapshot returns counters under the lock (Downloads, len(Reads)).
func (f *Fake) Counts() (downloads, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Downloads, len(f.Reads)
}
