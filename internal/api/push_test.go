package api

import (
	"context"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/push"
)

type fakePhonePush struct {
	enrolled string
	calls    int
}

func (p *fakePhonePush) PublicKey() (string, error) { return "public-key", nil }
func (p *fakePhonePush) Enabled(dev string) bool    { return p.enrolled == dev }
func (p *fakePhonePush) Subscribe(_ context.Context, dev string, _ push.Subscription) error {
	p.enrolled = dev
	p.calls++
	return nil
}
func (p *fakePhonePush) Unsubscribe(dev string) error {
	if dev == p.enrolled {
		p.enrolled = ""
	}
	return nil
}
func (p *fakePhonePush) NotifyApproval(context.Context, string) error { return nil }

func TestPushEnrollmentRequiresPairedPhoneAndOwnerPIN(t *testing.T) {
	f := newDeviceFixture(t)
	p := &fakePhonePush{}
	f.s.push = p
	cookie := f.paired(t)
	other := f.paired(t)
	f.setPIN(t, testPIN)
	body := map[string]any{"subscription": map[string]any{"endpoint": "https://fcm.googleapis.com/send/fixture"}}
	deviceResponse(t, f.request(t, "GET", "/api/push/key", deviceTestHost, nil, nil, false), 401)
	deviceResponse(t, f.request(t, "GET", "/api/push/key", "localhost", nil, nil, true), 403)
	deviceResponse(t, f.request(t, "POST", "/api/push/subscribe", "localhost", body, nil, true), 403)
	deviceResponse(t, f.request(t, "GET", "/api/push/key", deviceTestHost, nil, cookie, false), 200)
	confirmation := deviceResponse(t, f.request(t, "POST", "/api/push/subscribe", deviceTestHost, body, cookie, false), 428)
	if p.calls != 0 {
		t.Fatal("subscription stored before owner proof")
	}
	id, method := f.confirmation(t, confirmation)
	if method != "pin" {
		t.Fatal("changed owner proof flow")
	}
	deviceResponse(t, f.request(t, "POST", "/api/gate/browser/"+id, deviceTestHost, map[string]string{"pin": testPIN}, other, false), 403)
	deviceResponse(t, f.request(t, "POST", "/api/gate/browser/"+id, deviceTestHost, map[string]string{"pin": testPIN}, cookie, false), 200)
	deviceResponse(t, f.request(t, "POST", "/api/push/subscribe?confirmation="+id, deviceTestHost, body, cookie, false), 201)
	if p.calls != 1 || p.enrolled == "" || !f.s.pushDeviceActive(p.enrolled) {
		t.Fatal("phone not enrolled")
	}
	deviceResponse(t, f.request(t, "POST", "/api/push/unsubscribe", deviceTestHost, nil, other, false), 200)
	if p.enrolled == "" {
		t.Fatal("another phone disabled notifications")
	}
	deviceResponse(t, f.request(t, "POST", "/api/push/unsubscribe", deviceTestHost, nil, cookie, false), 200)
	if p.enrolled != "" {
		t.Fatal("phone could not disable notifications")
	}
	f.now = f.now.Add(91 * 24 * time.Hour)
	deviceResponse(t, f.request(t, "GET", "/api/push/key", deviceTestHost, nil, cookie, false), 401)
}
