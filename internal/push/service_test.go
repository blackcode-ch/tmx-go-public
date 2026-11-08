package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"golang.org/x/crypto/hkdf"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testSubscription(t *testing.T) (Subscription, *ecdh.PrivateKey) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{Endpoint: "https://fcm.googleapis.com/send/private-subscription"}
	sub.Keys.P256DH = b64.EncodeToString(key.PublicKey().Bytes())
	sub.Keys.Auth = b64.EncodeToString([]byte("test-auth-secret"))
	return sub, key
}

func publicLookup(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
}

func TestDeliveryPersistenceDedupeAndRemoval(t *testing.T) {
	for _, status := range []int{201, 404, 410, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var logs bytes.Buffer
			dir := t.TempDir()
			active := true
			s := Open(dir, func(string) bool { return active }, slog.New(slog.NewTextHandler(&logs, nil)))
			s.lookup = publicLookup
			sub, receiver := testSubscription(t)
			if err := s.Subscribe(context.Background(), "phone", sub); err != nil {
				t.Fatal(err)
			}
			key, err := s.PublicKey()
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.http = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("TTL") != "600" || r.Header.Get("Urgency") != "high" || r.Header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
					t.Error("wrong Web Push headers")
				}
				body, _ := io.ReadAll(r.Body)
				plain := decryptPush(t, receiver, []byte("test-auth-secret"), body)
				var payload map[string]string
				if json.Unmarshal(plain, &payload) != nil || len(payload) != 3 || payload["title"] != "Approval needed" || payload["category"] != "approvals" || len(payload["incident"]) != 64 {
					t.Error("private or malformed push payload")
				}
				if len(body) < 100 || bytes.Contains(body, []byte("Approval needed")) {
					t.Error("payload was not encrypted")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			err = s.NotifyApproval(context.Background(), "approval-1")
			if (err == nil) != (status == 201) || calls != 1 {
				t.Fatalf("delivery: calls=%d err=%v", calls, err)
			}
			_ = s.NotifyApproval(context.Background(), "approval-1")
			if calls != 1 {
				t.Fatal("duplicate incident sent")
			}
			reopened := Open(dir, func(string) bool { return true }, nil)
			reopened.http = s.http
			if next, err := reopened.PublicKey(); err != nil || next != key {
				t.Fatal("VAPID key did not persist")
			}
			_ = reopened.NotifyApproval(context.Background(), "approval-1")
			if calls != 1 {
				t.Fatal("restart repeated incident")
			}
			if (status == 404 || status == 410) && reopened.Enabled("phone") {
				t.Fatal("gone subscription retained")
			}
			active = false
			_ = s.NotifyApproval(context.Background(), "approval-2")
			if calls != 1 {
				t.Fatal("revoked device notified")
			}
			for _, secret := range []string{sub.Endpoint, sub.Keys.P256DH, sub.Keys.Auth, "vapid t="} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("secret logged")
				}
			}
		})
	}
}

func TestSubscriptionIsolationAndValidation(t *testing.T) {
	s := Open(t.TempDir(), func(string) bool { return true }, nil)
	s.lookup = publicLookup
	sub, _ := testSubscription(t)
	if err := s.Subscribe(context.Background(), "one", sub); err != nil {
		t.Fatal(err)
	}
	if err := s.Subscribe(context.Background(), "two", sub); err == nil {
		t.Fatal("stole another phone's subscription")
	}
	if err := s.Unsubscribe("two"); err != nil || !s.Enabled("one") {
		t.Fatal("another phone removed subscription")
	}
	sub.Keys.Auth = "invalid"
	if err := s.Subscribe(context.Background(), "one", sub); err == nil {
		t.Fatal("bad auth accepted")
	}

}

func TestOldDeliveryCannotRemoveRenewedSubscription(t *testing.T) {
	s := Open(t.TempDir(), func(string) bool { return true }, nil)
	s.lookup = publicLookup
	sub, _ := testSubscription(t)
	if err := s.Subscribe(context.Background(), "phone", sub); err != nil {
		t.Fatal(err)
	}
	s.http = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		// The phone renews while the original request is in flight.
		if err := s.Subscribe(context.Background(), "phone", sub); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 410, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	_ = s.NotifyApproval(context.Background(), "incident")
	if !s.Enabled("phone") {
		t.Fatal("old delivery removed renewed subscription")
	}
}

func TestRepeatedFailuresDisableUntilPhoneReenrolls(t *testing.T) {
	s := Open(t.TempDir(), func(string) bool { return true }, nil)
	s.lookup = publicLookup
	sub, _ := testSubscription(t)
	if err := s.Subscribe(context.Background(), "phone", sub); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.http = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	for _, id := range []string{"one", "two", "three", "four"} {
		_ = s.NotifyApproval(context.Background(), id)
	}
	if calls != 3 || s.Enabled("phone") {
		t.Fatal("failed subscription not disabled")
	}
	if err := s.Subscribe(context.Background(), "phone", sub); err != nil || !s.Enabled("phone") {
		t.Fatal("phone cannot reenable")
	}
}

func TestEndpointAndPublicAddressPolicy(t *testing.T) {
	for _, endpoint := range []string{"http://fcm.googleapis.com/a", "https://fcm.googleapis.com:444/a", "https://fcm.googleapis.com.evil.test/a", "https://user:pass@fcm.googleapis.com/a", "https://127.0.0.1/a", "https://evilpush.apple.com/a", "https://fcm.googleapis.com/a#fragment"} {
		if _, err := endpointURL(endpoint); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "169.254.169.254", "192.0.2.1", "198.18.0.1", "240.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "64:ff9b::7f00:1", "2002:7f00:1::"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("non-public IP accepted: %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(ip)) {
			t.Errorf("public IP refused: %s", ip)
		}
	}
	mixed := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if _, err := publicAddresses(context.Background(), "fcm.googleapis.com", mixed); err == nil {
		t.Fatal("mixed public/private DNS accepted")
	}
	client := newHTTPClient(mixed)
	if _, err := client.Get("https://fcm.googleapis.com/send/test"); err == nil {
		t.Fatal("dial bypassed public-address check")
	}
	if client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirect followed")
	}
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("proxy can bypass address validation")
	}
}

func decryptPush(t *testing.T, receiver *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	if len(body) < 103 {
		t.Fatal("short record")
	}
	pub, err := ecdh.P256().NewPublicKey(body[21:86])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := receiver.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	derive := func(secret, salt, info []byte, n int) []byte {
		out := make([]byte, n)
		if _, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, info), out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	info := append(append([]byte("WebPush: info\x00"), receiver.PublicKey().Bytes()...), pub.Bytes()...)
	ikm := derive(shared, auth, info, 32)
	cek := derive(ikm, body[:16], []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := derive(ikm, body[:16], []byte("Content-Encoding: nonce\x00"), 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[86:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatal("bad final record delimiter")
	}
	return plain[:len(plain)-1]
}
