package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/imaging"
	"picmover/internal/thumbs"
)

// Large thumbnails (640 px) are served from <library>/.picmover/large/.
// One that is missing is derived on demand from the stored preview (never
// the original) and written there: the large directory is the only thing
// the server writes into the cache (thumbs.db stays read-only).

const (
	// largeTimeout bounds one on-demand derivation once it has started.
	// It runs detached from the requests waiting for it, so the others
	// are not failed by one client going away.
	largeTimeout = 30 * time.Second
	// largeBudget bounds the decoded-pixel memory of concurrent
	// derivations (a 1600 px preview is about 10 MB decoded).
	largeBudget = 256 << 20
	// largeMissingTTL is how long a library's largeMissing count is
	// served before it is recomputed in the background.
	largeMissingTTL = time.Minute
	// largeMissingWalkTimeout bounds one background recount.
	largeMissingWalkTimeout = 5 * time.Minute
)

// largeDeriver runs the server's on-demand derivations: one at a time per
// store and hash (later requests join the first one's flight), at most
// slots at once overall. A flight waiting for a slot is dropped when every
// request waiting for it has gone (scrolled away, lightbox moved on), and
// the waiting flights get a slot in LIFO order: the most recent request
// is the tile most likely still on screen. Once started, a flight runs to
// the end (bounded by largeTimeout) so every request that joined it gets
// the result.
type largeDeriver struct {
	slots  int
	budget *imaging.MemBudget
	log    *slog.Logger
	// derive produces the large thumbnail; tests replace it.
	derive func(ctx context.Context, s *thumbs.Store, hash uint64, opts imaging.Options) (thumbs.DerivedLarge, error)

	mu      sync.Mutex
	running int
	waiting []*largeFlight // not started, oldest first; started from the end
	flights map[largeKey]*largeFlight
}

type largeKey struct {
	store *thumbs.Store
	hash  uint64
}

type largeFlight struct {
	key   largeKey
	entry *libEntry // nil in tests
	libID string
	// parent is the context of the request that created the flight; the
	// derivation runs under context.WithoutCancel(parent).
	parent  context.Context
	waiters int  // requests waiting on done (d.mu)
	started bool // got a slot (d.mu)
	done    chan struct{}
	res     thumbs.DerivedLarge // set before done is closed
	err     error
}

// largeSlots is max(2, NumCPU/2): derivations share the CPU with the
// grid's other requests and with a running job.
func largeSlots() int { return max(2, runtime.NumCPU()/2) }

func newLargeDeriver(log *slog.Logger) *largeDeriver {
	return &largeDeriver{
		slots:   largeSlots(),
		budget:  imaging.NewMemBudget(largeBudget),
		log:     log,
		derive:  thumbs.DeriveLargeData,
		flights: map[largeKey]*largeFlight{},
	}
}

// do derives the large thumbnail of hash in l's store, or joins the
// derivation of it already queued or running, and waits for the result
// or ctx. A request that gives up while its flight is still queued
// leaves it; the last one to leave removes the flight from the queue.
func (d *largeDeriver) do(ctx context.Context, l *libHandles, hash uint64) (thumbs.DerivedLarge, error) {
	k := largeKey{l.store, hash}
	d.mu.Lock()
	f, ok := d.flights[k]
	if !ok {
		f = &largeFlight{key: k, entry: l.entry, libID: l.id, parent: ctx, done: make(chan struct{})}
		d.flights[k] = f
		d.waiting = append(d.waiting, f)
		d.startLocked()
	}
	f.waiters++
	d.mu.Unlock()

	select {
	case <-f.done:
		return f.res, f.err
	case <-ctx.Done():
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	f.waiters--
	if f.waiters == 0 && !f.started {
		if i := slices.Index(d.waiting, f); i >= 0 {
			d.waiting = slices.Delete(d.waiting, i, i+1)
		}
		delete(d.flights, k)
	}
	return thumbs.DerivedLarge{}, ctx.Err()
}

// startLocked starts the most recently queued flights while slots are
// free. d.mu is held.
func (d *largeDeriver) startLocked() {
	for d.running < d.slots && len(d.waiting) > 0 {
		f := d.waiting[len(d.waiting)-1]
		d.waiting = d.waiting[:len(d.waiting)-1]
		f.started = true
		d.running++
		go d.run(f)
	}
}

// run derives f's large thumbnail. The flight is removed, its slot
// released and done closed in a defer, also when the derivation panics.
func (d *largeDeriver) run(f *largeFlight) {
	defer func() {
		if p := recover(); p != nil {
			f.res, f.err = thumbs.DerivedLarge{}, fmt.Errorf("large thumbnail: panic: %v", p)
			d.log.Error("large thumbnail derivation panicked", "library", f.libID, "hash", hexHash(f.key.hash), "panic", p)
		}
		d.mu.Lock()
		delete(d.flights, f.key)
		d.running--
		d.startLocked()
		d.mu.Unlock()
		close(f.done)
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.parent), largeTimeout)
	defer cancel()
	f.res, f.err = d.derive(ctx, f.key.store, f.key.hash, imaging.Options{Budget: d.budget})
	d.after(f)
}

// after records a finished derivation once per flight: the cached
// largeMissing count drops by one for a large written or for a hash newly
// known not to derive, and failures are logged once (later requests for
// a failed hash get thumbs.ErrLargeFailed at once and are not logged).
func (d *largeDeriver) after(f *largeFlight) {
	hash, s := f.key.hash, f.key.store
	switch {
	case f.err == nil:
		if f.res.Written && f.entry != nil {
			f.entry.largeMissingDone()
		}
		if f.res.WriteErr != nil && !errors.Is(f.res.WriteErr, thumbs.ErrLargeUnwritable) {
			d.log.Info("large thumbnail not written; serving it uncached", "library", f.libID, "hash", hexHash(hash), "err", f.res.WriteErr)
		}
	case errors.Is(f.err, thumbs.ErrLargeFailed):
	default:
		if s != nil && s.LargeFailed(hash) && f.entry != nil {
			f.entry.largeMissingDone()
		}
		d.log.Debug("large thumbnail not derived; serving the thumbnail", "library", f.libID, "hash", hexHash(hash), "err", f.err)
	}
}

// handleLarge serves the large thumbnail: the stored file, else one
// derived now from the preview (written to disk; when the library folder
// cannot be written, the derived bytes with no-store), else (the
// derivation failed: an undecodable preview, a timeout) the thumbnail
// bytes with no-store so the client asks again later; 404 when there is
// no preview (or, after a failed derivation, no thumbnail either).
// Content-Type comes from the bytes.
func (s *Server) handleLarge(w http.ResponseWriter, r *http.Request, l *libHandles) {
	hash, ok := renditionHash(w, r)
	if !ok {
		return
	}
	if s.serveLargeFile(w, r, l, hash) {
		return
	}
	if fi, err := os.Lstat(l.store.PreviewPath(hash)); err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "no large thumbnail")
		return
	}
	res, err := s.large.do(r.Context(), l, hash)
	if r.Context().Err() != nil {
		return
	}
	if err == nil {
		if s.serveLargeFile(w, r, l, hash) {
			return
		}
		if res.Data != nil {
			// Derived but not stored (read-only folder): the real large
			// thumbnail, but never cached, so it is stored and cached once
			// that is possible.
			serveUncached(w, res.Data)
			return
		}
	}
	data, _, _, found, terr := l.store.Thumb(r.Context(), hash)
	if terr != nil {
		s.internalError(w, r, terr)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no large thumbnail")
		return
	}
	// Not the large thumbnail: never cached under its URL.
	serveUncached(w, data)
}

// serveUncached answers 200 with rendition bytes that must not be cached
// under the URL.
func serveUncached(w http.ResponseWriter, data []byte) {
	h := w.Header()
	h.Set("Content-Type", renditionType(bytes.NewReader(data)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// serveLargeFile serves the stored large thumbnail of hash and reports
// whether it answered (served, or an internal error); false when there is
// no such file. An empty file (external corruption) counts as missing:
// the derivation replaces it.
func (s *Server) serveLargeFile(w http.ResponseWriter, r *http.Request, l *libHandles, hash uint64) bool {
	f, err := openRegular(l.store.LargePath(hash))
	if isMissing(err) {
		return false
	}
	if err != nil {
		s.internalError(w, r, err)
		return true
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		return false
	}
	serveRenditionFile(w, r, hash, f)
	return true
}

// largeMissing returns the number of rows (thumbnail ok, not deleted)
// whose preview exists and whose large thumbnail does not, less those
// whose derivation failed in this process: what the thumbs job would
// fill. Counting needs the catalog's hashes and one listing of each
// fan-out directory, so the count is cached per library. Only the very
// first call counts in the request; afterwards a stale count (older than
// largeMissingTTL) is served while it is recounted in the background, a
// job's end recounts in the job's goroutine, and an on-demand derivation
// adjusts it (largeMissingDone).
func (s *Server) largeMissing(ctx context.Context, l *libHandles) (int64, error) {
	e := l.entry
	e.largeMu.Lock()
	defer e.largeMu.Unlock()
	if e.largeAt.IsZero() {
		n, err := countLargeMissing(ctx, l.db, l.store)
		if err != nil {
			return 0, err
		}
		e.largeMissing, e.largeAt = n, time.Now()
		return n, nil
	}
	if time.Since(e.largeAt) >= largeMissingTTL && !e.largeBusy {
		e.largeBusy = true
		go func() {
			e.recountLargeMissing()
			e.largeMu.Lock()
			e.largeBusy = false
			e.largeMu.Unlock()
		}()
	}
	return e.largeMissing, nil
}

// recountLargeMissing recounts largeMissing outside any request (in the
// background, or at the end of a job) while the cached value keeps being
// served. Nothing happens when the library is not open.
func (e *libEntry) recountLargeMissing() {
	h, _, ok := e.acquire()
	if !ok {
		return
	}
	defer e.release()
	ctx, cancel := context.WithTimeout(context.Background(), largeMissingWalkTimeout)
	defer cancel()
	n, err := countLargeMissing(ctx, h.db, h.store)
	if err != nil {
		return
	}
	e.largeMu.Lock()
	e.largeMissing, e.largeAt = n, time.Now()
	e.largeMu.Unlock()
}

func countLargeMissing(ctx context.Context, db *catalog.DB, store *thumbs.Store) (int64, error) {
	p := store.NewPresence()
	var n int64
	err := db.ThumbOKHashes(ctx, func(hash uint64) error {
		if p.Preview(hash) && !p.Large(hash) && !store.LargeFailed(hash) {
			n++
		}
		return nil
	})
	return n, err
}

// largeMissingDone takes one off the cached largeMissing count: a large
// thumbnail was written on demand, or a hash newly failed to derive. The
// next recount corrects any drift (a hash outside the counted rows).
func (e *libEntry) largeMissingDone() {
	e.largeMu.Lock()
	if !e.largeAt.IsZero() && e.largeMissing > 0 {
		e.largeMissing--
	}
	e.largeMu.Unlock()
}

// invalidateLargeMissing makes the next largeMissing call count in the
// request (tests).
func (e *libEntry) invalidateLargeMissing() {
	e.largeMu.Lock()
	e.largeAt = time.Time{}
	e.largeMu.Unlock()
}
