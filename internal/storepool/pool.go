// Package storepool keeps one refcounted per-agent store open per workspace in
// this process. The store package intentionally rejects a second open handle
// for the same database, so every in-process service must share this pool.
package storepool

import (
	"errors"
	"fmt"
	"sync"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/store"
)

// ErrClosed means the pool has been closed.
var ErrClosed = errors.New("storepool: closed")

type entry struct {
	store *store.Store
	refs  int
}

// Pool owns one store per workspace while it has references.
type Pool struct {
	mu      sync.Mutex
	entries map[string]*entry
	closed  bool
}

// New returns an empty pool.
func New() *Pool {
	return &Pool{entries: map[string]*entry{}}
}

// Acquire returns a store and a release function. The store closes when the
// last reference is released.
func (p *Pool) Acquire(workspace string) (*store.Store, func(), error) {
	key, err := paths.Canonical(workspace)
	if err != nil {
		return nil, nil, fmt.Errorf("storepool: resolve workspace: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, ErrClosed
	}
	if existing := p.entries[key]; existing != nil {
		existing.refs++
		return existing.store, p.releaseFunc(key), nil
	}
	st, err := store.Open(key)
	if err != nil {
		return nil, nil, err
	}
	p.entries[key] = &entry{store: st, refs: 1}
	return st, p.releaseFunc(key), nil
}

// Retain adds a reference to an already-open workspace. It returns false when
// the workspace is not open.
func (p *Pool) Retain(workspace string) (func(), bool) {
	key, err := paths.Canonical(workspace)
	if err != nil {
		return nil, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, false
	}
	existing := p.entries[key]
	if existing == nil {
		return nil, false
	}
	existing.refs++
	return p.releaseFunc(key), true
}

// CloseAll closes every store regardless of outstanding references. It is
// intended for process shutdown only.
func (p *Pool) CloseAll() error {
	p.mu.Lock()
	p.closed = true
	entries := p.entries
	p.entries = map[string]*entry{}
	p.mu.Unlock()

	var joined error
	for _, item := range entries {
		if err := item.store.Close(); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

// Len reports how many workspaces are currently open.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

func (p *Pool) releaseFunc(key string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			item := p.entries[key]
			if item == nil {
				return
			}
			item.refs--
			if item.refs <= 0 {
				delete(p.entries, key)
				_ = item.store.Close()
			}
		})
	}
}
