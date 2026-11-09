package storepool

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func workspace(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	return dir
}

func TestAcquireRefcountsAndReopens(t *testing.T) {
	pool := New()
	defer pool.CloseAll()
	ws := workspace(t, "agent")

	first, releaseFirst, err := pool.Acquire(ws)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	second, releaseSecond, err := pool.Acquire(ws)
	if err != nil {
		t.Fatalf("Acquire(second) error: %v", err)
	}
	if first != second {
		t.Fatal("Acquire returned different stores for the same workspace")
	}
	releaseFirst()
	if pool.Len() != 1 {
		t.Fatalf("Len() = %d after one release, want 1", pool.Len())
	}
	retain, ok := pool.Retain(ws)
	if !ok {
		t.Fatal("Retain() = false, want true")
	}
	releaseSecond()
	releaseRetain := retain
	releaseRetain()
	if pool.Len() != 0 {
		t.Fatalf("Len() = %d after all releases, want 0", pool.Len())
	}
	if _, ok := pool.Retain(ws); ok {
		t.Fatal("Retain(closed) = true, want false")
	}

	reopened, release, err := pool.Acquire(ws)
	if err != nil {
		t.Fatalf("Acquire(reopen) error: %v", err)
	}
	if reopened == nil {
		t.Fatal("Acquire(reopen) = nil")
	}
	release()
}

func TestAcquireDifferentWorkspaces(t *testing.T) {
	pool := New()
	defer pool.CloseAll()
	one := workspace(t, "one")
	two := workspace(t, "two")
	first, releaseFirst, err := pool.Acquire(one)
	if err != nil {
		t.Fatalf("Acquire(one) error: %v", err)
	}
	defer releaseFirst()
	second, releaseSecond, err := pool.Acquire(two)
	if err != nil {
		t.Fatalf("Acquire(two) error: %v", err)
	}
	defer releaseSecond()
	if first == second {
		t.Fatal("different workspaces share a store")
	}
	if pool.Len() != 2 {
		t.Errorf("Len() = %d, want 2", pool.Len())
	}
}

func TestCloseAll(t *testing.T) {
	pool := New()
	ws := workspace(t, "agent")
	_, release, err := pool.Acquire(ws)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	defer release()
	if err := pool.CloseAll(); err != nil {
		t.Fatalf("CloseAll() error: %v", err)
	}
	if _, _, err := pool.Acquire(ws); err != ErrClosed {
		t.Fatalf("Acquire(closed) error = %v, want ErrClosed", err)
	}
	if pool.Len() != 0 {
		t.Errorf("Len() = %d, want 0", pool.Len())
	}
}

func TestConcurrentAcquireRelease(t *testing.T) {
	pool := New()
	defer pool.CloseAll()
	ws := workspace(t, "agent")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, release, err := pool.Acquire(ws)
			if err != nil {
				t.Errorf("Acquire() error: %v", err)
				return
			}
			if st == nil {
				t.Error("Acquire() returned nil store")
			}
			release()
		}()
	}
	wg.Wait()
	if pool.Len() != 0 {
		t.Errorf("Len() = %d, want 0", pool.Len())
	}
}
