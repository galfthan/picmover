package server

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importrec"
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
	drainTimeout = 10 * time.Second // a rebuild waits this long for in-flight reads
	maxIDBytes   = 255
	peekWorkers  = 4 // concurrent peeks of one listing
)

// errLibNotFound: the id is malformed or not in the workspace listing.
var errLibNotFound = errors.New("no such library")

// errReplaced: the library folder was replaced or moved while open (the
// lock file at its path is not the one this server holds).
var errReplaced = errors.New("the library folder was replaced or moved while it was open; open it again")

// errReplacedBusy: an open of a replaced library while requests still use
// the old handles.
var errReplacedBusy = errors.New("the library folder was replaced; requests on the old folder are still running, try again")

// errViaSymlink: a library is never created through a symbolic link.
var errViaSymlink = errors.New("this entry is a symbolic link; a library is never created through a link: rebuild the folder it points to")

// errNotRegularCatalog: media.db is not a regular file.
var errNotRegularCatalog = errors.New("media.db is not a regular file")

// libEntry is one library the server has opened or tried to open. Fields
// below mu are guarded by it.
type libEntry struct {
	id   string
	path string // <root>/<id>; in single mode the library root

	// trashMu serialises the operations that move or delete files and
	// update the catalog: trash, restore, delete, an import's restore of
	// trashed content, and a whole rebuild (beginRebuild to endRebuild).
	trashMu sync.Mutex

	mu     sync.Mutex
	state  string
	errMsg string
	holder *library.ErrLocked
	lib    *library.Library
	db     *catalog.DB
	store  *thumbs.Store
	user   *userdata.DB
	// unlock releases the library lock this process holds (nil: not
	// held). In single mode the lock belongs to cmd/serve: unlock is a
	// no-op and external is set, so it is never taken or released here.
	unlock   func()
	external bool
	lockInfo os.FileInfo // the lock file as it was when the lock was taken
	stale    bool        // the folder was replaced: close the handles once inflight drops to 0
	inflight int         // requests using the handles

	// planMu guards plans, the cached backup plans by target id.
	planMu sync.Mutex
	plans  map[string]cachedPlan
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
	defer e.mu.Unlock()
	e.inflight--
	if e.stale && e.inflight == 0 {
		e.closeHandlesLocked()
	}
}

// snapshot returns the state and the handles without counting a request
// (for listings and jobs, which the state machine already orders).
func (e *libEntry) snapshot() (state string, h *libHandles) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state, &libHandles{id: e.id, entry: e, lib: e.lib, db: e.db, store: e.store, user: e.user}
}

// closeCatalogLocked closes the catalog and the store (not user.db, not
// the lock).
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

// closeHandlesLocked closes every handle and releases the lock (unless
// cmd/serve owns it).
func (e *libEntry) closeHandlesLocked() {
	e.closeCatalogLocked()
	if e.user != nil {
		e.user.Close()
		e.user = nil
	}
	if e.unlock != nil && !e.external {
		e.unlock()
		e.unlock, e.lockInfo = nil, nil
	}
	e.stale = false
}

// closeAll closes every handle and releases the lock (shutdown).
func (e *libEntry) closeAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeHandlesLocked()
	if e.external && e.unlock != nil {
		e.unlock()
		e.unlock = nil
	}
	if e.state == libOpen || e.state == libRebuilding {
		e.state = libClosed
	}
}

// sameFolderLocked reports whether the folder at e.path is still the one
// whose lock this process holds: the lock file there is the file locked
// (os.SameFile). It is true when no lock is held.
func (e *libEntry) sameFolderLocked() bool {
	if e.unlock == nil || e.lockInfo == nil {
		return true
	}
	fi, err := os.Stat(filepath.Join(e.path, library.MetaDir, library.LockName))
	return err == nil && os.SameFile(fi, e.lockInfo)
}

// checkFolder is called before every mutation of an open library (trash,
// restore, delete, job start): when the folder was replaced or moved
// while open, the entry becomes error (errReplaced) and its handles are
// closed once no request uses them, so nothing acts on a folder whose
// lock this server does not hold.
func (e *libEntry) checkFolder() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sameFolderLocked() {
		return nil
	}
	e.markStaleLocked()
	return errReplaced
}

func (e *libEntry) markStaleLocked() {
	e.state, e.errMsg, e.holder = libError, errReplaced.Error(), nil
	e.stale = true
	if e.inflight == 0 {
		e.closeHandlesLocked()
	}
}

// lockLocked makes sure this process holds lib's lock for e: a lock it
// already holds on the same folder is reused (taking it again would
// collide with itself); one held on a replaced folder is dropped first.
// A busy library sets state busy and returns the *library.ErrLocked.
func (ws *workspace) lockLocked(ctx context.Context, e *libEntry, lib *library.Library) error {
	if e.unlock != nil {
		if e.sameFolderLocked() {
			return nil
		}
		if e.inflight > 0 {
			return errReplacedBusy
		}
		e.closeHandlesLocked()
	}
	unlock, err := lib.Lock(ctx, "serve")
	var locked *library.ErrLocked
	switch {
	case errors.As(err, &locked):
		e.state, e.holder = libBusy, locked
		return err
	case errors.Is(err, library.ErrLockUnsupported):
		ws.log.Warn("cannot lock the library; using it without the lock", "library", lib.Root(), "err", err)
	case err != nil:
		return err
	}
	e.unlock = unlock
	e.lockInfo, _ = os.Stat(lib.LockPath())
	return nil
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
	peeks  map[string]peekEntry // by library folder
}

// fileStamp identifies a file's content cheaply: size and mtime (zero when
// it does not exist).
type fileStamp struct {
	size  int64
	mtime time.Time
}

func stampOf(p string) fileStamp {
	fi, err := os.Stat(p)
	if err != nil {
		return fileStamp{size: -1}
	}
	return fileStamp{size: fi.Size(), mtime: fi.ModTime()}
}

// peekEntry is a cached successful peek and the stamps of media.db, its
// -wal and user.db it was taken at.
type peekEntry struct {
	db, wal, user fileStamp
	peek          catalog.Peek
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
	link    bool // the entry is a symbolic link (to a plain folder)
}

// discover lists the workspace: one ReadDir of the root. Hidden entries
// (dot names, the Windows hidden attribute) and invalid ids are left
// out; a symlinked entry is listed only when it resolves to a plain
// folder directly under the root (see linkTarget).
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
		link := false
		switch {
		case e.IsDir():
		case e.Type()&fs.ModeSymlink != 0:
			if !ws.linkOK(p) {
				continue
			}
			link = true
		default:
			continue
		}
		fi, err := os.Lstat(filepath.Join(p, library.DBName))
		out = append(out, listed{id: name, path: p, library: err == nil && fi.Mode().IsRegular(), link: link})
	}
	slices.SortFunc(out, func(a, b listed) int { return cmp.Compare(a.id, b.id) })
	return out, nil
}

// linkOK reports whether the symbolic link p may be listed: it resolves
// to a directory directly under the (resolved) root whose name does not
// start with a dot, and that directory is not a library and holds no
// .picmover (a link must never be a second name of a library, nor lead
// into one).
func (ws *workspace) linkOK(p string) bool {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(ws.root)
	if err != nil {
		root = ws.root
	}
	if filepath.Dir(resolved) != filepath.Clean(root) || strings.HasPrefix(filepath.Base(resolved), ".") {
		return false
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return false
	}
	return !isLibraryDir(resolved)
}

// isLibraryDir reports whether dir has a media.db or a .picmover.
func isLibraryDir(dir string) bool {
	for _, n := range []string{library.DBName, library.MetaDir} {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
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
	return ws.registerLocked(l)
}

func (ws *workspace) registerLocked(l listed) *libEntry {
	state := libClosed
	if !l.library {
		state = libFolder
	}
	e := &libEntry{id: l.id, path: l.path, state: state}
	ws.libs[l.id] = e
	return e
}

// freshEntry registers a new entry for a library just created at l. An
// old entry of the same id (its folder was deleted while it was open)
// is retired: its handles close once no request uses them.
func (ws *workspace) freshEntry(l listed) *libEntry {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if old := ws.libs[l.id]; old != nil {
		old.mu.Lock()
		if old.unlock != nil || old.db != nil || old.user != nil {
			old.markStaleLocked()
		}
		old.mu.Unlock()
	}
	return ws.registerLocked(l)
}

// peek returns the read-only peek of the catalog at dbPath, cached by
// the size and mtime of media.db, media.db-wal and user.db: a listing
// re-stats them and peeks again only when one changed. Only successful
// peeks are cached (never a cancelled or transient failure). A user.db
// that this picmover cannot open (another schema version) fails the peek
// with the same error the open would give.
func (ws *workspace) peek(ctx context.Context, dbPath string) (catalog.Peek, error) {
	dir := filepath.Dir(dbPath)
	userPath := filepath.Join(dir, userdata.FileName)
	key := peekEntry{db: stampOf(dbPath), wal: stampOf(dbPath + "-wal"), user: stampOf(userPath)}
	if key.db.size < 0 {
		_, err := os.Stat(dbPath)
		return catalog.Peek{}, err
	}
	ws.mu.Lock()
	c, ok := ws.peeks[dir]
	ws.mu.Unlock()
	if ok && c.db == key.db && c.wal == key.wal && c.user == key.user {
		return c.peek, nil
	}
	p, err := catalog.PeekFile(ctx, dbPath)
	if err == nil {
		err = userdata.CheckFile(ctx, userPath)
	}
	if err != nil {
		return catalog.Peek{}, err
	}
	key.peek = p
	ws.mu.Lock()
	ws.peeks[dir] = key
	ws.mu.Unlock()
	return p, nil
}

// open opens the library of e: the lock (reused when this process holds
// it), the catalog (never created), the thumbnail store (read-only,
// created empty when absent) and user.db. On failure the entry's state
// says why (busy, error).
func (ws *workspace) open(ctx context.Context, e *libEntry) error {
	if ws.isClosed() {
		return errClosed
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch e.state {
	case libRebuilding:
		return nil
	case libOpen:
		if e.sameFolderLocked() {
			return nil
		}
		e.markStaleLocked()
	}
	e.holder, e.errMsg = nil, ""
	lib, err := library.Attach(e.path)
	if err != nil {
		return ws.failLocked(e, libError, err)
	}
	if err := ws.lockLocked(ctx, e, lib); err != nil {
		var locked *library.ErrLocked
		if errors.As(err, &locked) {
			return err
		}
		return ws.failLocked(e, libError, err)
	}
	db, err := catalog.OpenExisting(ctx, lib.DBPath(), ws.log)
	if err != nil {
		ws.releaseLocked(e)
		return ws.failLocked(e, libError, err)
	}
	if err := e.attachLocked(ctx, lib, db); err != nil {
		db.Close()
		ws.releaseLocked(e)
		return ws.failLocked(e, libError, err)
	}
	ws.log.Info("library opened", "id", e.id, "root", lib.Root())
	backfillImports(ctx, db, e.user, ws.log)
	return nil
}

// backfillImports copies the catalog's import sessions into the import
// history of user.db the first time a library is opened
// (importrec.Backfill); a failure is logged and never fails the open.
func backfillImports(ctx context.Context, db *catalog.DB, user *userdata.DB, log *slog.Logger) {
	n, err := importrec.Backfill(ctx, db, user)
	switch {
	case err != nil:
		log.Error("import history backfill", "err", err)
	case n > 0:
		log.Debug("import history backfilled from the catalog", "records", n)
	}
}

// releaseLocked closes user.db and releases the lock of an entry that is
// not open (workspace mode; cmd/serve keeps the lock in single mode).
func (ws *workspace) releaseLocked(e *libEntry) {
	if e.inflight == 0 {
		e.closeHandlesLocked()
	}
}

// attachLocked completes an open with an opened catalog: store and
// user.db (kept when already open), state open.
func (e *libEntry) attachLocked(ctx context.Context, lib *library.Library, db *catalog.DB) error {
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
	e.state, e.errMsg, e.holder = libOpen, "", nil
	return nil
}

// failLocked sets the state and a client-safe reason (the detail goes to
// the log).
func (ws *workspace) failLocked(e *libEntry, state string, err error) error {
	ws.log.Info("library not opened", "id", e.id, "state", state, "err", err)
	e.state, e.errMsg = state, publicLibError(err)
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

// beginRebuild switches e to rebuilding: new requests get 503, running
// trash operations are waited for (e.trashMu, held until endRebuild, so
// no trash, restore or delete runs during a rebuild), in-flight reads are
// waited for (bounded by drainTimeout), then the catalog and the store
// are closed. A library that is not open (a folder, an unreadable
// catalog) is attached and locked first (a lock this process holds is
// reused); user.db is opened (and kept open). Every successful
// beginRebuild must be followed by exactly one endRebuild.
func (ws *workspace) beginRebuild(ctx context.Context, e *libEntry) (lib *library.Library, user *userdata.DB, err error) {
	e.mu.Lock()
	switch e.state {
	case libRebuilding:
		e.mu.Unlock()
		return nil, nil, errors.New("the library is already being rebuilt")
	case libOpen:
		if !e.sameFolderLocked() {
			e.markStaleLocked()
			e.mu.Unlock()
			return nil, nil, errReplaced
		}
	default:
		e.holder = nil
		if fi, err := os.Lstat(e.path); err == nil && fi.Mode()&fs.ModeSymlink != 0 && !ws.single {
			if _, err := os.Lstat(filepath.Join(e.path, library.DBName)); err != nil {
				e.mu.Unlock()
				return nil, nil, errViaSymlink
			}
		}
		lib, err := library.Attach(e.path)
		if err != nil {
			e.mu.Unlock()
			return nil, nil, err
		}
		if err := ws.lockLocked(ctx, e, lib); err != nil {
			e.mu.Unlock()
			return nil, nil, err
		}
		if e.user == nil {
			u, err := userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName))
			if err != nil {
				ws.releaseLocked(e)
				e.mu.Unlock()
				return nil, nil, err
			}
			e.user = u
		}
		e.lib = lib
	}
	e.state = libRebuilding
	lib, user = e.lib, e.user
	e.mu.Unlock()

	e.trashMu.Lock() // released by endRebuild
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
	return lib, user, nil
}

// endRebuild ends a rebuild: with a catalog db the library is open on it;
// with nil the rebuild left no usable catalog: the entry is a folder
// again when there is no media.db, else error with failure's reason, and
// the lock and user.db are released (cmd/serve keeps its lock in single
// mode).
func (ws *workspace) endRebuild(ctx context.Context, e *libEntry, db *catalog.DB, failure error) {
	defer e.trashMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if db != nil {
		if err := e.attachLocked(ctx, e.lib, db); err != nil {
			db.Close()
			ws.failLocked(e, libError, err)
			ws.releaseLocked(e)
		}
		return
	}
	e.closeCatalogLocked()
	if _, err := os.Lstat(filepath.Join(e.path, library.DBName)); errors.Is(err, fs.ErrNotExist) && !ws.single {
		e.state, e.errMsg = libFolder, ""
	} else {
		if failure == nil {
			failure = errors.New("rebuild failed")
		}
		ws.failLocked(e, libError, failure)
	}
	ws.releaseLocked(e)
}

// isClosed reports whether the workspace has been shut down.
func (ws *workspace) isClosed() bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.closed
}

// closeAll closes every library (the last phase of a shutdown); later
// requests get 503.
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

// isReadOnlyErr reports a write refused by permissions or a read-only
// file system.
func isReadOnlyErr(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}

// publicLibError is the client-facing reason a library cannot be opened,
// peeked or rebuilt: a short text without paths or raw OS errors (the
// detail is logged where the error happens).
func publicLibError(err error) string {
	var locked *library.ErrLocked
	var ve *userdata.VersionError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &locked):
		return locked.Error()
	case errors.Is(err, errReplaced), errors.Is(err, errReplacedBusy), errors.Is(err, errViaSymlink),
		errors.Is(err, errNotRegularCatalog), errors.Is(err, errClosed):
		return err.Error()
	case errors.As(err, &ve):
		return ve.Error()
	case errors.Is(err, userdata.ErrNotUserDB):
		return "user.db is not a picmover user database"
	case errors.Is(err, catalog.ErrNewerSchema):
		return "the catalog was written by a newer picmover: upgrade picmover"
	case errors.Is(err, catalog.ErrNeedsRebuild):
		return "the catalog needs a rebuild: its format is from another picmover version"
	case errors.Is(err, catalog.ErrNotCatalog):
		return "media.db is not a picmover catalog"
	case isReadOnlyErr(err):
		return "the folder is read-only (picmover cannot create its lock file or write the catalog)"
	case errors.Is(err, syscall.ENOSPC):
		return "no space left on the device"
	case errors.Is(err, fs.ErrNotExist):
		return "the library folder or one of its files is missing"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	return genericLibError
}

// genericLibError is publicLibError's text for an error it does not know.
const genericLibError = "the catalog could not be read (see the server log)"
