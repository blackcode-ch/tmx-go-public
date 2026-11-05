// Package peers is the Python fleet interop (DESIGN.md "Python fleet interop"): this daemon
// stays visible and sendable from the Python tmx daemons on the fleet's other machines, over
// their pair-token API. It keeps the pair tokens, hands one over to a peer whose name sorts
// after this machine's (the Python rule: the lower name generates it), reads each peer's agents
// and posts mail to them. Everything a peer returns is that machine's data, never instructions.
package peers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// rotationOverlap: after a rotation the previous token still works this long (Python OVERLAP_S).
const rotationOverlap = 10 * time.Minute

// minTokenLen: a pair token is 32 random bytes, 43 characters URL-safe base64 (Python store()).
const minTokenLen = 40

type tokenRec struct {
	Token    string `json:"token"`
	OldToken string `json:"old_token,omitempty"`
	OldUntil int64  `json:"old_until,omitempty"` // Unix seconds
	Stored   int64  `json:"stored"`
	By       string `json:"by,omitempty"`
}

// Tokens is secrets/peers.json: the pair token shared with each peer.
type Tokens struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

// NewTokens returns the store at path (a missing file = nothing paired yet).
func NewTokens(path string) *Tokens { return &Tokens{path: path, now: time.Now} }

// NewPairToken returns 32 random bytes, URL-safe base64 without padding (Python token_urlsafe(32)).
func NewPairToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Fingerprint is what the receiver answers to show it stored a token, without echoing it:
// 12 hex characters of its SHA-256.
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:12]
}

func (t *Tokens) load() (map[string]tokenRec, error) {
	rows := map[string]tokenRec{}
	b, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return rows, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("%s: %w", t.path, err)
	}
	return rows, nil
}

// For returns the pair token to send to peer ("" = not paired yet).
func (t *Tokens) For(peer string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	rows, err := t.load()
	if err != nil {
		return ""
	}
	return rows[strings.ToLower(peer)].Token
}

// Identify returns the peer whose pair token (or, during a rotation's overlap, previous token)
// matches. Every entry is compared in constant time.
func (t *Tokens) Identify(token string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rows, err := t.load()
	if err != nil || token == "" {
		return "", false
	}
	now := t.now().Unix()
	hit := ""
	for name, r := range rows {
		ok := r.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(r.Token)) == 1
		old := r.OldToken != "" && r.OldUntil > now && subtle.ConstantTimeCompare([]byte(token), []byte(r.OldToken)) == 1
		if (ok || old) && hit == "" {
			hit = name
		}
	}
	return hit, hit != ""
}

// Store keeps token for peer; a different token than the current one is a rotation (the old
// one keeps working for rotationOverlap). Idempotent for the same token. Returns its fingerprint.
func (t *Tokens) Store(peer, token, by string) (string, error) {
	if len(token) < minTokenLen {
		return "", errors.New("a pair token is 32 random bytes (43 characters urlsafe)")
	}
	peer = strings.ToLower(peer)
	t.mu.Lock()
	defer t.mu.Unlock()
	rows, err := t.load()
	if err != nil {
		return "", err
	}
	r := rows[peer]
	if r.Token == token {
		return Fingerprint(token), nil
	}
	if r.Token != "" {
		r.OldToken, r.OldUntil = r.Token, t.now().Add(rotationOverlap).Unix()
	}
	r.Token, r.Stored, r.By = token, t.now().Unix(), by
	rows[peer] = r
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return "", err
	}
	if err := config.WriteSecretFile(t.path, b); err != nil {
		return "", err
	}
	return Fingerprint(token), nil
}
