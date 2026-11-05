package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

// NewToken returns 32 random bytes as hex.
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// OwnerToken reads secrets/owner-token, creating it (private to the user) on first use.
func OwnerToken(p Paths) (string, error) {
	if t, err := ReadOwnerToken(p); err == nil {
		return t, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	t := NewToken()
	// Exclusive: if another process created it in the meantime, use theirs.
	f, err := CreateSecretFile(p.OwnerTokenFile())
	if errors.Is(err, os.ErrExist) {
		return ReadOwnerToken(p)
	}
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(t + "\n"); err != nil {
		f.Close()
		return "", err
	}
	return t, f.Close()
}

// ReadOwnerToken reads the owner token without creating it.
func ReadOwnerToken(p Paths) (string, error) {
	b, err := os.ReadFile(p.OwnerTokenFile())
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New("owner token file is empty: " + p.OwnerTokenFile())
	}
	return t, nil
}

// ReadSecret reads a one-line secret file ("" and os.ErrNotExist when it is missing).
func ReadSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// WriteSecret writes a one-line secret file (private to the user, atomically).
func WriteSecret(path, value string) error {
	return WriteSecretFile(path, []byte(strings.TrimSpace(value)+"\n"))
}

// CompanionToken reads secrets/companion-token, creating it on first use (create=true).
func CompanionToken(p Paths, create bool) (string, error) {
	t, err := ReadSecret(p.CompanionTokenFile())
	if err == nil && t != "" {
		return t, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if !create {
		return "", os.ErrNotExist
	}
	return RotateCompanionToken(p)
}

// RotateCompanionToken replaces the companion token; the old one stops working at once.
func RotateCompanionToken(p Paths) (string, error) {
	t := NewToken()
	return t, WriteSecret(p.CompanionTokenFile(), t)
}
