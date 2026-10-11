package thumbs

import (
	"errors"
	"sync"
	"time"
)

// What a Store remembers about large thumbnails it could not produce, for
// as long as it is open (a reopened Store starts afresh): hashes whose
// derivation failed (an undecodable preview: retrying costs a decode on
// every request and logs every time) and whether the large directory
// refused a write (a read-only library folder: the derived bytes are
// still served, uncached, but no write is attempted for a while).

// maxLargeFailed bounds the failed hashes a Store remembers; beyond it
// new failures are not remembered (and so retried).
const maxLargeFailed = 1 << 16

// largeRetryAfter is how long after a failed write PutLargeIfWritable
// skips writing. Once it has passed, the next derivation tries again; a
// write that succeeds clears the state.
const largeRetryAfter = 5 * time.Minute

var (
	// ErrLargeFailed is returned (wrapped) by DeriveLarge and
	// DeriveLargeData for a hash whose derivation already failed in this
	// Store.
	ErrLargeFailed = errors.New("large thumbnail could not be derived earlier")
	// ErrLargeUnwritable is DerivedLarge.WriteErr when the write was
	// skipped because a recent write into the large directory failed.
	ErrLargeUnwritable = errors.New("large thumbnail directory is not writable")
)

type largeState struct {
	mu           sync.Mutex
	failed       map[uint64]struct{}
	unwritableAt time.Time // zero: writable (or never tried)
}

// LargeFailed reports whether deriving the large thumbnail of hash failed
// earlier in this Store.
func (s *Store) LargeFailed(hash uint64) bool {
	s.ls.mu.Lock()
	defer s.ls.mu.Unlock()
	_, ok := s.ls.failed[hash]
	return ok
}

// MarkLargeFailed remembers that the large thumbnail of hash cannot be
// derived. It reports whether hash was newly remembered (false: already
// known, or the set is full).
func (s *Store) MarkLargeFailed(hash uint64) bool {
	s.ls.mu.Lock()
	defer s.ls.mu.Unlock()
	if _, ok := s.ls.failed[hash]; ok || len(s.ls.failed) >= maxLargeFailed {
		return false
	}
	if s.ls.failed == nil {
		s.ls.failed = map[uint64]struct{}{}
	}
	s.ls.failed[hash] = struct{}{}
	return true
}

// LargeUnwritable reports whether a write into the large directory failed
// less than largeRetryAfter ago (and none succeeded since).
func (s *Store) LargeUnwritable() bool {
	s.ls.mu.Lock()
	defer s.ls.mu.Unlock()
	return !s.ls.unwritableAt.IsZero() && time.Since(s.ls.unwritableAt) < largeRetryAfter
}

// setLargeWritable records the outcome of a write into the large
// directory.
func (s *Store) setLargeWritable(ok bool) {
	s.ls.mu.Lock()
	defer s.ls.mu.Unlock()
	if ok {
		s.ls.unwritableAt = time.Time{}
	} else {
		s.ls.unwritableAt = time.Now()
	}
}
