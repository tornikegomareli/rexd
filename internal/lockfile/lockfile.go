// Package lockfile provides advisory file locks. The kernel releases a lock when
// its process exits, so a crash never leaves a stale lock behind.
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("lock is held by another process")

// Lock is a held lock.
type Lock struct{ f *os.File }

// Acquire waits until it holds the lock at path.
func Acquire(path string) (*Lock, error) {
	return acquire(path, syscall.LOCK_EX)
}

// TryAcquire takes the lock at path, or returns ErrHeld without waiting.
func TryAcquire(path string) (*Lock, error) {
	return acquire(path, syscall.LOCK_EX|syscall.LOCK_NB)
}

func acquire(path string, how int) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return nil, ErrHeld
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return &Lock{f}, nil
}

// Release gives up the lock.
func (l *Lock) Release() error {
	return l.f.Close()
}
