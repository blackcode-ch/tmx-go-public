package fleet

import (
	"errors"
	"fmt"
)

// Kind classifies fleet errors so the API can map them to HTTP statuses.
type Kind int

const (
	KindInternal Kind = iota
	KindInvalid
	KindNotFound
	KindForbidden
	KindConflict
	KindTooMany
)

// Error is a fleet error with a plain-sentence message meant for the caller.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(k Kind, format string, args ...any) error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, args...)}
}

// KindOf returns the Kind of err (KindInternal for foreign errors).
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindInternal
}
