package push

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

type subscription struct {
	Subscription
	Device     string `json:"device"`
	Generation string `json:"generation"`
	Failures   int    `json:"failures"`
}

type state struct {
	Subscriptions map[string]subscription `json:"subscriptions"`
	Incidents     map[string]int64        `json:"incidents"`
}

type Service struct {
	mu     sync.Mutex
	dir    string
	key    *ecdh.PrivateKey
	state  state
	err    error
	http   *http.Client
	lookup lookupIPs
	active func(string) bool
	log    *slog.Logger
	now    func() time.Time
}

// Open reads only this Go daemon's secrets. Keys are created lazily on the phone's
// first enrollment. Corrupt state fails closed instead of silently replacing keys.
func Open(dir string, active func(string) bool, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{dir: dir, active: active, log: log, now: time.Now, lookup: net.DefaultResolver.LookupIPAddr,
		state: state{Subscriptions: map[string]subscription{}, Incidents: map[string]int64{}}}
	s.http = newHTTPClient(s.lookup)
	data, err := os.ReadFile(filepath.Join(dir, "push-subscriptions.json"))
	if err == nil {
		s.err = json.Unmarshal(data, &s.state)
	} else if !errors.Is(err, os.ErrNotExist) {
		s.err = err
	}
	if s.state.Subscriptions == nil {
		s.state.Subscriptions = map[string]subscription{}
	}
	if s.state.Incidents == nil {
		s.state.Incidents = map[string]int64{}
	}
	return s
}

func (s *Service) keyLocked() (*ecdh.PrivateKey, error) {
	if s.err != nil {
		return nil, errors.New("push storage unavailable")
	}
	if s.key != nil {
		return s.key, nil
	}
	path := filepath.Join(s.dir, "push-vapid.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// Never rotate a missing key underneath existing browser subscriptions.
		if len(s.state.Subscriptions) > 0 {
			return nil, errors.New("push signing key unavailable")
		}
		key, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		data, _ = json.Marshal(map[string]string{"private_key": b64.EncodeToString(key.Bytes())})
		if err := config.WriteSecretFile(path, data); err != nil {
			return nil, errors.New("push signing key unavailable")
		}
	} else if err != nil {
		return nil, errors.New("push signing key unavailable")
	}
	var record struct {
		PrivateKey string `json:"private_key"`
	}
	if json.Unmarshal(data, &record) != nil {
		return nil, errors.New("invalid push signing key")
	}
	raw, err := b64.DecodeString(record.PrivateKey)
	if err != nil {
		return nil, errors.New("invalid push signing key")
	}
	s.key, err = ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return nil, errors.New("invalid push signing key")
	}
	return s.key, nil
}

func (s *Service) PublicKey() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := s.keyLocked()
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(key.PublicKey().Bytes()), nil
}

func (s *Service) saveLocked() error {
	data, err := json.Marshal(s.state)
	if err == nil {
		err = config.WriteSecretFile(filepath.Join(s.dir, "push-subscriptions.json"), data)
	}
	if err != nil {
		s.err = err
		return errors.New("push storage unavailable")
	}
	return nil
}

func (s *Service) Subscribe(ctx context.Context, device string, in Subscription) error {
	if device == "" || s.active == nil || !s.active(device) {
		return errors.New("pair this device first")
	}
	u, err := endpointURL(in.Endpoint)
	if err != nil {
		return err
	}
	if _, err := publicAddresses(ctx, u.Hostname(), s.lookup); err != nil {
		return err
	}
	pub, e1 := b64.DecodeString(in.Keys.P256DH)
	auth, e2 := b64.DecodeString(in.Keys.Auth)
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil || e1 != nil || e2 != nil || len(auth) != 16 {
		return errors.New("invalid subscription keys")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.keyLocked(); err != nil {
		return err
	}
	count := 0
	for endpoint, old := range s.state.Subscriptions {
		if endpoint == in.Endpoint && old.Device != device {
			return errors.New("subscription belongs to another device")
		}
		if endpoint != in.Endpoint && old.Device == device {
			count++
		}
	}
	if count >= 4 || (len(s.state.Subscriptions) >= 64 && s.state.Subscriptions[in.Endpoint].Device == "") {
		return errors.New("too many push subscriptions")
	}
	s.state.Subscriptions[in.Endpoint] = subscription{Subscription: in, Device: device, Generation: config.NewToken()}
	return s.saveLocked()
}

func (s *Service) Enabled(device string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false
	}
	for _, sub := range s.state.Subscriptions {
		if sub.Device == device && sub.Failures < 3 {
			return true
		}
	}
	return false
}

func (s *Service) Unsubscribe(device string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return errors.New("push storage unavailable")
	}
	for endpoint, sub := range s.state.Subscriptions {
		if sub.Device == device {
			delete(s.state.Subscriptions, endpoint)
		}
	}
	return s.saveLocked()
}

// NotifyApproval sends once per incident to active paired devices. An accepted
// HTTP response proves push-service acceptance, never that the owner saw it.
func (s *Service) NotifyApproval(ctx context.Context, id string) error {
	hash := sha256.Sum256([]byte(id))
	incident := hex.EncodeToString(hash[:])
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return errors.New("push storage unavailable")
	}
	now := s.now()
	for key, until := range s.state.Incidents {
		if until <= now.Unix() {
			delete(s.state.Incidents, key)
		}
	}
	if s.state.Incidents[incident] > now.Unix() {
		s.mu.Unlock()
		return errors.New("push incident already attempted")
	}
	if len(s.state.Incidents) >= 4096 {
		s.mu.Unlock()
		return errors.New("push incident capacity reached")
	}
	subs := make([]subscription, 0, len(s.state.Subscriptions))
	for _, sub := range s.state.Subscriptions {
		if sub.Failures < 3 {
			subs = append(subs, sub)
		}
	}
	if len(subs) == 0 {
		s.mu.Unlock()
		s.log.Info("approval push unavailable", "approval_id", id, "reason", "no_subscriptions")
		return errors.New("no phone subscriptions")
	}
	key, err := s.keyLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.state.Incidents[incident] = now.Add(600 * time.Second).Unix()
	err = s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"title": "Approval needed", "category": "approvals", "incident": incident})
	sent, failed := 0, false
	for _, sub := range subs {
		if s.active == nil || !s.active(sub.Device) {
			continue
		}
		status := s.send(ctx, key, sub, payload, incident, now)
		ok := status >= 200 && status < 300
		s.log.Info("approval push delivery", "approval_id", id, "channel", "webpush", "ok", ok, "status", status)
		if ok {
			sent++
		} else {
			failed = true
		}
		s.mu.Lock()
		// A renewed subscription must not be removed by an old in-flight send.
		if current, exists := s.state.Subscriptions[sub.Endpoint]; exists && current == sub {
			if status == 404 || status == 410 {
				delete(s.state.Subscriptions, sub.Endpoint)
			} else {
				if ok {
					current.Failures = 0
				} else {
					current.Failures++
				}
				s.state.Subscriptions[sub.Endpoint] = current
			}
			if s.saveLocked() != nil {
				failed = true
			}
		}
		s.mu.Unlock()
	}
	s.log.Info("approval push summary", "approval_id", id, "accepted", sent, "failed", failed)
	if sent == 0 || failed {
		return errors.New("phone notification delivery incomplete")
	}
	return nil
}

func (s *Service) send(ctx context.Context, key *ecdh.PrivateKey, sub subscription, payload []byte, incident string, now time.Time) int {
	if _, err := endpointURL(sub.Endpoint); err != nil {
		return 0
	}
	pub, err := b64.DecodeString(sub.Keys.P256DH)
	if err != nil {
		return 0
	}
	auth, err := b64.DecodeString(sub.Keys.Auth)
	if err != nil {
		return 0
	}
	body, err := encrypt(payload, pub, auth)
	if err != nil {
		return 0
	}
	authorization, err := vapid(key, sub.Endpoint, now)
	if err != nil {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "600")
	req.Header.Set("Urgency", "high")
	req.Header.Set("Topic", incident[:32])
	res, err := s.http.Do(req)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	return res.StatusCode
}
