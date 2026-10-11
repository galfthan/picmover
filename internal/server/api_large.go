package server

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"picmover/internal/imaging"
	"picmover/internal/thumbs"
)

// Large thumbnails (640 px) are served from <library>/.picmover/large/.
// One that is missing is derived on demand from the stored preview (never
// the original) and written there: the large directory is the only thing
// the server writes into the cache (thumbs.db stays read-only).

const (
	// largeTimeout bounds one on-demand derivation. It runs detached from
	// the request that started it, so other requests waiting for the same
	// hash are not failed by that client going away.
	largeTimeout = 30 * time.Second
	// largeBudget bounds the decoded-pixel memory of concurrent
	// derivations (a 1600 px preview is about 10 MB decoded).
	largeBudget = 256 << 20
	// largeMissingTTL is how long a library's largeMissing count is
	// reused before it is computed again (it is also recomputed after a
	// job ends or a derivation succeeds).
	largeMissingTTL = time.Minute
)

// largeDeriver runs the server's on-demand derivations: one at a time per
// store and hash (later requests wait for the first), at most cap(sem)
// at once overall (others wait).
type largeDeriver struct {
	sem    chan struct{}
	budget *imaging.MemBudget
	// derive writes the large thumbnail; tests replace it.
	derive func(ctx context.Context, s *thumbs.Store, hash uint64, opts imaging.Options) (bool, error)

	mu      sync.Mutex
	flights map[largeKey]*largeFlight
}

type largeKey struct {
	store *thumbs.Store
	hash  uint64
}

type largeFlight struct {
	done chan struct{}
	err  error
}

// largeSlots is max(2, NumCPU/2): derivations share the CPU with the
// grid's other requests and with a running job.
func largeSlots() int { return max(2, runtime.NumCPU()/2) }

func newLargeDeriver() *largeDeriver {
	return &largeDeriver{
		sem:     make(chan struct{}, largeSlots()),
		budget:  imaging.NewMemBudget(largeBudget),
		derive:  thumbs.DeriveLarge,
		flights: map[largeKey]*largeFlight{},
	}
}

// do derives the large thumbnail of hash in s unless a derivation of it is
// already running, in which case it waits for that one's result (or ctx).
func (d *largeDeriver) do(ctx context.Context, s *thumbs.Store, hash uint64) error {
	k := largeKey{s, hash}
	d.mu.Lock()
	if f, ok := d.flights[k]; ok {
		d.mu.Unlock()
		select {
		case <-f.done:
			return f.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f := &largeFlight{done: make(chan struct{})}
	d.flights[k] = f
	d.mu.Unlock()

	f.err = d.run(ctx, s, hash)
	d.mu.Lock()
	delete(d.flights, k)
	d.mu.Unlock()
	close(f.done)
	return f.err
}

func (d *largeDeriver) run(parent context.Context, s *thumbs.Store, hash uint64) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), largeTimeout)
	defer cancel()
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-d.sem }()
	_, err := d.derive(ctx, s, hash, imaging.Options{Budget: d.budget})
	return err
}

// handleLarge serves the large thumbnail: the stored file, else one
// derived now from the preview, else (the derivation failed, e.g. a
// read-only library folder) the thumbnail bytes with no-store so the
// client asks again later; 404 when there is no preview (or, after a
// failed derivation, no thumbnail either). Content-Type comes from the
// bytes.
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
	err := s.large.do(r.Context(), l.store, hash)
	if err == nil {
		l.entry.invalidateLargeMissing()
		if s.serveLargeFile(w, r, l, hash) {
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	s.log.Debug("large thumbnail not derived; serving the thumbnail", "library", l.id, "hash", hexHash(hash), "err", err)
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
	h := w.Header()
	h.Set("Content-Type", renditionType(bytes.NewReader(data)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// serveLargeFile serves the stored large thumbnail of hash and reports
// whether it answered (served, or an internal error); false when there is
// no such file.
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
	serveRenditionFile(w, r, hash, f)
	return true
}

// largeMissing returns the number of rows (thumbnail ok, not deleted)
// whose preview exists and whose large thumbnail does not: what the
// thumbs job would fill. It needs the catalog's hashes and one listing of
// each fan-out directory, so it is cached per library for
// largeMissingTTL and recomputed after a job ends or a derivation
// succeeds (invalidateLargeMissing).
func (s *Server) largeMissing(ctx context.Context, l *libHandles) (int64, error) {
	e := l.entry
	e.largeMu.Lock()
	defer e.largeMu.Unlock()
	if !e.largeAt.IsZero() && time.Since(e.largeAt) < largeMissingTTL {
		return e.largeMissing, nil
	}
	p := l.store.NewPresence()
	var n int64
	err := l.db.ThumbOKHashes(ctx, func(hash uint64) error {
		if p.Preview(hash) && !p.Large(hash) {
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	e.largeMissing, e.largeAt = n, time.Now()
	return n, nil
}

// invalidateLargeMissing makes the next largeMissing call recompute.
func (e *libEntry) invalidateLargeMissing() {
	e.largeMu.Lock()
	e.largeAt = time.Time{}
	e.largeMu.Unlock()
}
