package server

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"sync"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// libJSON is the lib object of /api/libraries.
type libJSON struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Path    string       `json:"path"`
	State   string       `json:"state"`
	Summary *summaryJSON `json:"summary,omitempty"`
	Cover   *string      `json:"cover,omitempty"`
	Holder  *holderJSON  `json:"holder,omitempty"`
	Error   string       `json:"error,omitempty"`
	// Backup is the library's backup status; omitted when it has no
	// backup targets.
	Backup *libBackupJSON `json:"backup,omitempty"`
	// InterruptedMoves are the unfinished moves from or into the library
	// (docs/design/library-move.md); omitted when none.
	InterruptedMoves []interruptedJSON `json:"interruptedMoves,omitempty"`
}

// libBackupJSON is the backup one-liner of a library: targets and the
// newest backup (Unix seconds or null) from user.db, read-only for a
// closed library; behind (files) only for an open library whose plan
// for the most recently used target is cached.
type libBackupJSON struct {
	Targets      int    `json:"targets"`
	LastBackupAt *int64 `json:"lastBackupAt"`
	Label        string `json:"label,omitempty"`
	Behind       *int64 `json:"behind,omitempty"`
	// Incomplete: a target no backup finished to yet holds files of an
	// interrupted or failed one (the one with the most files).
	Incomplete *libIncompleteJSON `json:"incomplete,omitempty"`
	// Failure: the newest backup that stopped before it was done, when
	// none to that target finished after it.
	Failure *libFailureJSON `json:"failure,omitempty"`
}

type libIncompleteJSON struct {
	Label        string `json:"label"`
	Files        int64  `json:"files"`
	LibraryFiles int64  `json:"libraryFiles"` // 0: unknown
}

type libFailureJSON struct {
	Label     string `json:"label"`
	At        int64  `json:"at"`
	Error     string `json:"error,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// holderJSON describes the process holding a library's lock; fields are
// zero/null when it could not be read.
type holderJSON struct {
	PID   int     `json:"pid"`
	Cmd   string  `json:"cmd"`
	Since *string `json:"since"` // wall clock in the server's zone
}

// folderJSON is a subfolder of the workspace without media.db.
type folderJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"` // "folder"
}

type librariesJSON struct {
	Root      string       `json:"root"`
	Mode      string       `json:"mode"`
	Libraries []libJSON    `json:"libraries"`
	Folders   []folderJSON `json:"folders"`
}

func newSummary(sum catalog.Summary) *summaryJSON {
	return &summaryJSON{
		Total: sum.Total, Images: sum.Images, Raw: sum.Raw, Videos: sum.Videos,
		WithGPS: sum.WithGPS, Deleted: sum.Deleted, Bytes: sum.TotalBytes,
		Earliest: wallClock(sum.Earliest), Latest: wallClock(sum.Latest),
	}
}

func newHolder(e *library.ErrLocked) *holderJSON {
	if e == nil {
		return nil
	}
	return &holderJSON{PID: e.PID, Cmd: e.Cmd, Since: localWallClock(e.Since)}
}

// describe returns the lib object of a listed library: the registry's
// state when it has one, and the summary from a (cached) read-only peek
// of media.db. A peek failure makes a closed library an error. moves
// are the workspace's unfinished moves (allInterrupted).
func (s *Server) describe(ctx context.Context, l listed, e *libEntry, moves []listedInterrupted) libJSON {
	out := libJSON{ID: l.id, Name: l.id, Path: l.path, State: libClosed}
	if e != nil {
		e.mu.Lock()
		out.State, out.Error, out.Holder = e.state, e.errMsg, newHolder(e.holder)
		if e.lib != nil {
			out.Path = e.lib.Root()
		}
		e.mu.Unlock()
		if out.State == libFolder {
			out.State = libClosed // it has a media.db now
		}
	}
	if out.State == libRebuilding {
		return out
	}
	pk, err := s.ws.peek(ctx, filepath.Join(l.path, library.DBName))
	switch {
	case err != nil && out.State == libClosed:
		if ctx.Err() == nil {
			s.log.Info("cannot peek library", "id", l.id, "err", err)
		}
		out.State, out.Error = libError, publicLibError(err)
	case err == nil:
		out.Summary = newSummary(pk.Summary)
		if pk.HasCover {
			c := hexHash(pk.Cover)
			out.Cover = &c
		}
	}
	if out.State != libError {
		out.Backup = s.libBackup(ctx, l, e)
	}
	if mine := interruptedInvolving(moves, l.id); len(mine) > 0 {
		out.InterruptedMoves = mine
	}
	return out
}

// libBackup reads the backup status of a listed library: from the open
// user.db, else from a read-only peek of it.
func (s *Server) libBackup(ctx context.Context, l listed, e *libEntry) *libBackupJSON {
	var (
		sum userdata.BackupSummary
		ok  bool
	)
	if e != nil {
		if state, h := e.snapshot(); state == libOpen && h.user != nil {
			var err error
			sum, err = h.user.BackupSummary(ctx)
			ok = err == nil
		}
	}
	if !ok {
		var err error
		sum, ok, err = userdata.PeekBackupSummary(ctx, filepath.Join(l.path, userdata.FileName))
		if err != nil || !ok {
			return nil
		}
	}
	if sum.Targets == 0 {
		return nil
	}
	out := &libBackupJSON{Targets: sum.Targets, LastBackupAt: unixOrNull(sum.LastBackupAt), Label: sum.LastLabel}
	if i := sum.Incomplete; i != nil {
		out.Incomplete = &libIncompleteJSON{Label: i.Label, Files: i.Files, LibraryFiles: i.LibraryFiles}
	}
	if f := sum.Failure; f != nil {
		out.Failure = &libFailureJSON{Label: f.Label, At: f.At.Unix(), Error: f.Error, Cancelled: f.Cancelled}
	}
	if e != nil && sum.LastID != "" {
		if plan, ok := e.cachedPlanOf(sum.LastID, planTTL); ok {
			n := plan.New.Count + plan.Changed.Count + plan.Moved.Count
			out.Behind = &n
		}
	}
	return out
}

// handleLibraries lists the workspace: libraries (a subfolder with a
// regular media.db) and plain folders. In single mode the one library;
// in unset mode nothing.
func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	out, err := s.listLibraries(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// listLibraries is the /api/libraries body.
func (s *Server) listLibraries(ctx context.Context) (librariesJSON, error) {
	c := s.ws.cfg()
	all, err := s.ws.discover()
	if err != nil {
		return librariesJSON{}, err
	}
	out := librariesJSON{Root: c.root, Mode: c.mode(), Libraries: []libJSON{}, Folders: []folderJSON{}}
	// The peeks a listing needs run concurrently, at most peekWorkers at
	// a time; unchanged catalogs come from the cache.
	moves := allInterrupted(ctx, s.ws, s.log) // once for the listing
	var described []*libJSON
	var wg sync.WaitGroup
	sem := make(chan struct{}, peekWorkers)
	for _, l := range all {
		e := s.ws.entry(l.id)
		if !l.library {
			if e != nil {
				if st, _ := e.snapshot(); st == libRebuilding {
					out.Libraries = append(out.Libraries, libJSON{ID: l.id, Name: l.id, Path: l.path, State: st})
					continue
				}
			}
			out.Folders = append(out.Folders, folderJSON{ID: l.id, Name: l.id, Path: l.path, State: libFolder})
			continue
		}
		d := new(libJSON)
		described = append(described, d)
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			*d = s.describe(ctx, l, e, moves)
		})
	}
	wg.Wait()
	for _, d := range described {
		out.Libraries = append(out.Libraries, *d)
	}
	slices.SortFunc(out.Libraries, func(a, b libJSON) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

type createLibraryBody struct {
	Name string `json:"name"`
}

// handleLibraryCreate creates <root>/<name> with an empty catalog and
// opens it: 201 lib. Workspace mode only (400 in single mode); 400 for an
// invalid or reserved name, 409 when the folder exists.
func (s *Server) handleLibraryCreate(w http.ResponseWriter, r *http.Request) {
	// One snapshot of the configuration for the whole request: a
	// workspace switch meanwhile never makes it create elsewhere.
	c := s.ws.cfg()
	if c.root == "" {
		writeJSON(w, http.StatusConflict, errorUnsetJSON)
		return
	}
	if c.single {
		writeError(w, http.StatusBadRequest, "new libraries can only be created when serving a workspace")
		return
	}
	var body createLibraryBody
	if !decodeBody(w, r, &body) {
		return
	}
	name := body.Name
	if !validLibID(name) || reservedName(name) {
		writeError(w, http.StatusBadRequest, "invalid name: one folder name, not starting with a dot, without / \\ : < > \" | ? * or control characters, not a Windows device name (CON, NUL, COM1, ...), at most 255 bytes")
		return
	}
	if old := s.ws.entry(name); old != nil {
		if st, _ := old.snapshot(); st == libRebuilding {
			writeError(w, http.StatusConflict, "a library of this name is being rebuilt")
			return
		}
	}
	l, err := s.ws.createLibrary(r.Context(), c, name)
	if errors.Is(err, errExists) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// The folder is new: an entry left from a deleted folder of the same
	// name (open, holding its lock) is retired, never reused.
	e := s.ws.freshEntry(l)
	if err := s.ws.open(r.Context(), e); err != nil {
		s.openFailed(w, r, l, e, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.describe(r.Context(), l, e, allInterrupted(r.Context(), s.ws, s.log)))
}

// libEntry returns the registry entry of a listed library or folder, or
// answers 404.
func (s *Server) libEntry(w http.ResponseWriter, r *http.Request, id string) (*libEntry, bool) {
	if s.ws.isClosed() {
		writeError(w, http.StatusServiceUnavailable, errClosed.Error())
		return nil, false
	}
	if !validLibID(id) {
		writeError(w, http.StatusNotFound, errLibNotFound.Error())
		return nil, false
	}
	l, err := s.ws.find(id)
	if errors.Is(err, errLibNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	e := s.ws.entryFor(l)
	if l.library {
		e.mu.Lock()
		if e.state == libFolder {
			e.state = libClosed // media.db appeared
		}
		e.mu.Unlock()
	}
	return e, true
}

// handleLibraryOpen opens a library (lock, store, user.db): 200 lib;
// 409 {error, state: "busy", holder} when another process holds it, 409
// {error, state: "error"} for an unreadable catalog, 409 {error, state:
// "folder"} for a folder without media.db, 503 while it is rebuilt.
func (s *Server) handleLibraryOpen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("lib")
	if !validLibID(id) {
		writeError(w, http.StatusNotFound, errLibNotFound.Error())
		return
	}
	l, err := s.ws.find(id)
	if errors.Is(err, errLibNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	e, _ := s.libEntry(w, r, id)
	if e == nil {
		return
	}
	switch st, _ := e.snapshot(); {
	case st == libRebuilding:
		writeJSON(w, http.StatusServiceUnavailable, stateError{Error: "the library is being rebuilt", State: st})
		return
	case !l.library:
		writeJSON(w, http.StatusConflict, stateError{Error: "the folder has no media.db; record its files with a rebuild first", State: libFolder})
		return
	}
	if err := s.ws.open(r.Context(), e); err != nil {
		s.openFailed(w, r, l, e, err)
		return
	}
	writeJSON(w, http.StatusOK, s.describe(r.Context(), l, e, allInterrupted(r.Context(), s.ws, s.log)))
}

// openBusyJSON is the 409 body when another process holds the library.
type openBusyJSON struct {
	Error  string      `json:"error"`
	State  string      `json:"state"`
	Holder *holderJSON `json:"holder"`
}

// openFailed answers a failed open: 409 with the holder for a busy
// library, 409 with the reason for an unreadable one.
func (s *Server) openFailed(w http.ResponseWriter, r *http.Request, l listed, e *libEntry, err error) {
	var locked *library.ErrLocked
	if errors.Is(err, errClosed) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if errors.Is(err, errRetired) {
		writeJSON(w, http.StatusConflict, stateError{Error: err.Error(), State: libClosed})
		return
	}
	if errors.As(err, &locked) {
		msg := err.Error()
		if ownLock(locked) {
			msg = errOwnLock.Error()
		}
		writeJSON(w, http.StatusConflict, openBusyJSON{Error: msg, State: libBusy, Holder: newHolder(locked)})
		return
	}
	s.log.Info("cannot open library", "id", l.id, "err", err)
	writeJSON(w, http.StatusConflict, stateError{Error: publicLibError(err), State: libError})
}
