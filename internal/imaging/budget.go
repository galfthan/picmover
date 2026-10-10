package imaging

import (
	"context"
	"sync"
)

// MemBudget bounds the memory that concurrent decodes may hold at once. It
// is a counting semaphore over bytes: Acquire blocks until n bytes are
// free (or the context ends) and Release returns them. A single
// acquisition larger than the whole budget is granted when nothing else is
// held, so one huge image still proceeds, alone.
//
// A nil *MemBudget is valid and imposes no limit. Waiters are not queued
// in order: a large request can wait while smaller ones keep fitting.
type MemBudget struct {
	mu    sync.Mutex
	total int64
	held  int64
	wake  chan struct{} // closed (and replaced) on every Release
}

// NewMemBudget returns a budget of the given number of bytes (at least 1).
func NewMemBudget(bytes int64) *MemBudget {
	return &MemBudget{total: max(bytes, 1)}
}

// Acquire blocks until n bytes are available or ctx ends; it returns
// ctx.Err() in the latter case. n <= 0 acquires nothing.
func (b *MemBudget) Acquire(ctx context.Context, n int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil || n <= 0 {
		return nil
	}
	for {
		b.mu.Lock()
		if b.held == 0 || n <= b.total-b.held {
			b.held += n
			b.mu.Unlock()
			return nil
		}
		if b.wake == nil {
			b.wake = make(chan struct{})
		}
		w := b.wake
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w:
		}
	}
}

// Release returns n bytes previously acquired.
func (b *MemBudget) Release(n int64) {
	if b == nil || n <= 0 {
		return
	}
	b.mu.Lock()
	b.held = max(b.held-n, 0)
	if b.wake != nil {
		close(b.wake)
		b.wake = nil
	}
	b.mu.Unlock()
}

// Held reports the bytes currently acquired.
func (b *MemBudget) Held() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held
}
