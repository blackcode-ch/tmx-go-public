package config

import (
	"errors"
	"os"
	"time"
)

// ErrLocked means another daemon holds the lock for this state directory.
var ErrLocked = errors.New("another tmx daemon is already running for this state directory")

// Lock is the daemon's single-instance lock (an OS file lock on daemon.lock, released by
// the OS when the process dies, so a crash never leaves a stale lock).
type Lock struct{ f *os.File }

// AcquireLock takes the lock without waiting; ErrLocked if another process holds it.
func AcquireLock(p Paths) (*Lock, error) {
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p.LockFile(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// WaitLock is AcquireLock retried until timeout: a daemon that is still shutting down (it stops
// its agents after it stops answering HTTP) holds the lock for a few more seconds.
func WaitLock(p Paths, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	for {
		l, err := AcquireLock(p)
		if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			return l, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockFile(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}
