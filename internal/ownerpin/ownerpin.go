// Package ownerpin is the owner PIN: one short code, the same on every machine of the fleet
// and the same record as the Python daemon's (src/tmx/gate.py, "the owner PIN"), so the owner
// has ONE code. It replaces passkeys for every owner step a browser takes.
//
// Record (<TMX_HOME>/secrets/owner-pin.json): {"salt": 32 hex (16 bytes), "hash": 64 hex,
// "set": unix seconds}. hash = scrypt(PIN as UTF-8, salt, N=16384, r=8, p=1, 32 bytes).
// The newest "set" wins between machines. Wrong PINs slow a source down, never lock it out.
package ownerpin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

const (
	scryptN, scryptR, scryptP, keyLen = 16384, 8, 1, 32
	MinLen, MaxLen                    = 6, 12
	// FutureSkew: a synced record set further ahead than this is refused (Python: 300 s).
	FutureSkew = 300 * time.Second
	// Session and approval limits, the same as Python's.
	SessionIdle = time.Hour
	SessionMax  = 12 * time.Hour
	Fresh       = 5 * time.Minute
)

var (
	ErrFormat  = errors.New("the PIN is 6 to 12 digits")
	ErrWrong   = errors.New("wrong PIN")
	ErrNotSet  = errors.New("no owner PIN is set yet: set it at the machine")
	ErrInvalid = errors.New("invalid PIN record")
	// ErrBusy: a check from this source is already running, or MaxChecks are: try again in a
	// second. Never a guess: the PIN was not checked.
	ErrBusy = errors.New("a PIN check is already running: try again in a second")
)

// MaxChecks bounds concurrent scrypt checks (about 16 MB each) across all sources.
const MaxChecks = 4

// Record is the stored PIN, field for field the Python record.
type Record struct {
	Salt string  `json:"salt"`
	Hash string  `json:"hash"`
	Set  float64 `json:"set"`
}

// SetTime is when the PIN was set.
func (r Record) SetTime() time.Time {
	sec, frac := math.Modf(r.Set)
	return time.Unix(int64(sec), int64(frac*1e9))
}

// Valid checks the shape (salt 32 hex, hash 64 hex, set not too far ahead of now).
func (r Record) Valid(now time.Time) bool {
	s, err1 := hex.DecodeString(r.Salt)
	h, err2 := hex.DecodeString(r.Hash)
	return err1 == nil && err2 == nil && len(s) == 16 && len(h) == keyLen && r.Set > 0 &&
		!math.IsInf(r.Set, 0) && !math.IsNaN(r.Set) && !r.SetTime().After(now.Add(FutureSkew))
}

// ValidPIN: digits only, 6 to 12.
func ValidPIN(pin string) bool {
	if len(pin) < MinLen || len(pin) > MaxLen {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Hash derives the stored hash of pin with salt (raw bytes).
func Hash(pin string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(pin), salt, scryptN, scryptR, scryptP, keyLen)
}

// NewRecord hashes pin with a fresh random salt.
func NewRecord(pin string, now time.Time) (Record, error) {
	if !ValidPIN(pin) {
		return Record{}, ErrFormat
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Record{}, err
	}
	h, err := Hash(pin, salt)
	if err != nil {
		return Record{}, err
	}
	return Record{Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(h), Set: float64(now.UnixNano()) / 1e9}, nil
}

// Check compares pin against r in constant time.
func (r Record) Check(pin string) bool {
	if !ValidPIN(pin) {
		return false
	}
	salt, err := hex.DecodeString(r.Salt)
	if err != nil {
		return false
	}
	h, err := Hash(pin, salt)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(h)), []byte(r.Hash)) == 1
}

// Store is the record on disk plus the in-memory backoff. Safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	path  string
	rec   *Record
	err   error
	Now   func() time.Time
	fails map[string]*failure
	// One check in flight per source, MaxChecks in all: parallel requests can't each get a
	// guess before the count rises (review of #146), nor exhaust memory with scrypt.
	inflight map[string]bool
}

type failure struct {
	n     int
	until time.Time
}

// Open reads path (a missing file = no PIN yet).
func Open(path string) *Store {
	s := &Store{path: path, Now: time.Now, fails: map[string]*failure{}, inflight: map[string]bool{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		s.err = err
	default:
		var r Record
		if json.Unmarshal(b, &r) != nil || !r.Valid(time.Now().Add(100*365*24*time.Hour)) {
			s.err = ErrInvalid // never treated as "no PIN": that would let anyone set one
		} else {
			s.rec = &r
		}
	}
	return s
}

// Err is a load error (the PIN can't be used or set until it is fixed at the machine).
func (s *Store) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

// IsSet tells whether a PIN exists.
func (s *Store) IsSet() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.rec != nil }

// Record returns a copy of the stored record, or nil.
func (s *Store) Record() *Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		return nil
	}
	r := *s.rec
	return &r
}

// caller holds mu
func (s *Store) write(r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := config.WriteSecretFile(s.path, append(b, '\n')); err != nil {
		return err
	}
	s.rec, s.err = &r, nil
	return nil
}

// Set stores a new PIN. The caller decides who may (first time: the owner at the machine;
// a change: the old PIN or the owner at the machine). Returns the new record to push.
func (s *Store) Set(pin string) (Record, error) {
	r, err := NewRecord(pin, s.Now())
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return r, s.write(r)
}

// Adopt takes a peer's record if it is well-formed and newer than ours.
func (s *Store) Adopt(r Record) (bool, error) {
	if !r.Valid(s.Now()) {
		return false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec != nil && r.Set <= s.rec.Set {
		return false, nil
	}
	return true, s.write(Record{Salt: r.Salt, Hash: r.Hash, Set: r.Set})
}

// Wait is how long source must wait before its next try (0 = now).
func (s *Store) Wait(source string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.fails[source]; f != nil {
		if d := f.until.Sub(s.Now()); d > 0 {
			return d
		}
	}
	return 0
}

// Verify checks pin for source with the backoff: wrong #5..#9 wait 60 s, #10 on 900 s each,
// never permanent. A right PIN clears the source. A waiting source gets a *WaitError and its
// PIN is not checked at all.
func (s *Store) Verify(source, pin string) error {
	s.mu.Lock()
	rec, loadErr := s.rec, s.err
	f := s.fails[source]
	now := s.Now()
	if f != nil && now.Before(f.until) {
		s.mu.Unlock()
		return &WaitError{f.until.Sub(now)}
	}
	if loadErr != nil || rec == nil {
		s.mu.Unlock()
		if loadErr != nil {
			return loadErr
		}
		return ErrNotSet
	}
	if s.inflight[source] || len(s.inflight) >= MaxChecks {
		s.mu.Unlock()
		return ErrBusy
	}
	s.inflight[source] = true // reserved under the lock: the count below is seen by the next try
	s.mu.Unlock()
	ok := rec.Check(pin) // slow: outside the lock
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, source)
	if ok {
		delete(s.fails, source)
		return nil
	}
	if s.fails[source] == nil {
		if len(s.fails) >= 10000 { // bound memory: drop the expired ones
			for k, v := range s.fails {
				if !now.Before(v.until) && v.n < 5 {
					delete(s.fails, k)
				}
			}
		}
		s.fails[source] = &failure{}
	}
	f = s.fails[source]
	f.n++
	switch {
	case f.n >= 10:
		f.until = s.Now().Add(900 * time.Second)
	case f.n >= 5:
		f.until = s.Now().Add(60 * time.Second)
	}
	return ErrWrong
}

// ClearBackoff forgets every source's wrong tries (the owner at the machine).
func (s *Store) ClearBackoff() { s.mu.Lock(); s.fails = map[string]*failure{}; s.mu.Unlock() }

// WaitError: too many wrong PINs from this source; try again after Wait.
type WaitError struct{ Wait time.Duration }

func (e *WaitError) Error() string {
	return "too many wrong PINs: wait " + e.Wait.Round(time.Second).String() + " (or clear it at the machine)"
}
