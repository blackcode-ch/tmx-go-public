package people

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The same rules as Python's team.py, so imported data keeps validating.
var (
	keyRx   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)
	phoneRx = regexp.MustCompile(`^\+?[0-9 ]{6,20}$`)
	dueRx   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	badMail = regexp.MustCompile(`[\s?&%#\x00-\x1f\x7f]`)
)

// Kinds a person can be; "client" is never offered Tiamatrix connect (Python bug #32).
var Kinds = []string{"team", "external", "client"}

// Promise states.
const (
	Open    = "open"
	Done    = "done"
	Dropped = "dropped"
)

func ValidKey(k string) error {
	if !keyRx.MatchString(k) {
		return errors.New("key must match [a-z0-9][a-z0-9_.-]{0,39}")
	}
	return nil
}

func NormKind(k string) (string, error) {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return "team", nil
	}
	for _, v := range Kinds {
		if k == v {
			return k, nil
		}
	}
	return "", fmt.Errorf("kind must be one of %s", strings.Join(Kinds, ", "))
}

// Phone keeps the digits only.
func Phone(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !phoneRx.MatchString(raw) {
		return "", errors.New("phone must look like +41 79 123 45 67")
	}
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}

func Email(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Count(raw, "@") != 1 || len(raw) < 3 || len(raw) > 254 || strings.HasPrefix(raw, "@") || strings.HasSuffix(raw, "@") {
		return "", errors.New("not a valid email")
	}
	if badMail.MatchString(raw) {
		return "", errors.New("email must not contain spaces, ?, &, %, # or control characters")
	}
	return raw, nil
}

// Due is an ISO date ("" stays "").
func Due(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !dueRx.MatchString(raw) {
		return "", errors.New("due must be an ISO date, like 2026-10-15")
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return "", errors.New("due is not a real date")
	}
	return raw, nil
}

// DueFireAt is when a promise's reminder fires: 09:00 local time on the due date.
func DueFireAt(due string, loc *time.Location) (time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", due, loc)
	if err != nil {
		return time.Time{}, err
	}
	return d.Add(9 * time.Hour), nil
}

func ValidStatus(s string) bool { return s == Open || s == Done || s == Dropped }
