package lockfile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTryAcquireFailsWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "test.lock")
	held, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second TryAcquire: got %v, want ErrHeld", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("after Release: %v", err)
	}
	again.Release()
}

func TestAcquireCanBeCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	held, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := Acquire(ctx, path)
		got <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire ignored the cancelled context")
	}
}

func TestAcquireWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	held, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan *Lock)
	go func() {
		l, err := Acquire(context.Background(), path)
		if err != nil {
			t.Error(err)
		}
		got <- l
	}()

	select {
	case <-got:
		t.Fatal("Acquire returned while the lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	held.Release()
	select {
	case l := <-got:
		l.Release()
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire did not return after Release")
	}
}
