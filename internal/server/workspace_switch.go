package server

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"picmover/internal/library"
)

// The desktop app's workspace switch, as the registry sees it: the new
// configuration is prepared (a single library opened and locked, or an
// entry this process already holds handed over), published in one step
// (publish), and only then are the previous registry's entries retired
// (retireAll), after the requests still using them had a moment to end.

// ownLock reports whether a lock holder is this process (same pid, and
// the same host when the lock file names one).
func ownLock(l *library.ErrLocked) bool {
	if l == nil || l.PID != os.Getpid() {
		return false
	}
	if l.Host == "" {
		return true
	}
	h, err := os.Hostname()
	return err == nil && strings.EqualFold(h, l.Host)
}

// entries returns the registered entries.
func (ws *workspace) entries() []*libEntry {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	out := make([]*libEntry, 0, len(ws.libs))
	for _, e := range ws.libs {
		out = append(out, e)
	}
	return out
}

// publish makes c, with the registry libs, the served configuration in
// one step under ws.mu: a request sees either the old configuration or
// the new one, never a gap. take, when not nil, runs under ws.mu before
// the swap with the old registry's entries and may add entries handed
// over from them to libs; when it fails nothing changes. publish returns
// the old registry's entries, which the caller retires (retireAll), and
// errClosed once a shutdown began.
func (ws *workspace) publish(c *wsConf, libs map[string]*libEntry, take func(old []*libEntry, libs map[string]*libEntry) error) ([]*libEntry, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return nil, errClosed
	}
	old := make([]*libEntry, 0, len(ws.libs))
	for _, e := range ws.libs {
		old = append(old, e)
	}
	if take != nil {
		if err := take(old, libs); err != nil {
			return nil, err
		}
	}
	ws.libs = libs
	ws.peeks = map[string]peekEntry{}
	ws.conf.Store(c)
	return old, nil
}

// retireWait bounds how long a workspace switch waits for the requests
// that still use the previous registry's libraries before it retires
// them anyway (their handles then close when the last request ends).
const retireWait = 5 * time.Second

// retireAll retires entries that left the registry: it waits up to wait
// for the requests using them to end, then retires every one. Entries
// already retired (handed over: their handles live on in the new entry)
// are not waited for.
func retireAll(entries []*libEntry, wait time.Duration) {
	deadline := time.Now().Add(wait)
	for {
		busy := 0
		for _, e := range entries {
			e.mu.Lock()
			if !e.retired.Load() {
				busy += e.inflight
			}
			e.mu.Unlock()
		}
		if busy == 0 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, e := range entries {
		e.retire()
	}
}

// retire closes a library that left the registry: its handles close and
// its lock is released now, or when the last request using them ends. A
// lock handed to the server in single mode is released too. Open,
// rebuild and job starts refuse it from now on (errRetired).
func (e *libEntry) retire() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.retired.Store(true)
	e.external = false // closeHandlesLocked releases every lock
	if e.state == libOpen || e.state == libRebuilding {
		e.state = libClosed
	}
	e.stale = true
	if e.inflight == 0 {
		e.closeHandlesLocked()
	}
}

// handOver moves the open library of e (its lock, catalog, store and
// user.db) into a new, unregistered entry id at path, and leaves e
// retired and empty: requests still holding e's handles keep using them,
// now owned by the new entry, so nothing is closed or locked again. ok
// is false when e is not open on its folder. external marks the lock as
// the single mode's (released by closeAll or a later switch only).
func (e *libEntry) handOver(id, path string, external bool) (n *libEntry, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.retired.Load() || e.state != libOpen || !e.sameFolderLocked() {
		return nil, false
	}
	n = &libEntry{id: id, path: path, state: libOpen, lib: e.lib, db: e.db, store: e.store, user: e.user,
		unlock: e.unlock, external: external, lockInfo: e.lockInfo}
	e.lib, e.db, e.store, e.user, e.unlock, e.lockInfo = nil, nil, nil, nil, nil, nil
	e.state, e.stale, e.external = libClosed, false, false
	e.retired.Store(true)
	return n, true
}

// isOpen reports whether e is open on its folder (a hand-over candidate).
func (e *libEntry) isOpen() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.retired.Load() && e.state == libOpen && e.sameFolderLocked()
}

// sameDir reports whether a and b name the same existing directory.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}
