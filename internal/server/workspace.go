package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/thumbs"
	"picmover/internal/userdata"
)

// Library states (the lib object's state).
const (
	libClosed     = "closed"     // listed, summary from a read-only peek, not opened
	libOpen       = "open"       // opened: locked, catalog, store and user.db open
	libBusy       = "busy"       // locked by another process
	libRebuilding = "rebuilding" // a rebuild job runs on it
	libError      = "error"      // media.db unreadable or needs a rebuild
	libFolder     = "folder"     // a directory without media.db
)

// Workspace modes.
const (
	modeWorkspace = "workspace"
	modeSingle    = "single"
)

const (
	peekTTL      = 10 * time.Second // peek cache lifetime
	drainTimeout = 10 * time.Second // a rebuild waits this long for in-flight requests
	maxIDBytes   = 255
)

// errLibNotFound: the id is malformed or not in the workspace listing.
var errLibNotFound = errors.New("no such library")

// libEntry is one library the server has opened or tried to open. Fields
// below mu are guarded by it.
type libEntry struct {
	id   string
	path string // <root>/<id>; in single mode the library root

	// trashMu serialises trash operations on this library.
	trashMu sync.Mutex

	mu       sync.Mutex
	state    string
	errMsg   string
	holder   *library.ErrLocked
	lib      *library.Library
	db       *catalog.DB
	store    *thumbs.Store
	user     *userdata.DB
	unlock   func()
	inflight int // requests using the handles
}

// libHandles are the open handles of a library for one request.
type libHandles struct {
	id    string
	entry *libEntry
	lib   *library.Library
	db    *catalog.DB
	store *thumbs.Store
	user  *userdata.DB
}

// acquire returns the handles of an open library and counts the request
// as in flight (call release). ok is false when the library is not open;
// state then says why.
func (e *libEntry) acquire() (h *libHandles, state string, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != libOpen {
		return nil, e.state, false
	}
	e.inflight++
	return &libHandles{id: e.id, entry: e, lib: e.lib, db: e.db, store: e.store, user: e.user}, libOpen, true
}

func (e *libEntry) release() {
	e.mu.Lock()
	e.inflight--
	e.mu.Unlock()
}

// snapshot returns the state and the handles without counting a request
// (for listings and jobs, which the state machine already orders).
func (e *libEntry) snapshot() (state string, h *libHandles) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state, &libHandles{id: e.id, entry: e, lib: e.lib, db: e.db, store: e.store, user: e.user}
}

// closeLocked closes the catalog and the store (not user.db, not the
// lock).
func (e *libEntry) closeCatalogLocked() {
	if e.db != nil {
		e.db.Close()
		e.db = nil
	}
	if e.store != nil {
		e.store.Close()
		e.store = nil
	}
}

// closeAll closes every handle and releases the lock.
func (e *libEntry) closeAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeCatalogLocked()
	if e.user != nil {
		e.user.Close()
		e.user = nil
	}
	if e.unlock != nil {
		e.unlock()
		e.unlock = nil
	}
	if e.state == libOpen || e.state == libRebuilding {
		e.state = libClosed
	}
}

// workspace is the library registry: a folder of libraries (workspace
// mode) or one library (single mode).
type workspace struct {
	root   string // absolute; the workspace folder, or the library root in single mode
	single bool
	log    *slog.Logger

	mu     sync.Mutex
	closed bool                 // shut down: nothing opens any more
	libs   map[string]*libEntry // by id
	peeks  map[string]peekEntry // by media.db path
	now    func() time.Time
}

type peekEntry struct {
	size    int64
	mtime   time.Time
	at      time.Time
	peek    catalog.Peek
	err     error
	errText string
}

// validLibID reports whether id can name a library folder: one path
// component, not . or .., no /, \, : or NUL, not starting with a dot
// (hidden, and .picmover), at most 255 bytes.
func validLibID(id string) bool {
	if id == "" || len(id) > maxIDBytes || id == "." || id == ".." || strings.HasPrefix(id, ".") {
		return false
	}
	return !strings.ContainsAny(id, "/\\:\x00")
}

// reservedName reports Windows device names (CON, PRN, AUX, NUL,
// COM1-COM9, LPT1-LPT9, case-insensitively, with or without an
// extension) and names Windows cannot create (trailing dot or space,
// control characters). New libraries may not use them on any platform,
// so a workspace stays usable from every OS.
func reservedName(name string) bool {
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return true
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>"|?*`, r) {
			return true
		}
	}
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

// singleID is the id of the library in single mode: the base name of its
// root, "library" when that is empty or not a valid id (a drive root).
func singleID(root string) string {
	id := filepath.Base(root)
	if !validLibID(id) {
		return "library"
	}
	return id
}

// listed is one directory entry of the workspace listing.
type listed struct {
	id      string
	path    string
	library bool // has a regular media.db
}

// discover lists the workspace: one ReadDir of the root. Hidden entries
// (dot names, the Windows hidden attribute) and invalid ids are left
// out; a symlinked folder is listed only when it resolves inside the
// root.
func (ws *workspace) discover() ([]listed, error) {
	if ws.single {
		return []listed{{id: singleID(ws.root), path: ws.root, library: true}}, nil
	}
	ents, err := os.ReadDir(ws.root)
	if err != nil {
		return nil, err
	}
	var out []listed
	for _, e := range ents {
		name := e.Name()
		p := filepath.Join(ws.root, name)
		if !validLibID(name) || isHidden(e, p) {
			continue
		}
		switch {
		case e.IsDir():
		case e.Type()&fs.ModeSymlink != 0:
			if !ws.symlinkInside(p) {
				continue
			}
		default:
			continue
		}
		fi, err := os.Lstat(filepath.Join(p, library.DBName))
		out = append(out, listed{id: name, path: p, library: err == nil && fi.Mode().IsRegular()})
	}
	slices.SortFunc(out, func(a, b listed) int { return cmp.Compare(a.id, b.id) })
	return out, nil
}

// symlinkInside reports whether the link p resolves to a directory inside
// the workspace root.
func (ws *workspace) symlinkInside(p string) bool {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(ws.root)
	if err != nil {
		root = ws.root
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	fi, err := os.Stat(resolved)
	return err == nil && fi.IsDir()
}

// find returns the listing entry of id (exact byte match), or
// errLibNotFound.
func (ws *workspace) find(id string) (listed, error) {
	if !validLibID(id) {
		return listed{}, errLibNotFound
	}
	all, err := ws.discover()
	if err != nil {
		return listed{}, err
	}
	for _, l := range all {
		if l.id == id {
			return l, nil
		}
	}
	return listed{}, errLibNotFound
}

// entry returns the registered entry of id, or nil.
func (ws *workspace) entry(id string) *libEntry {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.libs[id]
}

// entryFor returns the entry of a listed library, registering it (state
// closed or folder) when new.
func (ws *workspace) entryFor(l listed) *libEntry {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if e := ws.libs[l.id]; e != nil {
		return e
	}
	state := libClosed
	if !l.library {
		state = libFolder
	}
	e := &libEntry{id: l.id, path: l.path, state: state}
	ws.libs[l.id] = e
	return e
}

// peek returns the read-only peek of the catalog at dbPath, cached by the
// file's size and mtime for peekTTL.
func (ws *workspace) peek(ctx context.Context, dbPath string) (catalog.Peek, error) {
	fi, err := os.Stat(dbPath)
	if err != nil {
		return catalog.Peek{}, err
	}
	now := ws.now()
	ws.mu.Lock()
	c, ok := ws.peeks[dbPath]
	ws.mu.Unlock()
	if ok && c.size == fi.Size() && c.mtime.Equal(fi.ModTime()) && now.Sub(c.at) < peekTTL {
		return c.peek, c.err
	}
	p, err := catalog.PeekFile(ctx, dbPath)
	ws.mu.Lock()
	ws.peeks[dbPath] = peekEntry{size: fi.Size(), mtime: fi.ModTime(), at: now, peek: p, err: err}
	ws.mu.Unlock()
	return p, err
}

// open opens the library of e: the lock, the catalog (never created),
// the thumbnail store (read-only, created empty when absent) and
// user.db. On failure the entry's state says why (busy, error).
func (ws *workspace) open(ctx context.Context, e *libEntry) error {
	if ws.isClosed() {
		return errClosed
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch e.state {
	case libOpen, libRebuilding:
		return nil
	}
	e.holder, e.errMsg = nil, ""
	lib, err := library.Attach(e.path)
	if err != nil {
		return e.failLocked(libError, err)
	}
	unlock, err := lib.Lock(ctx, "serve")
	var locked *library.ErrLocked
	switch {
	case errors.As(err, &locked):
		e.state, e.holder = libBusy, locked
		return err
	case errors.Is(err, library.ErrLockUnsupported):
		ws.log.Warn("cannot lock the library; serving it without the lock", "library", lib.Root(), "err", err)
	case err != nil:
		return e.failLocked(libError, err)
	}
	db, err := catalog.OpenExisting(ctx, lib.DBPath(), ws.log)
	if err != nil {
		unlock()
		return e.failLocked(libError, err)
	}
	if err := e.attachLocked(ctx, lib, db, unlock); err != nil {
		db.Close()
		unlock()
		return e.failLocked(libError, err)
	}
	ws.log.Info("library opened", "id", e.id, "root", lib.Root())
	return nil
}

// attachLocked completes an open with an opened catalog: store and
// user.db (kept when already open), state open.
func (e *libEntry) attachLocked(ctx context.Context, lib *library.Library, db *catalog.DB, unlock func()) error {
	store, err := openReadStore(lib)
	if err != nil {
		return err
	}
	if e.user == nil {
		user, err := userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName))
		if err != nil {
			store.Close()
			return err
		}
		e.user = user
	}
	db.SetMaxIdleConns(maxIdleConns)
	e.lib, e.db, e.store = lib, db, store
	if unlock != nil {
		e.unlock = unlock
	}
	e.state, e.errMsg, e.holder = libOpen, "", nil
	return nil
}

func (e *libEntry) failLocked(state string, err error) error {
	e.state, e.errMsg = state, err.Error()
	return err
}

// openReadStore opens the thumbnail cache read-only, creating an empty
// one first when the library has none yet: jobs write through their own
// store.
func openReadStore(lib *library.Library) (*thumbs.Store, error) {
	store, err := thumbs.OpenStoreReadOnly(lib)
	if errors.Is(err, fs.ErrNotExist) {
		w, werr := thumbs.OpenStore(lib)
		if werr != nil {
			return nil, werr
		}
		w.Close()
		store, err = thumbs.OpenStoreReadOnly(lib)
	}
	return store, err
}

// beginRebuild switches e to rebuilding: new requests get 503, in-flight
// ones are waited for (bounded by drainTimeout), then the catalog and the
// store are closed. A library that is not open (a folder, an unreadable
// catalog) is attached and locked first; user.db is opened (and kept
// open). prev is the state to restore on failure before the catalog is
// replaced.
func (ws *workspace) beginRebuild(ctx context.Context, e *libEntry) (lib *library.Library, user *userdata.DB, prev string, err error) {
	e.mu.Lock()
	prev = e.state
	switch e.state {
	case libRebuilding:
		e.mu.Unlock()
		return nil, nil, prev, errors.New("the library is already being rebuilt")
	case libBusy, libClosed, libFolder, libError:
		e.holder = nil
		lib, err := library.Attach(e.path)
		if err != nil {
			e.mu.Unlock()
			return nil, nil, prev, err
		}
		unlock, err := lib.Lock(ctx, "serve")
		var locked *library.ErrLocked
		switch {
		case errors.As(err, &locked):
			e.state, e.holder = libBusy, locked
			e.mu.Unlock()
			return nil, nil, prev, err
		case errors.Is(err, library.ErrLockUnsupported):
			ws.log.Warn("cannot lock the library; rebuilding it without the lock", "library", lib.Root(), "err", err)
		case err != nil:
			e.mu.Unlock()
			return nil, nil, prev, err
		}
		if e.user == nil {
			u, err := userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName))
			if err != nil {
				unlock()
				e.mu.Unlock()
				return nil, nil, prev, err
			}
			e.user = u
		}
		e.lib, e.unlock = lib, unlock
	}
	e.state = libRebuilding
	lib, user = e.lib, e.user
	e.mu.Unlock()

	deadline := time.Now().Add(drainTimeout)
	for {
		e.mu.Lock()
		n := e.inflight
		if n == 0 || time.Now().After(deadline) {
			e.closeCatalogLocked()
			e.mu.Unlock()
			break
		}
		e.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	return lib, user, prev, nil
}

// endRebuild reopens e after a rebuild with the catalog db (nil when the
// rebuild could not create one: the state becomes error with msg).
func (ws *workspace) endRebuild(ctx context.Context, e *libEntry, db *catalog.DB, failure error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if db == nil {
		msg := "rebuild failed"
		if failure != nil {
			msg = failure.Error()
		}
		e.state, e.errMsg = libError, msg
		return
	}
	if err := e.attachLocked(ctx, e.lib, db, nil); err != nil {
		db.Close()
		e.state, e.errMsg = libError, err.Error()
		ws.log.Error("reopen after rebuild failed", "id", e.id, "err", err)
	}
}

// isClosed reports whether the workspace has been shut down.
func (ws *workspace) isClosed() bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.closed
}

// closeAll closes every library (shutdown); later requests get 503.
func (ws *workspace) closeAll() {
	ws.mu.Lock()
	ws.closed = true
	libs := make([]*libEntry, 0, len(ws.libs))
	for _, e := range ws.libs {
		libs = append(libs, e)
	}
	ws.mu.Unlock()
	for _, e := range libs {
		e.closeAll()
	}
}

// createLibrary creates <root>/<name> with an empty catalog and returns
// its listing entry. The name must be a valid, non-reserved id that does
// not exist yet (errExists).
func (ws *workspace) createLibrary(ctx context.Context, name string) (listed, error) {
	p := filepath.Join(ws.root, name)
	if err := os.Mkdir(p, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return listed{}, errExists
		}
		return listed{}, err
	}
	lib, err := library.Open(p)
	if err != nil {
		return listed{}, err
	}
	db, err := catalog.Open(ctx, lib.DBPath(), ws.log)
	if err != nil {
		return listed{}, err
	}
	db.Close()
	return listed{id: name, path: p, library: true}, nil
}

var errExists = errors.New("a folder with this name already exists")

// describeErr is the user-facing text of a library error.
func describeErr(err error) string {
	if errors.Is(err, catalog.ErrNeedsRebuild) {
		return fmt.Sprintf("the catalog needs a rebuild: %v", err)
	}
	return err.Error()
}
