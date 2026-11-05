package companion

import (
	"crypto/subtle"
	"os"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

// The link token (Companion → tmx) is read from secrets/companion-token when it changes on
// disk, so `tmx companion install` / `token --rotate` / `uninstall` (CLI, without the daemon)
// take effect at once.
type linkToken struct {
	mu    sync.Mutex
	mod   time.Time
	size  int64
	value string
	used  int64 // last successful use (Unix ms)
}

// LinkTokenMatches reports whether tok is the current link token.
func (s *Service) LinkTokenMatches(tok string) bool {
	if tok == "" {
		return false
	}
	s.link.mu.Lock()
	defer s.link.mu.Unlock()
	path := s.o.Paths.CompanionTokenFile()
	fi, err := os.Stat(path)
	if err != nil {
		s.link.value, s.link.mod, s.link.size = "", time.Time{}, 0
		return false
	}
	if !fi.ModTime().Equal(s.link.mod) || fi.Size() != s.link.size {
		v, err := config.ReadSecret(path)
		if err != nil {
			return false
		}
		s.link.value, s.link.mod, s.link.size = v, fi.ModTime(), fi.Size()
	}
	if s.link.value == "" || len(s.link.value) < 32 {
		return false
	}
	ok := subtle.ConstantTimeCompare([]byte(tok), []byte(s.link.value)) == 1
	if ok {
		s.link.used = time.Now().UnixMilli()
	}
	return ok
}

// LinkLastUsed is when Companion last used the link (Unix ms; 0 = not since the daemon started).
func (s *Service) LinkLastUsed() int64 {
	s.link.mu.Lock()
	defer s.link.mu.Unlock()
	return s.link.used
}

// RotateLinkToken makes a new link token (the old one stops working at once).
func (s *Service) RotateLinkToken() error {
	_, err := config.RotateCompanionToken(s.o.Paths)
	return err
}

// Install writes tmx's entry and skill into Companion (creating the link token if needed).
func (s *Service) Install() (InstallStatus, error) {
	if _, err := config.CompanionToken(s.o.Paths, true); err != nil {
		return InstallStatus{}, err
	}
	return Install(s.o.Install)
}

// Uninstall removes tmx's entry and skill from Companion and revokes the link token.
func (s *Service) Uninstall() (InstallStatus, error) {
	st, err := Uninstall(s.o.Install)
	if err != nil {
		return st, err
	}
	if err := os.Remove(s.o.Paths.CompanionTokenFile()); err != nil && !os.IsNotExist(err) {
		return st, err
	}
	return st, nil
}

// Paths is the state folder the service works in.
func (s *Service) Paths() config.Paths { return s.o.Paths }
