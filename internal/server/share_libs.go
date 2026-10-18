package server

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"time"

	"picmover/internal/userdata"
)

// Libraries and phones (docs/design/lan-share.md, "Review fixes"): a
// library that a phone's request opened on demand is closed again, and
// its lock released, once no phone has used it for phoneIdle, unless the
// admin side has used it since. Reading a library's identity for the
// Share page is cached by the user.db file stamps.

// phoneIdle is how long a library opened only for phones stays open
// without a phone request (tests shorten it).
var phoneIdle = 10 * time.Minute

// openForPhone opens e for a phone's request; when that opened it (it was
// not open), e is marked as opened for phones.
func (ws *workspace) openForPhone(ctx context.Context, e *libEntry) error {
	e.mu.Lock()
	was := e.state == libOpen
	e.mu.Unlock()
	if err := ws.openEntry(ctx, e); err != nil {
		return err
	}
	e.mu.Lock()
	if !was && e.state == libOpen {
		e.phoneOpened = true
	}
	e.mu.Unlock()
	return nil
}

// notePhone records a phone's use of e.
func (e *libEntry) notePhone(now time.Time) {
	e.mu.Lock()
	e.phoneAt = now
	e.mu.Unlock()
}

// adminUse records that the admin side uses e: it stays open.
func (e *libEntry) adminUse() {
	e.mu.Lock()
	e.phoneOpened = false
	e.mu.Unlock()
}

// closePhoneLibraries closes the libraries opened only for phones that no
// phone used for idle (0: all of them), when no request, job or move uses
// them.
func (s *Server) closePhoneLibraries(idle time.Duration) {
	now := time.Now()
	s.ws.mu.Lock()
	libs := make([]*libEntry, 0, len(s.ws.libs))
	for _, e := range s.ws.libs {
		libs = append(libs, e)
	}
	s.ws.mu.Unlock()
	for _, e := range libs {
		if s.jobs != nil && s.jobBusy(e) {
			continue
		}
		e.mu.Lock()
		if e.phoneOpened && e.state == libOpen && e.inflight == 0 && e.moving.Load() == 0 && !e.external &&
			now.Sub(e.phoneAt) >= idle {
			e.closeHandlesLocked()
			e.state, e.phoneOpened = libClosed, false
			s.log.Info("library closed: it was opened for phones and no phone uses it now", "id", e.id)
		}
		e.mu.Unlock()
	}
}

// closePhoneLibrary closes e at once when only phones had opened it and
// nothing uses it.
func (s *Server) closePhoneLibrary(e *libEntry) {
	if s.jobs != nil && s.jobBusy(e) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phoneOpened && e.state == libOpen && e.inflight == 0 && e.moving.Load() == 0 && !e.external {
		e.closeHandlesLocked()
		e.state, e.phoneOpened = libClosed, false
	}
}

// jobBusy reports a job of e that has not finished.
func (s *Server) jobBusy(e *libEntry) bool {
	for _, j := range s.jobs.list(e) {
		j.mu.Lock()
		done := j.finalLocked()
		j.mu.Unlock()
		if !done {
			return true
		}
	}
	return false
}

// userPeek is a cached read of a closed library's user.db: its
// LibraryID and albums, with the stamps of user.db and its -wal.
type userPeek struct {
	db, wal fileStamp
	id      string
	albums  []userdata.Album
	hasID   bool
	hasAlb  bool
}

// userPeekReads counts the reads of peekUser (tests).
var userPeekReads atomic.Int64

// peekUser reads the LibraryID (albums false) or the albums of the
// user.db at path read-only, cached until user.db or its -wal change: the
// Share page polls every 2 s and the tray every 5 s.
func (ws *workspace) peekUser(ctx context.Context, path string, albums bool) (userPeek, error) {
	key := userPeek{db: stampOf(path), wal: stampOf(path + "-wal")}
	ws.mu.Lock()
	c, ok := ws.userPeeks[path]
	ws.mu.Unlock()
	if ok && c.db == key.db && c.wal == key.wal && ((albums && c.hasAlb) || (!albums && c.hasID)) {
		return c, nil
	}
	if !ok || c.db != key.db || c.wal != key.wal {
		c = key
	}
	userPeekReads.Add(1)
	if albums {
		a, err := userdata.PeekAlbums(ctx, path)
		if err != nil {
			return userPeek{}, err
		}
		c.albums, c.hasAlb = a, true
	} else {
		id, err := userdata.PeekLibraryID(ctx, path)
		if err != nil {
			return userPeek{}, err
		}
		c.id, c.hasID = id, true
	}
	ws.mu.Lock()
	if ws.userPeeks == nil {
		ws.userPeeks = map[string]userPeek{}
	}
	ws.userPeeks[path] = c
	ws.mu.Unlock()
	return c, nil
}

// peekLibraryID is userdata.PeekLibraryID of library dir, cached.
func (ws *workspace) peekLibraryID(ctx context.Context, dir string) (string, error) {
	p, err := ws.peekUser(ctx, filepath.Join(dir, userdata.FileName), false)
	return p.id, err
}

// peekAlbums is userdata.PeekAlbums of library dir, cached.
func (ws *workspace) peekAlbums(ctx context.Context, dir string) ([]userdata.Album, error) {
	p, err := ws.peekUser(ctx, filepath.Join(dir, userdata.FileName), true)
	return p.albums, err
}
