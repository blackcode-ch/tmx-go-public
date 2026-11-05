package recorder

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Playback tickets. A <video src> cannot send the owner token (it never goes in a URL), so
// the owner's window asks for a ticket: an HMAC of the recording's id and an expiry, signed
// with a key that lives only in this daemon's memory. A ticket plays only that recording,
// only until it expires; the window asks for a new one when playback fails.

// TicketTTL is how long a ticket plays.
const TicketTTL = 10 * time.Minute

// Ticket returns a ticket for recording id and when it expires.
func (s *Service) Ticket(id string) (string, time.Time, error) {
	if _, err := s.Get(id); err != nil {
		return "", time.Time{}, err
	}
	exp := s.o.Now().Add(TicketTTL).Truncate(time.Second)
	e := strconv.FormatInt(exp.Unix(), 36)
	return e + "." + s.sign(id, e), exp, nil
}

// CheckTicket reports whether t is a valid, unexpired ticket for recording id.
func (s *Service) CheckTicket(id, t string) bool {
	e, sig, ok := strings.Cut(t, ".")
	if !ok || e == "" || sig == "" {
		return false
	}
	unix, err := strconv.ParseInt(e, 36, 64)
	if err != nil || !s.o.Now().Before(time.Unix(unix, 0)) {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.sign(id, e)))
}

func (s *Service) sign(id, exp string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("tmx-recording-ticket\x00" + id + "\x00" + exp))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}
