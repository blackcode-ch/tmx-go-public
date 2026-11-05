// Package ownerpin is the owner PIN: one short code, the same on every machine of the fleet
// and the same record as the Python daemon's (src/tmx/gate.py, "the owner PIN"), so the owner
// has ONE code. It replaces passkeys for every owner step a browser takes.
//
// Record (<TMX_HOME>/secrets/owner-pin.json): {"salt": 32 hex (16 bytes), "hash": 64 hex,
// "set": unix seconds}. hash = scrypt(PIN as UTF-8, salt, N=16384, r=8, p=1, 32 bytes).
// The newest "set" wins between machines.
//
// Unknown callers share a persisted machine-wide backoff, unaffected by UI nonces.
// An authenticated paired device has its own persisted bucket: other callers never slow it.
// Each bucket waits after 5 wrong guesses, doubling from one minute to at most four hours.
// The right PIN clears only that bucket; the owner at the machine can clear all waits.
package ownerpin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
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

// Per-bucket backoff: GlobalFree wrong PINs in a row are free; from
// then on every try waits GlobalFirstWait, doubling with each further wrong PIN, up to
// GlobalMaxWait. A 6-digit PIN then takes centuries to guess.
const (
	GlobalFree      = 5
	GlobalFirstWait = time.Minute
	GlobalMaxWait   = 4 * time.Hour
)

// globalWait is the lockout after n wrong PINs in a row (0 below GlobalFree).
func globalWait(n int) time.Duration {
	if n < GlobalFree {
		return 0
	}
	d := GlobalFirstWait
	for i := GlobalFree; i < n && d < GlobalMaxWait; i++ {
		d *= 2
	}
	return min(d, GlobalMaxWait)
}

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
	// global is the unknown/unpaired callers count. fails holds paired-device buckets.
	// All buckets are persisted at triesPath.
	global    failure
	triesPath string
	// One check in flight per source, MaxChecks in all: parallel requests can't each get a
	// guess before the count rises (review of #146), nor exhaust memory with scrypt.
	inflight map[string]bool
}

type failure struct {
	n     int
	until time.Time
}

// triesFile stores the shared unknown-source count and independent paired-device counts.
type triesFile struct {
	Fails   int                  `json:"fails"`
	Until   int64                `json:"until"` // Unix ms; 0 = no wait
	Devices map[string]triesFile `json:"devices,omitempty"`
}

// TriesPath is where the machine-wide wrong-PIN count of the record at path is kept.
func TriesPath(path string) string {
	return filepath.Join(filepath.Dir(path), strings.TrimSuffix(filepath.Base(path), ".json")+"-tries.json")
}

// Open reads path (a missing file = no PIN yet) and the wrong-PIN count beside it.
func Open(path string) *Store {
	s := &Store{path: path, Now: time.Now, fails: map[string]*failure{}, inflight: map[string]bool{}, triesPath: TriesPath(path)}
	s.loadTries()
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

// bucketKey is an internal trust boundary: only the API may mark a source browser:,
// after authenticating an existing paired device. All other labels share one bucket.
func bucketKey(source string) string {
	if strings.HasPrefix(source, "browser:") {
		return source
	}
	return ""
}

func failureFromFile(t triesFile, now time.Time) failure {
	if t.Fails < 0 {
		return failure{n: GlobalFree, until: now.Add(GlobalFirstWait)}
	}
	f := failure{n: t.Fails}
	if t.Until > 0 {
		f.until = time.UnixMilli(t.Until)
		if limit := now.Add(GlobalMaxWait); f.until.After(limit) {
			f.until = limit
		}
	}
	return f
}

// Malformed state throttles unknown callers; it cannot lock out every paired device.
func (s *Store) loadTries() {
	now := time.Now()
	b, err := os.ReadFile(s.triesPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var state triesFile
	if err != nil || json.Unmarshal(b, &state) != nil {
		s.global = failure{n: GlobalFree, until: now.Add(GlobalFirstWait)}
		return
	}
	s.global = failureFromFile(state, now)
	for source, entry := range state.Devices {
		if bucketKey(source) == "" {
			continue
		}
		f := failureFromFile(entry, now)
		s.fails[source] = &f
	}
}

func fileFromFailure(f failure) triesFile {
	state := triesFile{Fails: f.n}
	if !f.until.IsZero() {
		state.Until = f.until.UnixMilli()
	}
	return state
}

// Caller holds mu. Failed writes retain the in-memory throttle until restart.
func (s *Store) saveTries() {
	state := fileFromFailure(s.global)
	if len(s.fails) > 0 {
		state.Devices = make(map[string]triesFile, len(s.fails))
	}
	for source, f := range s.fails {
		state.Devices[source] = fileFromFailure(*f)
	}
	b, _ := json.Marshal(state)
	_ = config.WriteSecretFile(s.triesPath, append(b, '\n'))
}

func (s *Store) bucket(source string) *failure {
	if source == "" {
		return &s.global
	}
	if s.fails[source] == nil {
		s.fails[source] = &failure{}
	}
	return s.fails[source]
}

// Wait reports only this caller's bucket. Paired devices never inherit unknown callers' wait.
func (s *Store) Wait(source string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	source = bucketKey(source)
	if source != "" && s.fails[source] == nil {
		return 0
	}
	return max(s.bucket(source).until.Sub(s.Now()), 0)
}

// Verify checks one bucket at a time, so parallel requests cannot outrun its counter.
// Paired devices use their authenticated identity; unpaired callers share the machine bucket.
func (s *Store) Verify(source, pin string) error {
	source = bucketKey(source)
	s.mu.Lock()
	rec, loadErr := s.rec, s.err
	f := s.bucket(source)
	now := s.Now()
	if now.Before(f.until) {
		wait := f.until.Sub(now)
		s.mu.Unlock()
		return &WaitError{wait}
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
	s.inflight[source] = true
	s.mu.Unlock()
	ok := rec.Check(pin)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, source)
	f = s.bucket(source)
	if ok {
		if source == "" {
			s.global = failure{}
		} else {
			delete(s.fails, source)
		}
		s.saveTries()
		return nil
	}
	if f.n < GlobalFree+32 {
		f.n++
	}
	if d := globalWait(f.n); d > 0 {
		f.until = s.Now().Add(d)
	}
	s.saveTries()
	return ErrWrong
}

// ClearBackoff is the owner's local recovery for every persisted bucket.
func (s *Store) ClearBackoff() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails = map[string]*failure{}
	s.global = failure{}
	s.saveTries()
}

// WaitError: too many wrong PINs from this source; try again after Wait.
type WaitError struct{ Wait time.Duration }

func (e *WaitError) Error() string {
	return "too many wrong PINs: wait " + e.Wait.Round(time.Second).String() + " (or clear it at the machine)"
}
