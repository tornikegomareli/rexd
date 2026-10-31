// Package lockfile provides advisory file locks. The kernel releases a lock when
// its process exits, so a crash never leaves a stale lock behind.
package lockfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const retryDelay = 50 * time.Millisecond

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("lock is held by another process")

// Lock is a held lock.
type Lock struct{ f *os.File }

// Acquire waits until it holds the lock at path, or until ctx ends. It checks
// again and again, because a blocking flock cannot be interrupted, and the person
// waiting must be able to press Ctrl-C.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	for {
		lock, err := TryAcquire(path)
		if !errors.Is(err, ErrHeld) {
			return lock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryDelay):
		}
	}
}

// TryAcquire takes the lock at path alone, or returns ErrHeld without waiting.
func TryAcquire(path string) (*Lock, error) {
	return tryLock(path, syscall.LOCK_EX)
}

// TryAcquireShared takes the lock at path beside other shared holders, or returns
// ErrHeld without waiting if someone holds it alone. It lets a process ask whether
// a lock is held without looking like a holder to others that ask.
func TryAcquireShared(path string) (*Lock, error) {
	return tryLock(path, syscall.LOCK_SH)
}

func tryLock(path string, how int) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
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
