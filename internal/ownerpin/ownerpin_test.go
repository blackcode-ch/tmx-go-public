package ownerpin

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Vectors confirmed byte-for-byte against Python hashlib.scrypt by access-fix-o (3 Nov).
func TestVectorsMatchPython(t *testing.T) {
	for _, c := range []struct{ pin, salt, hash string }{
		{"123456", "000102030405060708090a0b0c0d0e0f", "53e59f1f9f56c2317d99f7a1e76fd1d43677ccb19c2c125e054f9a81281e0aec"},
		{"987654321012", "ffeeddccbbaa99887766554433221100", "b2f4376c5db628fa24d3e8a0e087a2e838653543d225e400a76b78608611502c"},
	} {
		s, _ := hex.DecodeString(c.salt)
		h, err := Hash(c.pin, s)
		if err != nil || hex.EncodeToString(h) != c.hash {
			t.Fatalf("%s: %x %v", c.pin, h, err)
		}
		if !(Record{Salt: c.salt, Hash: c.hash, Set: 1}).Check(c.pin) {
			t.Fatal("check")
		}
	}
}

func TestFormat(t *testing.T) {
	for pin, ok := range map[string]bool{"123456": true, "123456789012": true, "12345": false,
		"1234567890123": false, "12345a": false, "١٢٣٤٥٦": false, "": false} {
		if ValidPIN(pin) != ok {
			t.Errorf("%q", pin)
		}
	}
}

func TestStoreFileIsPythonShape(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secrets", "owner-pin.json")
	s := Open(p)
	if s.IsSet() || s.Err() != nil {
		t.Fatal("fresh")
	}
	if _, err := s.Set("246810"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || len(m) != 3 || len(m["salt"].(string)) != 32 || len(m["hash"].(string)) != 64 {
		t.Fatalf("%s", b)
	}
	if Open(p).Verify("x", "246810") != nil {
		t.Fatal("reopen")
	}
	// A Python-written record (int "set", an extra key) is read too.
	os.WriteFile(p, []byte(`{"salt":"000102030405060708090a0b0c0d0e0f","hash":"53e59f1f9f56c2317d99f7a1e76fd1d43677ccb19c2c125e054f9a81281e0aec","set":1762156800,"v":1}`), 0o600)
	if Open(p).Verify("x", "123456") != nil {
		t.Fatal("python record")
	}
	// A corrupt file is an error, never "no PIN" (that would let anyone set one).
	os.WriteFile(p, []byte(`{"salt":"zz"}`), 0o600)
	if s := Open(p); s.Err() == nil || s.IsSet() {
		t.Fatal("corrupt")
	}
}

func TestBackoffNeverPermanent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := Open(filepath.Join(t.TempDir(), "p.json"))
	s.Now = func() time.Time { return now }
	if err := s.Verify("a", "123456"); !errors.Is(err, ErrNotSet) {
		t.Fatal(err)
	}
	s.Set("123456")
	for i := 1; i <= 4; i++ {
		if err := s.Verify("a", "000000"); !errors.Is(err, ErrWrong) {
			t.Fatal(i, err)
		}
	}
	if s.Verify("a", "000000") != ErrWrong || s.Wait("a") != 60*time.Second {
		t.Fatal("5th")
	}
	var w *WaitError
	if err := s.Verify("a", "123456"); !errors.As(err, &w) { // even the right PIN waits
		t.Fatal(err)
	}
	if s.Verify("b", "123456") != nil { // another source is unaffected
		t.Fatal("b")
	}
	for i := 6; i <= 10; i++ {
		now = now.Add(61 * time.Second)
		s.Verify("a", "000000")
	}
	if s.Wait("a") != 900*time.Second {
		t.Fatal("10th", s.Wait("a"))
	}
	now = now.Add(901 * time.Second)
	if s.Verify("a", "123456") != nil || s.Wait("a") != 0 {
		t.Fatal("never permanent")
	}
	for i := 0; i < 12; i++ {
		now = now.Add(901 * time.Second)
		s.Verify("a", "000000")
	}
	s.ClearBackoff()
	if s.Verify("a", "123456") != nil {
		t.Fatal("clear")
	}
}

func TestAdoptNewestWins(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := Open(filepath.Join(t.TempDir(), "p.json"))
	s.Now = func() time.Time { return now }
	mine, _ := s.Set("111111")
	older, _ := NewRecord("222222", now.Add(-time.Hour))
	if ok, _ := s.Adopt(older); ok {
		t.Fatal("older adopted")
	}
	newer, _ := NewRecord("333333", now.Add(time.Minute))
	if ok, err := s.Adopt(newer); !ok || err != nil || s.Verify("x", "333333") != nil {
		t.Fatal("newer", err)
	}
	future, _ := NewRecord("444444", now.Add(301*time.Second))
	if _, err := s.Adopt(future); err == nil {
		t.Fatal("future")
	}
	if _, err := s.Adopt(Record{Salt: "00", Hash: mine.Hash, Set: float64(now.Add(2 * time.Minute).Unix())}); err == nil {
		t.Fatal("bad salt")
	}
}

// Review of #146: parallel guesses from one source get one check at a time, so the count
// rises before the next guess; and no more than MaxChecks scrypt runs at once overall.
func TestParallelGuessesAreCounted(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "p.json"))
	s.Set("123456")
	var wg sync.WaitGroup
	var checked, busy atomic.Int32
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := s.Verify("pair:host", "000000"); {
			case errors.Is(err, ErrWrong):
				checked.Add(1)
			case errors.Is(err, ErrBusy):
				busy.Add(1)
			}
		}()
	}
	wg.Wait()
	// Each check that ran was counted before the next could start: after 5 the source waits.
	if checked.Load() > 5 {
		t.Fatalf("%d guesses checked in parallel (busy %d)", checked.Load(), busy.Load())
	}
	if s.Wait("pair:host") == 0 && checked.Load() == 5 {
		t.Fatal("5 wrong guesses but no wait")
	}
	// Many sources at once: never more than MaxChecks in flight.
	var peak atomic.Int32
	s2 := Open(filepath.Join(t.TempDir(), "q.json"))
	s2.Set("123456")
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s2.mu.Lock()
			n := int32(len(s2.inflight))
			s2.mu.Unlock()
			if n > peak.Load() {
				peak.Store(n)
			}
			s2.Verify(fmt.Sprint("src", i), "000000")
		}(i)
	}
	wg.Wait()
	if peak.Load() > MaxChecks {
		t.Fatal("peak", peak.Load())
	}
}
