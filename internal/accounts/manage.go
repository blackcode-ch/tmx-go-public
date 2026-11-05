package accounts

import (
	"errors"
	"slices"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ErrSigningIn: the account's sign-in is running.
var ErrSigningIn = errors.New("a sign-in is running for this account: cancel it first")

// ErrNoProfiles: this service was started without an account list.
var ErrNoProfiles = errors.New("named accounts are not available here")

func (s *Service) launchDefault(h model.Harness) string {
	if s.o.Profiles == nil {
		return DefaultName
	}
	return s.o.Profiles.Default(h)
}

// AddAccount adds the account name to harness h (signed out until the owner signs in) and
// checks it.
func (s *Service) AddAccount(h, name string) error {
	if s.o.Profiles == nil {
		return ErrNoProfiles
	}
	d := defFor(model.Harness(h))
	if d == nil {
		return ErrUnknown
	}
	if err := s.o.Profiles.Add(d.h, name); err != nil {
		return err
	}
	e := s.newEntry(d, name)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("tmx is stopping")
	}
	// After h's other accounts, so the list keeps the harnesses' order.
	at := len(s.entries)
	for i, x := range s.entries {
		if x.def.h == d.h {
			at = i + 1
		}
	}
	s.entries = slices.Insert(slices.Clone(s.entries), at, e)
	s.changedLocked()
	s.mu.Unlock()
	s.publish()
	s.refresh(e)
	return nil
}

// RemoveAccount takes the account name off harness h's list. Its home, with whatever sign-in
// it holds, stays on disk: sign out first to end the sign-in.
func (s *Service) RemoveAccount(h, name string) error {
	if s.o.Profiles == nil {
		return ErrNoProfiles
	}
	if name == "" || name == DefaultName {
		return ErrIsDefault
	}
	e := s.entry(h, name)
	if e == nil {
		return ErrNoAccount
	}
	e.loginMu.Lock()
	defer e.loginMu.Unlock()
	s.mu.Lock()
	if e.sess != nil && !e.sess.ended() {
		s.mu.Unlock()
		return ErrSigningIn
	}
	s.mu.Unlock()
	if err := s.o.Profiles.Remove(e.def.h, name); err != nil {
		return err
	}
	s.mu.Lock()
	s.entries = slices.DeleteFunc(slices.Clone(s.entries), func(x *entry) bool { return x == e })
	s.changedLocked()
	s.mu.Unlock()
	s.publish()
	return nil
}

// SetLaunchDefault makes name the account of h's agents launched without one.
func (s *Service) SetLaunchDefault(h, name string) error {
	if s.o.Profiles == nil {
		return ErrNoProfiles
	}
	if !Known(h) {
		return ErrUnknown
	}
	if err := s.o.Profiles.SetDefault(model.Harness(h), name); err != nil {
		return err
	}
	s.mu.Lock()
	s.changedLocked()
	s.mu.Unlock()
	s.publish()
	return nil
}

func defFor(h model.Harness) *def {
	for _, d := range defs() {
		if d.h == h {
			return d
		}
	}
	return nil
}
